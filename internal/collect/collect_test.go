package collect

import (
	"context"
	"errors"
	"testing"
)

// fakeCollector implements Collector for the CollectorSet tests.
type fakeCollector struct {
	id      string
	err     error
	fillCPU bool
}

func (f *fakeCollector) ID() string { return f.id }

func (f *fakeCollector) Collect(ctx context.Context, m *HostMetrics, info *HostInfo) error {
	if f.err != nil {
		return f.err
	}
	if f.fillCPU {
		m.Cpu.UsagePercent = 1
	}
	return nil
}

func TestCollectorSetCollectsAll(t *testing.T) {
	set := NewCollectorSet(
		&fakeCollector{id: "cpu", fillCPU: true},
		&fakeCollector{id: "memory"},
	)
	snap := set.Collect(context.Background())
	if snap.Metrics.Timestamp == 0 {
		t.Fatal("timestamp not set")
	}
	for _, id := range []string{"cpu", "memory"} {
		if f, ok := snap.Features[id]; !ok || !f.Available {
			t.Fatalf("features[%s] = %+v, want available", id, snap.Features[id])
		}
	}
}

func TestCollectorSetIsolatesFailures(t *testing.T) {
	set := NewCollectorSet(
		&fakeCollector{id: "cpu", fillCPU: true},
		&fakeCollector{id: "disk", err: errors.New("read-only fs")},
	)
	snap := set.Collect(context.Background())
	if snap.Metrics.Cpu.UsagePercent != 1 {
		t.Fatal("healthy collector was not run after a failure")
	}
	if f := snap.Features["disk"]; f.Available || f.Reason == "" {
		t.Fatalf("failing collector feature = %+v, want unavailable with reason", f)
	}
}

func TestCollectorSetSkipMarksUnavailable(t *testing.T) {
	set := NewCollectorSet(&fakeCollector{id: "temperature"})
	set.Skip("temperature", "requires root")
	snap := set.Collect(context.Background())
	if f := snap.Features["temperature"]; f.Available || f.Reason != "requires root" {
		t.Fatalf("skipped feature = %+v, want unavailable/requires root", f)
	}
}

func TestCollectorSetIDs(t *testing.T) {
	set := NewCollectorSet(
		&fakeCollector{id: "cpu"},
		&fakeCollector{id: "memory"},
	)
	ids := set.IDs()
	if len(ids) != 2 || ids[0] != "cpu" || ids[1] != "memory" {
		t.Fatalf("IDs() = %v", ids)
	}
}
