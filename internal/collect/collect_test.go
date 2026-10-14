package collect

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeCollector implements Collector for the CollectorSet tests.
type fakeCollector struct {
	id      ID
	err     error
	fillCPU bool
}

func (f *fakeCollector) ID() ID { return f.id }

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

// statefulCollector stands in for the rate-based Collectors (CPU,
// network): it mutates its own state on every call, so two overlapping
// Collect calls would race without the set's lock.
type statefulCollector struct {
	calls int
}

func (s *statefulCollector) ID() ID { return IDCPU }

func (s *statefulCollector) Collect(_ context.Context, m *HostMetrics, _ *HostInfo) error {
	s.calls++
	m.Cpu.UsagePercent = float64(s.calls)
	return nil
}

// TestCollectorSetConcurrentCollect pins the contract the Collector
// interface documents ("must be safe for concurrent use") and that the
// Agent API relies on: the metrics handlers and the WebSocket stream all
// call Collect from independent goroutines. Run with -race to see the
// data race this guards against.
func TestCollectorSetConcurrentCollect(t *testing.T) {
	c := &statefulCollector{}
	set := NewCollectorSet(c)

	const goroutines = 16
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				snap := set.Collect(context.Background())
				if snap.Metrics.Timestamp == 0 {
					t.Error("snapshot missing timestamp")
					return
				}
			}
		}()
	}
	wg.Wait()

	// Every call must have been accounted for: a lost update would mean
	// two Collect calls interleaved on the same collector.
	if c.calls != goroutines*iterations {
		t.Fatalf("collector saw %d calls, want %d (lost updates = interleaved Collects)",
			c.calls, goroutines*iterations)
	}
}
