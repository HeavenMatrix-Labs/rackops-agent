// Package platform provides per-platform Collector implementations for the
// RackOps Agent: CPU, memory, disks, network, temperature, uptime, and
// hardware specs. Each platform file implements the collect.Collector seam
// with stdlib-only platform access (procfs on Linux, sysctl on macOS, Win32
// P/Invoke on Windows) so the agent stays a small static binary.
//
// Platform differences are expressed as feature availability, not errors:
// a platform that cannot read a value (temperature on Windows, board info on
// VMs) skips its Collector and the snapshot marks it unavailable with a
// reason, per ADR-0001 ("not available on this Host").
package platform

import (
	"context"
	"os"
	"runtime"

	"rackops/agent/internal/collect"
)

// Collectors returns the platform's Collector set, base first so a
// snapshot never lacks hostname/OS/arch. Limited mode skips the
// privileged Collectors via CollectorSet.Skip; this function always
// returns the full set.
func Collectors() []collect.Collector {
	return append([]collect.Collector{baseCollector{}}, allCollectors()...)
}

// baseCollector fills the platform-independent parts of HostInfo from the
// standard library. It is always available and runs first so a snapshot
// never has an empty hostname/OS/arch.
type baseCollector struct{}

func (baseCollector) ID() string { return "hostinfo" }

func (baseCollector) Collect(_ context.Context, _ *collect.HostMetrics, info *collect.HostInfo) error {
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	info.OS = runtime.GOOS
	info.Arch = runtime.GOARCH
	return nil
}

// IsWindows reports whether the agent is running on Windows, where the
// install flow (and the app) must expect different service-management
// commands.
func IsWindows() bool { return runtime.GOOS == "windows" }
