// Package api exposes the Agent API v1: host info, on-demand metrics, and a
// periodic metrics stream, all behind token auth. TLS is applied at the
// server (cmd/agent); this package is transport-blind (the Transport is the
// user's choice: LAN, Tailscale, Cloudflare, VPN — see GLOSSARY).
//
// The API is the app's Agent Connector: the app holds an endpoint URL +
// token per Host and talks to exactly this surface (ADR-0001).
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"rackops/agent/internal/collect"
)

// Token is the per-Host registration secret. It is generated on first run
// and persisted (see cmd/agent), so the app and the Agent agree on it.
// The raw value is retained because `-show-token` and the install flow
// need to display it to the user; verification compares SHA-256 digests
// in constant time, so the raw value is never used on the request path.
type Token struct {
	raw    string
	sha256 string
}

// NewToken generates a random registration token.
func NewToken() (Token, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Token{}, err
	}
	return LoadToken(base64.RawURLEncoding.EncodeToString(b))
}

// String returns the raw token. Intended for one-time display.
func (t Token) String() string { return t.raw }

// Verify reports whether the presented token matches.
func (t Token) Verify(presented string) bool {
	if t.raw == "" || presented == "" {
		return false
	}
	sum := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(t.sha256)) == 1
}

// LoadToken re-hydrates a persisted token (raw base64 string) so the Agent
// can verify requests across restarts without regenerating.
func LoadToken(raw string) (Token, error) {
	if raw == "" {
		return Token{}, errors.New("api: empty token")
	}
	sum := sha256.Sum256([]byte(raw))
	return Token{raw: raw, sha256: hex.EncodeToString(sum[:])}, nil
}

// Server is the Agent API server. It is transport-blind: it serves an
// http.Handler that cmd/agent mounts on a plain or TLS listener.
type Server struct {
	token          Token
	set            *collect.CollectorSet
	hostName       string
	version        string
	log            *slog.Logger
	streamInterval time.Duration
}

// Options configure the server.
type Options struct {
	Token    Token
	Set      *collect.CollectorSet
	HostName string
	Version  string
	Log      *slog.Logger
	// StreamInterval controls how often the metrics stream emits a
	// snapshot. Defaults to 5s.
	StreamInterval time.Duration
}

// New builds a Server.
func New(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StreamInterval <= 0 {
		opts.StreamInterval = 5 * time.Second
	}
	return &Server{
		token:          opts.Token,
		set:            opts.Set,
		hostName:       opts.HostName,
		version:        opts.Version,
		log:            opts.Log,
		streamInterval: opts.StreamInterval,
	}
}

// Handler returns the Agent API v1 handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/info", s.requireToken(s.handleInfo))
	mux.HandleFunc("/api/v1/metrics", s.requireToken(s.handleMetrics))
	mux.HandleFunc("/api/v1/metrics/stream", s.requireToken(s.handleStream))

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	snap := s.set.Collect(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":  s.version,
		"agent":    "rackops-agent",
		"info":     snap.Info,
		"features": snap.Features,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.set.Collect(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// handleStream upgrades to a WebSocket and pushes one JSON Snapshot
// immediately, then every streamInterval until the client disconnects.
// This is the "WebSocket metrics stream" of the Agent API v1; each frame is
// a collect.Snapshot, the same shape as /api/v1/metrics.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		// The app (a phone) connects cross-origin over the user-chosen
		// Transport, so same-origin enforcement would reject every real
		// client.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("stream upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx := r.Context()
	if err := conn.WriteJSON(s.set.Collect(ctx)); err != nil {
		return
	}

	ticker := time.NewTicker(s.streamInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.WriteJSON(s.set.Collect(ctx)); err != nil {
				return
			}
		}
	}
}

func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The token is read from the Authorization header only. A
		// ?token= query fallback was tempting for clients that cannot set
		// headers, but it writes the secret into the request line, where
		// it lands in proxy logs, browser history and TLS-terminating
		// middleware logs. The app sets the header; it always can.
		presented := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(presented) > len(prefix) && presented[:len(prefix)] == prefix {
			presented = presented[len(prefix):]
		}
		if !s.token.Verify(presented) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next(w, r)
	}
}
