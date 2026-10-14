//go:build linux

package platform

import (
	"context"
	"testing"

	"rackops/agent/internal/collect"
)

// Linux Collector tests: these verify that the real procfs/sysfs
// Collectors produce plausible values on a Linux host. CI's smoke test
// also runs the built Agent against the live API; these tests prove the
// underlying Collector output is correct independently.

func TestUptimeCollectorLinux(t *testing.T) {
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
	var m collect.HostMetrics
	if err := (&memoryCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("memory: %v", err)
	}
	if m.Memory.TotalBytes == 0 {
		t.Fatal("memory total = 0")
	}
}

func TestDiskCollectorLinux(t *testing.T) {
	var m collect.HostMetrics
	if err := (&diskCollector{}).Collect(context.Background(), &m, &collect.HostInfo{}); err != nil {
		t.Fatalf("disk: %v", err)
	}
	if len(m.Disks) == 0 {
		t.Fatal("no disks reported on a Linux host")
	}
}

func TestCPUCollectorLinuxSecondSample(t *testing.T) {
	c := &cpuCollector{}
	ctx := context.Background()
	var m1 collect.HostMetrics
	if err := c.Collect(ctx, &m1, &collect.HostInfo{}); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	var m2 collect.HostMetrics
	if err := c.Collect(ctx, &m2, &collect.HostInfo{}); err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if m2.Cpu.UsagePercent < 0 || m2.Cpu.UsagePercent > 100 {
		t.Fatalf("cpu = %v, out of [0,100]", m2.Cpu.UsagePercent)
	}
}

func TestHardwareCollectorLinux(t *testing.T) {
	var m collect.HostMetrics
	var info collect.HostInfo
	if err := (&hardwareCollector{}).Collect(context.Background(), &m, &info); err != nil {
		t.Fatalf("hardware: %v", err)
	}
	if info.Hardware.NumCPU <= 0 {
		t.Fatalf("NumCPU = %d, want > 0", info.Hardware.NumCPU)
	}
}

func TestAllCollectorsLinuxIDs(t *testing.T) {
	set := collect.NewCollectorSet(allCollectors()...)
	ids := set.IDs()
	if len(ids) != 7 {
		t.Fatalf("want 7 Collectors, got %v", ids)
	}
}
