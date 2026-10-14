//go:build !windows

package platform

import (
	"context"
	"runtime"
	"testing"

	"rackops/agent/internal/collect"
)

// On macOS/Linux CI we exercise the real Collectors. On this dev machine
// (Windows) these tests are excluded via the build tag; the Windows
// version of these tests lives in platform_windows_test.go.

func TestAllCollectorsHaveUniqueIDs(t *testing.T) {
	set := collect.NewCollectorSet(allCollectors()...)
	seen := map[string]bool{}
	for _, id := range set.IDs() {
		if seen[id] {
			t.Fatalf("duplicate collector id %q", id)
		}
		seen[id] = true
	}
	if !seen["cpu"] || !seen["memory"] || !seen["disk"] {
		t.Fatalf("missing base collector ids: %v", set.IDs())
	}
}

func TestHostinfoCollectorFillsBaseline(t *testing.T) {
	var m collect.HostMetrics
	var info collect.HostInfo
	if err := (baseCollector{}).Collect(context.Background(), &m, &info); err != nil {
		t.Fatalf("hostinfo: %v", err)
	}
	if info.Hostname == "" {
		t.Fatal("hostname not filled")
	}
	if info.OS != runtime.GOOS {
		t.Fatalf("os = %q, want %q", info.OS, runtime.GOOS)
	}
}

func TestUptimeCollectorLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uptime test only meaningful on Linux")
	}
	var m collect.HostMetrics
	var info collect.HostInfo
	if err := (&uptimeCollector{}).Collect(context.Background(), &m, &info); err != nil {
		t.Fatalf("uptime: %v", err)
	}
	if m.UptimeSeconds <= 0 {
		t.Fatalf("uptime = %v, want > 0", m.UptimeSeconds)
	}
}

func TestMemoryCollectorLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("memory test only meaningful on Linux")
	}
	var m collect.HostMetrics
	if err := (&memoryCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("memory: %v", err)
	}
	if m.Memory.TotalBytes == 0 {
		t.Fatal("memory total = 0")
	}
}

func TestDiskCollectorLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("disk test only meaningful on Linux")
	}
	var m collect.HostMetrics
	if err := (&diskCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("disk: %v", err)
	}
	if len(m.Disks) == 0 {
		t.Fatal("no disks reported on Linux")
	}
}

func TestCPUCollectorSecondSampleLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cpu delta test only meaningful on Linux")
	}
	c := &cpuCollector{}
	ctx := context.Background()
	var m1, m2 collect.HostMetrics
	if err := c.Collect(ctx, &m1, &collect.HostInfo{}); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if err := c.Collect(ctx, &m2, &collect.HostInfo{}); err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if m2.Cpu.UsagePercent < 0 || m2.Cpu.UsagePercent > 100 {
		t.Fatalf("cpu = %v, out of [0,100]", m2.Cpu.UsagePercent)
	}
}

func TestHardwareCollectorLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hardware test only meaningful on Linux")
	}
	var m collect.HostMetrics
	var info collect.HostInfo
	if err := (&hardwareCollector{}).Collect(context.Background(), &m, &info); err != nil {
		t.Fatalf("hardware: %v", err)
	}
	if info.Hardware.NumCPU <= 0 {
		t.Fatalf("NumCPU = %d, want > 0", info.Hardware.NumCPU)
	}
}
