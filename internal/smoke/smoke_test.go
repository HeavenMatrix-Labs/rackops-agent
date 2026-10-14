// Package agent_smoke is the CI smoke test: it builds the real Agent
// binary, runs it locally with a generated token, and verifies the health
// and metrics endpoints answer with the right auth behavior. This is the
// "one-command install" proof — the exact path a user hits when they
// curl the Agent's endpoint after installing.
package agent_smoke

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"rackops/agent/internal/api"
	"rackops/agent/internal/collect"
)

// buildAgent compiles the agent binary for the host platform and returns
// its path.
func buildAgent(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "agent")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agent")
	// The test is compiled inside the module; Go sets the cwd to the
	// package dir (agent/internal/smoke). Walk two levels up to the
	// module root where ./cmd/agent lives.
	cmd.Dir = filepath.Join("..", "..")
	buf := new(bytes.Buffer)
	cmd.Stderr = buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build agent: %v\n%s", err, buf.String())
	}
	return out
}

// netListenForPort picks a free TCP port by binding :0.
func netListenForPort() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// runAgentWithMode starts the built agent with a generated token and the
// given mode, returns the base URL, the token, and a cleanup func that
// stops the process.
func runAgentWithMode(t *testing.T, bin, mode string) (base, token string, cleanup func()) {
	t.Helper()
	// Pick a free port by binding :0.
	l, err := netListenForPort()
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	tok, err := api.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	// The smoke test runs the agent in-process-style: pass the token as
	// a value (not a file) via the RACKOPS_AGENT_TOKEN env var.
	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte(tok.String()), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	cmd := exec.Command(bin, "-addr", addr, "-token", tokFile, "-mode", mode, "-tls-cert", "", "-tls-key", "")
	// Send stdout/stderr to the test log so a crash is visible.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}

	cleanup = func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	t.Cleanup(cleanup)

	// Wait for the health endpoint to answer.
	base = "http://" + addr
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, tok.String(), cleanup
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("agent never became healthy at %s", base)
	return "", "", cleanup
}

// TestSmokeAgentAPI builds and runs the real agent, then verifies:
//  1. /health is open (no token) — service-manager liveness.
//  2. /api/v1/metrics requires the token.
//  3. /api/v1/metrics with the token returns a well-formed Snapshot.
func TestSmokeAgentAPI(t *testing.T) {
	bin := buildAgent(t)
	base, tok, cleanup := runAgentWithMode(t, bin, "full")
	defer cleanup()

	// 1. /health is open.
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", resp.StatusCode)
	}

	// 2. /api/v1/metrics without a token -> 401.
	resp, err = http.Get(base + "/api/v1/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics no-token status = %d, want 401", resp.StatusCode)
	}

	// 3. /api/v1/metrics with the token -> 200 + a real Snapshot.
	req, _ := http.NewRequest("GET", base+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	var snap collect.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Metrics.Timestamp == 0 {
		t.Fatal("snapshot timestamp not set")
	}
	if len(snap.Features) == 0 {
		t.Fatal("snapshot has no features")
	}

	// The base hostinfo collector should always be available.
	if f := snap.Features["hostinfo"]; !f.Available {
		t.Fatalf("hostinfo feature = %+v, want available", f)
	}
}

// TestSmokeAgentVersionCommand checks that the built binary reports its
// version via -version (the release-build flag path).
func TestSmokeAgentVersionCommand(t *testing.T) {
	bin := buildAgent(t)
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		t.Fatalf("-version: %v", err)
	}
	ver := string(bytes.TrimSpace(out))
	if ver == "" {
		t.Fatal("empty version output")
	}
	t.Logf("agent version = %q", ver)
}

// TestSmokeLimitedModeSkipsPrivilegedCollectors proves limited mode
// actually skips the privileged Collectors and reports them unavailable.
func TestSmokeLimitedModeSkipsPrivilegedCollectors(t *testing.T) {
	bin := buildAgent(t)
	base, tok, cleanup := runAgentWithMode(t, bin, "limited")
	defer cleanup()

	req, _ := http.NewRequest("GET", base+"/api/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()

	var snap collect.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// In limited mode, temperature (the privileged one) must be marked
	// unavailable with a reason.
	if f := snap.Features["temperature"]; f.Available {
		t.Fatalf("temperature available in limited mode: %+v", f)
	}
	if f := snap.Features["hostinfo"]; !f.Available {
		t.Fatalf("hostinfo should stay available in limited mode: %+v", f)
	}
}
