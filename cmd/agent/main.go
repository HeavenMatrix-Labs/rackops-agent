package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"rackops/agent/internal/health"
)

// version is stamped at release-build time via -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	versionFlag := flag.Bool("version", false, "print the agent version and exit")
	addr := flag.String("addr", ":8443", "listen address for the health endpoint")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}

	// Skeleton: only the health endpoint exists yet. Ticket 02 adds the full
	// Agent API (metrics stream, actions, alert rules) behind token auth + TLS.
	server := http.Server{
		Addr:    *addr,
		Handler: health.Handler(),
	}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "rackops-agent:", err)
		os.Exit(1)
	}
}
