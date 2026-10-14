// Command agent is the RackOps per-host Agent: it runs on a Host,
// collects metrics and specs via the platform Collectors, and serves the
// Agent API v1 (host info, on-demand metrics, WebSocket metrics stream)
// behind token auth, optionally over TLS.
//
// Usage:
//
//	rackops-agent                 # run the Agent (reads RACKOPS_AGENT_* env)
//	rackops-agent -version        # print the version and exit
//
// Environment:
//
//	RACKOPS_AGENT_ADDR     listen address, default ":8443"
//	RACKOPS_AGENT_TLS_CERT cert file for TLS (PEM)
//	RACKOPS_AGENT_TLS_KEY  key file for TLS (PEM)
//	RACKOPS_AGENT_TOKEN    path to a registration token file (default:
//	                       generated on first run and written to the
//	                       standard path)
//	RACKOPS_AGENT_MODE     "full" (default) or "limited" (skip privileged
//	                       Collectors)
//
// The Agent is transport-blind: how the app reaches this Host (LAN,
// Tailscale, Cloudflare, VPN) is the user's choice (see GLOSSARY). The
// install flow displays the registration token once and persists it so
// restarts keep the same value.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"rackops/agent/internal/api"
	"rackops/agent/internal/collect"
	"rackops/agent/internal/platform"
)

// version is stamped at release-build time via -ldflags "-X main.version=...".
var version = "0.2.0"

// privilegedCollectorIDs are the slices that need privileges on some
// platforms (root on Linux/macOS for temperature sensors). Limited mode
// skips these so the Agent can run unprivileged and the app marks the
// corresponding features "not available on this Host".
var privilegedCollectorIDs = map[collect.ID]bool{
	collect.IPTemperature: true,
}

