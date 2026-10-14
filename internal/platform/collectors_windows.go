//go:build windows

package platform

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"rackops/agent/internal/collect"
)

// kernel32 + Win32 P/Invoke, stdlib-only. The agent uses NewLazyDLL so
// no extra module is needed on Windows.
var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes   = kernel32.NewProc("GetSystemTimes")
	procGetTickCount64   = kernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetLogicalDriveStringsW = kernel32.NewProc("GetLogicalDriveStringsW")
	procGetLogicalCPUInformation = kernel32.NewProc("GetLogicalProcessorInformation")
)

// MEMORYSTATUSEx mirrors the Win32 struct (16 bytes + 1 DWORD padding).
type memoryStatusEx struct {
	Length         uint32
	MemoryLoad     uint32
	TotalPhys      uint64
	AvailPhys      uint64
	TotalPageFile  uint64
	AvailPageFile  uint64
	TotalVirtual   uint64
	AvailExtended  uint64
	AvailExtendedReserved uint64
}

// fileTime is two 32-bit halves of a 64-bit FILETIME.
type fileTime struct {
	lo, hi uint32
}

// uptimeCollector reads the system's up time in milliseconds.
type uptimeCollector struct{}

func (c *uptimeCollector) ID() string { return "uptime" }

func (c *uptimeCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	r, _, _ := procGetTickCount64.Call()
	ms := uint64(r)
	sec := float64(ms) / 1000
	m.UptimeSeconds = sec
	info.Uptime = sec
	return nil
}

// cpuCollector reports aggregate CPU usage from GetSystemTimes deltas.
// GetSystemTimes returns idle/kernel/user file-times; kernel already
// includes idle, so total = kernel - idle + user.
type cpuCollector struct {
	prevIdle, prevKernel, prevUser uint64
}

func (c *cpuCollector) ID() string { return "cpu" }

func (c *cpuCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	var idle, kernel, user fileTime
	r, _, errno := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return fmt.Errorf("windows cpu: GetSystemTimes: %v", errno)
	}
	idleNS := filetime(&idle)
	kernelNS := filetime(&kernel)
	userNS := filetime(&user)
	totalNS := kernelNS + userNS // kernel already includes idle
	if c.prevKernel == 0 {
		c.prevIdle, c.prevKernel, c.prevUser = idleNS, totalNS, userNS
		return nil
	}
	dTotal := totalNS - c.prevKernel
	dIdle := idleNS - c.prevIdle
	c.prevIdle, c.prevKernel, c.prevUser = idleNS, totalNS, userNS
	if dTotal <= 0 {
		m.Cpu.UsagePercent = 0
		return nil
	}
	m.Cpu.UsagePercent = 100 * float64(dTotal-dIdle) / float64(dTotal)
	return nil
}

func filetime(ft *fileTime) uint64 {
	return uint64(ft.hi)<<32 | uint64(ft.lo)
}

// memoryCollector reports physical memory via GlobalMemoryStatusEx.
type memoryCollector struct{}

func (c *memoryCollector) ID() string { return "memory" }

func (c *memoryCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, errno := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return fmt.Errorf("windows memory: GlobalMemoryStatusEx: %v", errno)
	}
	m.Memory.TotalBytes = st.TotalPhys
	m.Memory.UsedBytes = st.TotalPhys - st.AvailPhys
	return nil
}

// diskCollector enumerates the drive letters and reports per-letter
// capacity via GetDiskFreeSpaceExW.
type diskCollector struct{}

func (c *diskCollector) ID() string { return "disk" }

func (c *diskCollector) Collect(_ context.Context, m *collect.HostMetrics, _ *collect.HostInfo) error {
	buf := make([]uint16, 512)
	r, _, _ := procGetLogicalDriveStringsW.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 {
		return fmt.Errorf("windows disk: GetLogicalDriveStringsW failed")
	}
	// GetLogicalDriveStringsW fills a NUL-separated list of drive roots
	// like "C:\" "D:\0"; we stop at the trailing empty entry.
	for _, drive := range windowsStrings(buf) {
		if drive == "" {
			continue
		}
		drivePtr, err := syscall.UTF16PtrFromString(drive)
		if err != nil {
			continue
		}
		var total, free, avail uint64
		r, _, _ := procGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(drivePtr)),
			uintptr(unsafe.Pointer(&total)),
			uintptr(unsafe.Pointer(&free)),
			uintptr(unsafe.Pointer(&avail)))
		if r == 0 {
			continue
		}
		// Use avail (available to the calling user), not free, to match
		// the Linux behavior of reporting the user-visible space.
		m.Disks = append(m.Disks, collect.Disk{
			MountPoint: drive,
			TotalBytes: total,
			FreeBytes:  avail,
			FSType:     "NTFS",
		})
	}
	return nil
}

// networkCollector is a placeholder: enumerating per-interface byte rates
// needs the IP Helper API (iphlpapi.dll) which we don't link; the Agent
// marks the slice unavailable with a reason so the app degrades gracefully.
type networkCollector struct{}

func (c *networkCollector) ID() string { return "network" }

func (c *networkCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("windows network: not available in stdlib-only build")
}

// temperatureCollector: Windows has no stdlib-accessible temperature API
// (it's a WMI/SMIClass story). The Agent reports it unavailable, which is
// the ticket's explicit requirement: "Windows reports 'not available'
// gracefully".
type temperatureCollector struct{}

func (c *temperatureCollector) ID() string { return "temperature" }

func (c *temperatureCollector) Collect(_ context.Context, _ *collect.HostMetrics, _ *collect.HostInfo) error {
	return fmt.Errorf("windows temperature: not available on this Host")
}

// hardwareCollector fills in the logical CPU count; everything else
// (CPU model, memory model, board) needs WMI which the stdlib-only build
// doesn't link, so those fields stay empty / unavailable.
type hardwareCollector struct{}

func (c *hardwareCollector) ID() string { return "hardware" }

func (c *hardwareCollector) Collect(_ context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	info.Hardware.NumCPU = runtime.NumCPU()
	// Reuse the memory Collector's value when it's already filled so the
	// Hardware block is self-consistent with the live Memory slice.
	if m.Memory.TotalBytes > 0 {
		info.Hardware.MemoryTotalBytes = m.Memory.TotalBytes
	}
	return nil
}

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

// windowsStrings splits a null-terminated UTF-16 buffer into the
// individual strings it contains.
func windowsStrings(buf []uint16) []string {
	var out []string
	start := 0
	for i, u := range buf {
		if u == 0 {
			out = append(out, syscall.UTF16ToString(buf[start:i]))
			start = i + 1
			if i == len(buf)-1 {
				break // trailing null; done
			}
		}
	}
	// The buffer is double-null-terminated; stop at the first empty.
	var trimmed []string
	for _, s := range out {
		if s == "" {
			break
		}
		trimmed = append(trimmed, s)
	}
	return trimmed
}
