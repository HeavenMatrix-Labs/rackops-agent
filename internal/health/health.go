// Package health exposes the minimal liveness endpoint of the RackOps Agent.
// The full Agent API (token auth, TLS, metrics, actions, alert rules) lands in
// ticket 02; until then this is the only surface.
package health

import (
	"net/http"
	"strings"
)

// Handler returns the health handler. GET /health answers 200 with "ok".
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// Serve is a convenience wrapper used by tests and the binary.
func Serve(addr string) error {
	srv := &http.Server{Addr: addr, Handler: Handler()}
	if err := srv.ListenAndServe(); err != nil && !strings.HasPrefix(err.Error(), "http: Server.ListenAndServe") {
		return err
	}
	return nil
}
