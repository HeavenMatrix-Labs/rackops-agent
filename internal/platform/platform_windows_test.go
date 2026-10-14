//go:build windows

package platform

import (
	"context"
	"testing"

	"rackops/agent/internal/collect"
)

// These tests run on Windows and exercise the real Win32 Collectors,
// not just the seam: CI's smoke test covers the same surface through
// the running Agent, and these prove the Collector output itself is
// plausible.

func TestUptimeCollectorWindows(t *testing.T) {
	var m collect.HostMetrics
	var info collect.HostInfo
	if err := (&uptimeCollector{}).Collect(context.Background(), &m, &info); err != nil {
		t.Fatalf("uptime: %v", err)
	}
	if m.UptimeSeconds <= 0 {
		t.Fatalf("uptime = %v, want > 0", m.UptimeSeconds)
	}
	if info.Uptime != m.UptimeSeconds {
		t.Fatalf("info.Uptime = %v, want to match metrics", info.Uptime)
	}
}

func TestMemoryCollectorWindows(t *testing.T) {
	var m collect.HostMetrics
	if err := (&memoryCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("memory: %v", err)
	}
	if m.Memory.TotalBytes == 0 {
		t.Fatal("memory total = 0, want > 0")
	}
	if m.Memory.UsedBytes >= m.Memory.TotalBytes {
		t.Fatalf("used %d >= total %d", m.Memory.UsedBytes, m.Memory.TotalBytes)
	}
}

func TestDiskCollectorWindows(t *testing.T) {
	var m collect.HostMetrics
	if err := (&diskCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("disk: %v", err)
	}
	if len(m.Disks) == 0 {
		t.Fatal("no disks reported on a Windows host")
	}
	for _, d := range m.Disks {
		if d.TotalBytes == 0 {
			t.Fatalf("disk %s has zero total", d.MountPoint)
		}
	}
}

func TestCPUCollectorWindowsSecondSample(t *testing.T) {
	c := &cpuCollector{}
	ctx := context.Background()
	var m1 collect.HostMetrics
	if err := c.Collect(ctx, &m1, &collect.HostInfo{}); err != nil {
		t.Fatalf("first cpu sample: %v", err)
	}
	// First sample has no delta; the second must carry one.
	var m2 collect.HostMetrics
	if err := c.Collect(ctx, &m2, &collect.HostInfo{}); err != nil {
		t.Fatalf("second cpu sample: %v", err)
	}
	if m2.Cpu.UsagePercent < 0 || m2.Cpu.UsagePercent > 100 {
		t.Fatalf("cpu = %v, out of [0,100]", m2.Cpu.UsagePercent)
	}
}

func TestPlaceholderCollectorsReportUnavailable(t *testing.T) {
	// Temperature and network are honest "not available" on Windows:
	// the Collector errors and the snapshot marks the feature
	// unavailable with a reason.
	for _, c := range []collect.Collector{&temperatureCollector{}, &networkCollector{}} {
		var m collect.HostMetrics
		err := c.Collect(context.Background(), &m, &collect.HostInfo{})
		if err == nil {
			t.Fatalf("%s: expected unavailable error, got nil", c.ID())
		}
	}
}

func TestAllCollectorsWindowsIDs(t *testing.T) {
	set := collect.NewCollectorSet(Collectors()...)
	ids := set.IDs()
	if len(ids) == 0 {
		t.Fatal("no collectors on Windows")
	}
}
