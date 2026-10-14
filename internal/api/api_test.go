package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"rackops/agent/internal/collect"
)

// fakeCollector is the test double behind the Collector seam: it fills a
// fixed slice of the snapshot and never errors, so the API tests exercise
// the API, not the platform code.
type fakeCollector struct {
	id      collect.ID
	failure error
	mutate  func(m *collect.HostMetrics, info *collect.HostInfo)
}

func (f *fakeCollector) ID() collect.ID { return f.id }

func (f *fakeCollector) Collect(ctx context.Context, m *collect.HostMetrics, info *collect.HostInfo) error {
	if f.failure != nil {
		return f.failure
	}
	if f.mutate != nil {
		f.mutate(m, info)
	}
	return nil
}

func newTestServer(t *testing.T, set *collect.CollectorSet, token Token) *httptest.Server {
	t.Helper()
	srv := New(Options{
		Token:          token,
		Set:            set,
		HostName:       "test-host",
		Version:        "0.2.0",
		StreamInterval: 10 * time.Millisecond,
	})
	return httptest.NewServer(srv.Handler())
}

func TestTokenGenerateVerify(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if tok.String() == "" {
		t.Fatal("token string empty")
	}
	if !tok.Verify(tok.String()) {
		t.Fatal("token does not verify against itself")
	}
	if tok.Verify("wrong") {
		t.Fatal("token verified a wrong value")
	}
	if tok.Verify("") {
		t.Fatal("token verified empty")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	orig, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	loaded, err := LoadToken(orig.String())
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if !loaded.Verify(orig.String()) {
		t.Fatal("loaded token does not verify original")
	}
	if loaded.Verify("nope") {
		t.Fatal("loaded token verified wrong value")
	}
}

func TestLoadTokenRejectsEmpty(t *testing.T) {
	if _, err := LoadToken(""); err == nil {
		t.Fatal("LoadToken(\"\") should error")
	}
}

func TestHealthIsOpen(t *testing.T) {
	set := collect.NewCollectorSet()
	srv := newTestServer(t, set, mustToken(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMetricsRequiresToken(t *testing.T) {
	set := collect.NewCollectorSet(
		&fakeCollector{id: "cpu", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Cpu.UsagePercent = 42
		}},
	)
	srv := newTestServer(t, set, mustToken(t))
	defer srv.Close()

	// no token -> 401
	resp, err := http.Get(srv.URL + "/api/v1/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}

	// wrong token -> 401
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET wrong-token: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", resp2.StatusCode)
	}
}

func TestMetricsWithTokenReturnsSnapshot(t *testing.T) {
	set := collect.NewCollectorSet(
		&fakeCollector{id: "cpu", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Cpu.UsagePercent = 12.5
		}},
		&fakeCollector{id: "memory", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Memory.TotalBytes = 16 << 30
			m.Memory.UsedBytes = 8 << 30
		}},
	)
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var snap collect.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Metrics.Cpu.UsagePercent != 12.5 {
		t.Fatalf("cpu = %v, want 12.5", snap.Metrics.Cpu.UsagePercent)
	}
	if snap.Metrics.Memory.TotalBytes != 16<<30 {
		t.Fatalf("mem total = %d, want 16GiB", snap.Metrics.Memory.TotalBytes)
	}
	if f, ok := snap.Features["cpu"]; !ok || !f.Available {
		t.Fatalf("features[cpu] = %+v, want available", snap.Features["cpu"])
	}
	if snap.Metrics.Timestamp == 0 {
		t.Fatal("timestamp not set")
	}
}

func TestMetricsSkippedCollectorReportedUnavailable(t *testing.T) {
	// Simulates limited mode: the temperature collector was skipped at
	// construction. The snapshot must mark it unavailable with a reason,
	// so the app can show "not available on this Host".
	set := collect.NewCollectorSet(
		&fakeCollector{id: "cpu"},
		&fakeCollector{id: "temperature"},
	)
	set.Skip("temperature", "requires root")
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()

	var snap collect.Snapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	f := snap.Features["temperature"]
	if f.Available {
		t.Fatal("temperature should be unavailable")
	}
	if f.Reason == "" {
		t.Fatal("unavailable feature must carry a reason")
	}
}

func TestMetricsCollectorFailureIsIsolated(t *testing.T) {
	// One collector erroring must not break the others.
	set := collect.NewCollectorSet(
		&fakeCollector{id: "cpu", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Cpu.UsagePercent = 3
		}},
		&fakeCollector{id: "disk", failure: errBoom},
		&fakeCollector{id: "memory", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Memory.TotalBytes = 1 << 30
		}},
	)
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var snap collect.Snapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	if snap.Metrics.Cpu.UsagePercent != 3 {
		t.Fatalf("cpu still collected = %v", snap.Metrics.Cpu.UsagePercent)
	}
	if snap.Metrics.Memory.TotalBytes != 1<<30 {
		t.Fatalf("memory still collected = %d", snap.Metrics.Memory.TotalBytes)
	}
	if snap.Features["disk"].Available {
		t.Fatal("failing disk collector must be marked unavailable")
	}
}

