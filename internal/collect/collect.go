// Package collect defines the Collector seam: the Agent's units of work that
// gather Host metrics and specs. The Agent API is tested behind this seam with
// fake Collectors; real platform Collectors live under internal/platform.
//
// A Collector collects one narrow slice of host data (CPU, memory, disks,
// network, temperature, uptime, hardware specs). Collectors that need
// privileges (e.g. temperature sensors on some platforms) are skipped in
// limited mode and their slices come back marked unavailable, so the app can
// degrade gracefully (ADR-0001 consequence: "not available on this Host").
package collect

import (
	"context"
	"sync"
	"time"
)

// ID names one slice of Host data a Collector is responsible for.
// It is a type, not a bare string, because IDs cross three boundaries
// (the Collector's ID method, CollectorSet.Skip, and the Agent's
// limited-mode list) where a typo would silently disable a slice
// instead of failing to compile.
type ID string

// The slices the Agent reports. A platform Collectors() returns one
// Collector per slice it can serve.
const (
	IDHostInfo    ID = "hostinfo"
	IDCPU         ID = "cpu"
	IDMemory      ID = "memory"
	IDDisk        ID = "disk"
	IDNetwork     ID = "network"
	IPTemperature ID = "temperature"
	IDUptime      ID = "uptime"
	IDHardware    ID = "hardware"
)

func (id ID) String() string { return string(id) }

// Collector gathers one slice of Host data. Implementations must be safe
// for concurrent use: the API and the metrics stream sample Collectors on
// overlapping schedules. CollectorSet.Collect serializes its callers, so
// an implementation that keeps state between calls (a rate computed from
// the previous sample, say) does not need its own lock.
type Collector interface {
	// ID is the stable, app-facing identifier of the slice, e.g. "cpu",
	// "memory", "disk", "network", "temperature", "uptime", "hardware".
	ID() ID

	// Collect fills in the collector's slice of the HostMetrics and
	// HostInfo structs. Collectors leave the other fields untouched.
	// Unavailable data must be left as zero values; the caller decides
	// how to present them (see Unavailable and Feature).
	Collect(ctx context.Context, m *HostMetrics, info *HostInfo) error
}

// HostMetrics is one sample of a Host's live metrics. All numeric fields use
// absolute units (bytes, hertz, celsius) so the app never has to guess.
// A field is present when its Collector ran; a Collector may leave a slice
// zero and report it unavailable via Feature.Available.
type HostMetrics struct {
	// Timestamp is Unix milliseconds of when the sample was taken.
	Timestamp int64 `json:"timestamp"`

	// Cpu is aggregate host CPU usage (all cores), percent 0-100.
	Cpu struct {
		UsagePercent float64 `json:"usagePercent"`
	} `json:"cpu"`

	// Memory is host physical memory, bytes.
	Memory struct {
		TotalBytes uint64 `json:"totalBytes"`
		UsedBytes  uint64 `json:"usedBytes"`
	} `json:"memory"`

	// Disks is one entry per mounted filesystem the Agent reports on.
	Disks []Disk `json:"disks"`

	// Network is aggregate traffic across non-internal interfaces, bytes.
	Network struct {
		BytesInPerSecond  float64 `json:"bytesInPerSecond"`
		BytesOutPerSecond float64 `json:"bytesOutPerSecond"`
	} `json:"network"`

	// Temperature is the highest observed host temperature, celsius.
	// Nil when no temperature sensor is readable (Windows, limited mode).
	Temperature *float64 `json:"temperature"`

	// UptimeSeconds is the host's current uptime.
	UptimeSeconds float64 `json:"uptimeSeconds"`
}

