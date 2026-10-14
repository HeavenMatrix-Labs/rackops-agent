//go:build darwin

// Darwin collectors. All of these use only the standard library, so the
// agent stays a small static binary and the Collector seam remains the
// only place platform code enters.
//
// Honest limitations of the stdlib-only build (recorded in the
// ticket-02 closeout):
//
//   - CPU, memory, uptime: Go's syscall.Sysctl exposes only
//     string- and uint32-valued sysctls. kern.cp_time, vm.stats2 and
//     kern.boottime are all struct/array sysctls that need
//     sysctlbyname(3) via cgo, so these slices report unavailable.
//   - Network: getifaddrs is a C API; the slice reports unavailable.
//   - Temperature: the SMC framework needs root; unavailable.
//   - Disks: we probe well-known mount points instead of getmntinfo, so
//     the list is best-effort.
//
// What does work on macOS today: host info (hostname, OS, arch), disk
// capacity via statfs, uptime-free specs, and the logical CPU count.
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

func (c *cpuCollector) ID() collect.ID { return collect.IDCPU }

func (c *cpuCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin cpu: not available in stdlib-only build")
}

// memoryCollector reads the vm statistics sysctl. This is a struct
// value, which the stdlib-only syscall.Sysctl (string-returning) cannot
// read — so we report memory as unavailable too. The disk Collector
// below still gives us the block sizes.
type memoryCollector struct{}

func (c *memoryCollector) ID() collect.ID { return collect.IDMemory }

func (c *memoryCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin memory: not available in stdlib-only build")
}

// diskCollector reports the standard macOS mount points using Statfs.
// The stdlib-only build cannot enumerate mounts via getmntinfo (cgo), so
// we probe the well-known ones and keep the readable ones.
type diskCollector struct{}

func (c *diskCollector) ID() collect.ID { return collect.IDDisk }

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

func (c *networkCollector) ID() collect.ID { return collect.IDNetwork }

func (c *networkCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin network: not available in stdlib-only build")
}

// temperatureCollector is a placeholder: reading SMC sensors requires
// root + the SMC framework. In limited mode this Collector is skipped; in
// full mode it still reports unavailable unless run as root with the
// sensor readable.
type temperatureCollector struct{}

func (c *temperatureCollector) ID() collect.ID { return collect.IPTemperature }

func (c *temperatureCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin temperature: requires root (SMC) - not available in this mode")
}

// uptimeCollector reports unavailable on darwin. kern.boottime is a
// struct timeval, and Go's stdlib syscall.Sysctl only exposes
// string- and uint32-valued sysctls, so there is no stdlib-only way to
// read the boot time. We do not call sysctl and discard the result: an
// honest error is better than a plausible-looking zero.
type uptimeCollector struct{}

func (c *uptimeCollector) ID() collect.ID { return collect.IDUptime }

func (c *uptimeCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("darwin uptime: kern.boottime is a struct sysctl, not reachable stdlib-only")
}

// hardwareCollector fills in what the stdlib-only build can reach: the
// logical CPU count. Total RAM needs the vm.stats2 struct sysctl, which
// the memory Collector reports as unavailable for.
type hardwareCollector struct{}

func (c *hardwareCollector) ID() collect.ID { return collect.IDHardware }

func (c *hardwareCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	info.Hardware.NumCPU = runtime.NumCPU()
	return nil
}