func TestInfoEndpoint(t *testing.T) {
	set := collect.NewCollectorSet(
		&fakeCollector{id: "hardware", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			i.Hostname = "rack-01"
			i.Hardware.CPUModel = "Intel Xeon E3"
			i.Hardware.NumCPU = 8
		}},
	)
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/info", nil)
	req.Header.Set("Authorization", "Bearer "+tok.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET info: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	info, _ := body["info"].(map[string]any)
	if info == nil {
		t.Fatalf("missing info in body: %v", body)
	}
	if info["hostname"] != "rack-01" {
		t.Fatalf("hostname = %v, want rack-01", info["hostname"])
	}
}

func TestStreamEmitsSnapshots(t *testing.T) {
	set := collect.NewCollectorSet(
		&fakeCollector{id: "cpu", mutate: func(m *collect.HostMetrics, i *collect.HostInfo) {
			m.Cpu.UsagePercent = 7
		}},
	)
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/metrics/stream"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+tok.String())
	conn, _, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, header)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()

	// Read two frames to prove the ticker keeps emitting.
	for i := 0; i < 2; i++ {
		var snap collect.Snapshot
		if err := conn.ReadJSON(&snap); err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if snap.Metrics.Cpu.UsagePercent != 7 {
			t.Fatalf("frame %d cpu = %v, want 7", i, snap.Metrics.Cpu.UsagePercent)
		}
	}
}

func TestStreamRejectsBadToken(t *testing.T) {
	set := collect.NewCollectorSet()
	srv := newTestServer(t, set, mustToken(t))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/metrics/stream"
	header := http.Header{}
	header.Set("Authorization", "Bearer wrong")
	_, resp, err := websocket.DefaultDialer.DialContext(context.Background(), wsURL, header)
	if err == nil {
		// A successful dial with a bad token would be a bug.
		t.Fatal("websocket upgrade should reject a bad token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upgrade status = %d, want 401", resp.StatusCode)
	}
}

func TestTokenInQueryStringIsRejected(t *testing.T) {
	// A token in the query string leaks into proxy logs, browser
	// history and TLS-terminating middleware. The API must not accept
	// it, even though accepting it would be convenient.
	set := collect.NewCollectorSet(&fakeCollector{id: collect.IDCPU})
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/metrics?token=" + tok.String())
	if err != nil {
		t.Fatalf("GET metrics?token=: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query-string token status = %d, want 401", resp.StatusCode)
	}
}

func TestTokenEndpointDoesNotExist(t *testing.T) {
	// There is no endpoint that hands the raw token back over the wire,
	// even to an authenticated caller: a network-level token exfiltration
	// primitive is not something the Agent API needs.
	set := collect.NewCollectorSet()
	tok := mustToken(t)
	srv := newTestServer(t, set, tok)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/token", nil)
	req.Header.Set("Authorization", "Bearer "+tok.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/token status = %d, want 404", resp.StatusCode)
	}
}

func mustToken(t *testing.T) Token {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	return tok
}

var errBoom = errors.New("boom")