// Disk describes one mounted filesystem.
type Disk struct {
	MountPoint string `json:"mountPoint"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	// FSType is e.g. "ext4", "btrfs", "NTFS"; empty when unknown.
	FSType string `json:"fsType,omitempty"`
}

// HostInfo is the host's relatively static specs, gathered once and updated
// lazily when cheap. The dashboard's "hardware specs" screen renders this.
type HostInfo struct {
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Uptime   float64  `json:"uptimeSeconds"`
	Hardware Hardware `json:"hardware"`
}

// Hardware holds the "specs" section of a host. Fields are pointers or
// empty strings where the platform genuinely cannot know them.
type Hardware struct {
	// CPUModel is e.g. "Intel(R) Xeon(R) CPU E3-1225 v3".
	// Empty when unavailable.
	CPUModel string `json:"cpuModel,omitempty"`
	// NumCPU is the logical CPU count.
	NumCPU int `json:"numCpu"`
	// MemoryTotalBytes, when known by the hardware collector.
	MemoryTotalBytes uint64 `json:"memoryTotalBytes,omitempty"`
	// Disks is a list of block devices (model/serial when readable),
	// distinct from the mounted-filesystem list in HostMetrics.
	Disks []HDDisk `json:"disks"`
	// Board is motherboard/vendor info when readable; empty otherwise.
	Board string `json:"board,omitempty"`
}

// HDDisk is a block device, not a mount.
type HDDisk struct {
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Size  uint64 `json:"sizeBytes"`
	IsSSD bool   `json:"isSsd"`
}

// Feature reports whether a collector ran on this host in the current mode.
// The app uses this to render "not available" instead of zeros (ADR-0001).
type Feature struct {
	Available bool `json:"available"`
	// Reason is a short, human-readable explanation when !Available,
	// e.g. "requires root", "not available on this Host".
	Reason string `json:"reason,omitempty"`
}

// Features is the per-Host map of feature availability the Agent API
// publishes.
type Features map[string]Feature

// Snapshot is one full sample of a Host: live metrics, static specs, and
// per-collector feature availability. The Agent API's on-demand metrics
// endpoint and the WebSocket stream both return Snapshot values, so the app
// always knows whether a field is a real zero or simply unavailable
// (ADR-0001: "not available on this Host" must never look like a failure).
type Snapshot struct {
	Metrics  HostMetrics `json:"metrics"`
	Info     HostInfo    `json:"info"`
	Features Features    `json:"features"`
}

// CollectorSet aggregates Collectors into a Snapshot and tracks which
// collectors were skipped at construction time (limited mode, platform
// without sensors, ...) so they surface in Features as unavailable.
//
// Collect serializes concurrent callers with a single lock. Several
// Collectors (CPU, network) compute a rate from a delta against their
// own previous sample, so they keep mutable state between calls; two
// overlapping Collect calls would race on that state and interleave
// two snapshots into one. Serializing here, once, is what makes the
// Collector interface's "safe for concurrent use" contract true for
// every implementation instead of asking each Collector to lock.
type CollectorSet struct {
	collectors []Collector
	skipped    map[ID]string

	mu sync.Mutex
}

// NewCollectorSet builds a set from the given Collectors.
func NewCollectorSet(cs ...Collector) *CollectorSet {
	return &CollectorSet{
		collectors: cs,
		skipped:    map[ID]string{},
	}
}

// Skip records that the collector with id was skipped and why.
func (s *CollectorSet) Skip(id ID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped[id] = reason
}

// Collect runs every collector and produces one Snapshot. An error from one
// collector does not stop the others: that collector's feature is marked
// unavailable with the error as reason.
//
// Collect is safe to call from multiple goroutines; callers are
// serialized.
func (s *CollectorSet) Collect(ctx context.Context) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	var m HostMetrics
	var info HostInfo
	features := Features{}
	m.Timestamp = time.Now().UnixMilli()

	for _, c := range s.collectors {
		id := c.ID().String()
		if reason, ok := s.skipped[c.ID()]; ok {
			features[id] = Feature{Available: false, Reason: reason}
			continue
		}
		if err := c.Collect(ctx, &m, &info); err != nil {
			features[id] = Feature{Available: false, Reason: err.Error()}
			continue
		}
		features[id] = Feature{Available: true}
	}
	return Snapshot{Metrics: m, Info: info, Features: features}
}

// IDs returns every collector ID in the set (running and skipped).
func (s *CollectorSet) IDs() []ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ID, 0, len(s.collectors))
	for _, c := range s.collectors {
		out = append(out, c.ID())
	}
	return out
}
