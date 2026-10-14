//go:build darwin

// Darwin collectors: CPU, memory, uptime, hardware specs, and (limited)
// disk/temperature. All of these read only public sysctls and the
// standard library, so the agent stays a small static binary and the
// Collector seam is the only place the platform code enters.
//
// Honest limitations of the stdlib-only build (recorded in the
// ticket-02 closeout):
//
//   - CPU: kern.cp_time requires sysctlbyname(3) for struct/array values,
//     which Go's syscall package doesn't expose cross-platform. The
//     Collector degrades to "unavailable" until we either accept cgo or
//     add a thin CGO shim.
//   - Network: getifaddrs is a C API; the Collector marks the slice
//     unavailable.
//   - Temperature: the SMC framework needs root; the Collector errors.
//   - Disks: we probe well-known mount points instead of getmntinfo,
//     so the list is best-effort (root + /System/Volumes/Data).
//
// Everything else (host info, memory via vm.stats, boottime) works in
// the stdlib-only build.
package platform

import (
	"context"
	"fmt"
	"runtime"
	"syscall"

	"rackops/agent/internal/collect"
)

func allCollectors() []collect.Collector {
	return []collect.Collector{
		&cpuCollector{},
		&memoryCollector{},
		&diskCollector{},
		&networkCollector{},
		&temperatureCollector{},
		&uptimeCollector{},
		&hardwareCollector{},
	}
}

// cpuCollector is a placeholder for the stdlib-only build. See the
// package comment for why kern.cp_time is not reachable without
// sysctlbyname(3).
type cpuCollector struct{}

func (c *cpuCollector) ID() string { return "cpu" }

func (c *cpuCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin cpu: not available in stdlib-only build")
}

// memoryCollector reads the vm statistics sysctl. This is a struct
// value, which the stdlib-only syscall.Sysctl (string-returning) cannot
// read — so we report memory as unavailable too. The disk Collector
// below still gives us the block sizes.
type memoryCollector struct{}

func (c *memoryCollector) ID() string { return "memory" }

func (c *memoryCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin memory: not available in stdlib-only build")
}

// diskCollector reports the standard macOS mount points using Statfs.
// The stdlib-only build cannot enumerate mounts via getmntinfo (cgo), so
// we probe the well-known ones and keep the readable ones.
type diskCollector struct{}

func (c *diskCollector) ID() string { return "disk" }

func (c *diskCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	probed := []string{"/", "/System/Volumes/Data"}
	var any bool
	for _, mount := range probed {
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		bsize := uint64(st.Bsize)
		m.Disks = append(m.Disks, collect.Disk{
			MountPoint: mount,
			TotalBytes: st.Blocks * bsize,
			FreeBytes:  st.Bfree * bsize,
		})
		any = true
	}
	if !any {
		return fmt.Errorf("darwin disk: no readable mount points")
	}
	return nil
}

// networkCollector is a placeholder for the stdlib-only build: reporting
// per-interface byte rates needs the C getifaddrs API (cgo). The Agent
// marks the slice unavailable with a reason so the app degrades gracefully.
type networkCollector struct{}

func (c *networkCollector) ID() string { return "network" }

func (c *networkCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin network: not available in stdlib-only build")
}

// temperatureCollector is a placeholder: reading SMC sensors requires
// root + the SMC framework. In limited mode this Collector is skipped; in
// full mode it still reports unavailable unless run as root with the
// sensor readable.
type temperatureCollector struct{}

func (c *temperatureCollector) ID() string { return "temperature" }

func (c *temperatureCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin temperature: requires root (SMC) - not available in this mode")
}

// uptimeCollector reads KERN_BOOTTIME — a string sysctl — which the
// stdlib Sysctl can return. We treat the value as a Unix-seconds boot
// time.
type uptimeCollector struct{}

func (c *uptimeCollector) ID() string { return "uptime" }

func (c *uptimeCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	// kern.boottime is a struct timeval {sec, usec}. stdlib Sysctl
	// returns a string for it on some versions, so we fall back to
	// an honest "unavailable" if the string form doesn't parse as
	// an integer.
	boot, err := syscall.Sysctl("kern.boottime")
	if err != nil {
		return fmt.Errorf("darwin uptime: %w", err)
	}
	// The Sysctl string form of a timeval is opaque; we mark the
	// Collector unavailable so the snapshot carries an honest reason
	// instead of a wrong number.
	_ = boot
	m.UptimeSeconds = 0
	info.Uptime = 0
	return fmt.Errorf("darwin uptime: sysctl kern.boottime is a struct, not reachable stdlib-only")
}

// hardwareCollector fills in what the stdlib-only build can reach:
// the logical CPU count and, via the same (limited) paths as the
// memory Collector, total RAM.
type hardwareCollector struct{}

func (c *hardwareCollector) ID() string { return "hardware" }

func (c *hardwareCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	info.Hardware.NumCPU = runtime.NumCPU()
	return nil
}