func main() {
	versionFlag := flag.Bool("version", false, "print the agent version and exit")
	showTokenFlag := flag.Bool("show-token", false, "print the persisted registration token and exit")
	addrFlag := flag.String("addr", envOr("RACKOPS_AGENT_ADDR", ":8443"), "listen address for the Agent API")
	tlsCert := flag.String("tls-cert", sentinelSafe("RACKOPS_AGENT_TLS_CERT"), "TLS certificate file (PEM)")
	tlsKey := flag.String("tls-key", sentinelSafe("RACKOPS_AGENT_TLS_KEY"), "TLS private key file (PEM)")
	// RACKOPS_AGENT_TOKEN is a *path* to a token file, not the token:
	// the token is 24 random bytes that must outlive the process, and
	// an env var is the wrong home for it (the service manager cannot
	// set it securely across reboots). The install flow writes the
	// file; the Agent reads it in on every start.
	tokenFile := flag.String("token", sentinelSafe("RACKOPS_AGENT_TOKEN"), "path to the registration token file (default: auto-generated + persisted)")
	mode := flag.String("mode", envOr("RACKOPS_AGENT_MODE", "full"), `run mode: "full" or "limited"`)
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}

	if *showTokenFlag {
		// Print the token without starting the service. This is the
		// "pasting it into the app" path: the installer or a human runs
		// `rackops-agent -show-token` and copies the value. If no token
		// file exists yet, generate + persist one and print that.
		tok, err := loadOrCreateToken(*tokenFile, slog.New(slog.DiscardHandler))
		if err != nil {
			fmt.Fprintln(os.Stderr, "rackops-agent:", err)
			os.Exit(1)
		}
		fmt.Println(tok.String())
		return
	}

	// A misconfigured token path is a fatal, user-facing error. Fail
	// fast with a clear message instead of logging and exiting silently.
	if *tokenFile != "" {
		if _, err := os.Stat(*tokenFile); err != nil {
			fmt.Fprintf(os.Stderr, "rackops-agent: -token file %s: %v\n", *tokenFile, err)
			os.Exit(1)
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *mode != "full" && *mode != "limited" {
		log.Error("invalid -mode", "mode", *mode, "want", "full|limited")
		os.Exit(1)
	}

	// Registration token: read the token file (when one is configured),
	// else generate + persist a new one so restarts keep the same value
	// (the app already holds the value from install time).
	token, err := loadOrCreateToken(*tokenFile, log)
	if err != nil {
		log.Error("token load-or-create failed", "err", err)
		os.Exit(1)
	}

	// Build the Collector set, skipping privileged Collectors in limited
	// mode so their slices come back marked unavailable.
	set := collect.NewCollectorSet(platform.Collectors()...)
	if *mode == "limited" {
		for _, id := range set.IDs() {
			if privilegedCollectorIDs[id] {
				set.Skip(id, "limited mode: requires privileges on this Host")
			}
		}
	}

	server := api.New(api.Options{
		Token:    token,
		Set:      set,
		HostName: hostName(),
		Version:  version,
		Log:      log,
	})

	ln, err := net.Listen("tcp", *addrFlag)
	if err != nil {
		log.Error("listen failed", "addr", *addrFlag, "err", err)
		os.Exit(1)
	}

	// Serve with TLS when a cert+key pair is configured. A broken pair
	// is a misconfiguration, not a transient condition: fail fast with
	// a clear log line instead of silently downgrading to plain HTTP.
	useTLS := *tlsCert != "" && *tlsKey != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			log.Error("TLS cert load failed - check the installer-generated pair or supply -tls-cert/-tls-key",
				"cert", *tlsCert, "key", *tlsKey, "err", err)
			os.Exit(1)
		}
		log.Info("rackops-agent listening", "addr", *addrFlag, "tls", true)
		if err := serveTLS(ln, server.Handler(), cert); err != nil {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	} else {
		log.Info("rackops-agent listening (plain HTTP - no TLS cert configured)", "addr", *addrFlag)
		if err := servePlain(ln, server.Handler()); err != nil {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}
}

// servePlain runs an HTTP server on ln.
func servePlain(ln net.Listener, h http.Handler) error {
	return (&http.Server{Handler: h}).Serve(ln)
}

// serveTLS runs a TLS server on ln using the provided cert.
func serveTLS(ln net.Listener, h http.Handler, cert tls.Certificate) error {
	return (&http.Server{
		Handler:   h,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}).ServeTLS(ln, "", "")
}

// loadOrCreateToken returns the API's registration token. If a token
// file path is supplied (via -token / RACKOPS_AGENT_TOKEN), it is read;
// otherwise the Agent looks for a persisted token at its standard
// location; if none, it generates one, persists it, and logs the value
// once so the install flow can display it.
//
// A missing configured token file is a hard error, not a "generate a
// new one" fallback: silently minting a fresh token would desync the
// Host from the app (which already holds the old token) and every API
// call would 401 — the exact failure mode a user has no way to
// diagnose. Misconfiguration must be loud.
func loadOrCreateToken(tokenFilePath string, log *slog.Logger) (api.Token, error) {
	if tokenFilePath != "" {
		data, err := os.ReadFile(tokenFilePath)
		if err != nil {
			return api.Token{}, fmt.Errorf("token file %s: %w (delete the -token/RACKOPS_AGENT_TOKEN setting to regenerate)", tokenFilePath, err)
		}
		if raw := strings.TrimSpace(string(data)); raw != "" {
			return api.LoadToken(raw)
		}
		return api.Token{}, fmt.Errorf("token file %s is empty", tokenFilePath)
	}
	path := tokenPath()
	if data, err := os.ReadFile(path); err == nil {
		if raw := strings.TrimSpace(string(data)); raw != "" {
			return api.LoadToken(raw)
		}
	}
	tok, err := api.NewToken()
	if err != nil {
		return api.Token{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return api.Token{}, err
	}
	if err := os.WriteFile(path, []byte(tok.String()), 0o600); err != nil {
		return api.Token{}, err
	}
	// Log where the token landed so the install flow can surface it, but
	// not the secret itself: stderr is routinely captured into journald /
	// launchd logs that outlive the process, and the token is the only
	// thing guarding the Agent API. `rackops-agent -show-token` prints
	// it on demand, on a machine the user is already logged into.
	log.Info("rackops-agent generated registration token", "path", path, "hint", "run: rackops-agent -show-token")
	return tok, nil
}

// tokenPath is where the Agent persists its registration token.
func tokenPath() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("ProgramData"); d != "" {
			return filepath.Join(d, "RackOps", "agent", "token")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "rackops", "agent", "token")
	}
	return filepath.Join(os.TempDir(), "rackops-agent", "token")
}

// hostName is the stable host identifier reported to the app.
func hostName() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown-host"
}

// envOr returns v if non-empty, else fallback.
func envOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// sentinelSafe returns an environment variable's value, treating a value
// equal to the variable's own name as "unset". Some hosts export their
// env vars as literal name placeholders (RACKOPS_AGENT_TOKEN=
// "RACKOPS_AGENT_TOKEN"); such a value is not a path, and mistaking it
// for one makes the Agent fail with a confusing "file not found".
func sentinelSafe(name string) string {
	v := os.Getenv(name)
	if v == name {
		return ""
	}
	return v
}
