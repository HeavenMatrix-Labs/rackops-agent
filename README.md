# rackops-agent

Per-host Agent for the RackOps homelab management app. Apache-2.0.

The Agent runs on a Host, collects its metrics and hardware specs, and
serves the Agent API v1 (host info, on-demand metrics, WebSocket metrics
stream) behind a per-Host registration token, optionally over TLS. The
RackOps app's Agent Connector talks to exactly this surface (ADR-0001).

## Data flow (what leaves this machine)

- **Out:** alert events (ticket 11) to the user's own RackOps backend,
  and the metrics stream to the user's own phone/app over the
  user-chosen Transport (LAN, Tailscale, Cloudflare, VPN). Nothing is
  sent to a public service.
- **In:** the user's own registration token and (later) alert rules.
- No analytics, no telemetry, no code downloads after install.

The only third-party Go dependency is `gorilla/websocket` (metrics
streaming). All platform collectors (Linux procfs/sysfs, macOS sysctl,
Windows Win32 P/Invoke) use the standard library only, so the binary
stays a small static executable.

## Install

**Linux (one-liner, root by default):**

```sh
curl -fsSL https://<host>/rackops-agent-install.sh | sudo bash -s -- [--limited] [--user]
```

**macOS (one-liner, user-level launchd):**

```sh
curl -fsSL https://<host>/rackops-agent-install.sh | bash -s -- [--limited]
```

**Windows:** the release zip ships `rackops-agent.exe`; register it as a
service with `sc.exe create rackops-agent binPath= C:\ProgramFiles\rackops\rackops-agent.exe`
(or run it directly). See `install/` for the scripted Windows service
registration.

The installer downloads the prebuilt binary (or builds from source in
this repo until the GitHub org exists — ticket 13), writes a
self-signed TLS cert, registers the service (systemd / launchd /
Windows service), and prints the registration token:

```
Registration token:
  NTQ3...
```

Paste that token into the RackOps app (Hosts → Add → Agent) to add the
Host to your Lab. The token persists on the Host across restarts; use
`rackops-agent -show-token` to display it again.

**Uninstall:**

```sh
curl -fsSL https://<host>/rackops-agent-uninstall.sh | sudo bash -s -- [--remove-data]
```

**In-place update:** re-run the installer with a newer `--version`;
the service is re-enabled in place. The old binary is replaced, the
token and TLS files are kept.

## Agent API v1

All endpoints except `/health` require the token:

```
Authorization: Bearer <token>        # or ?token=<token>
```

| Endpoint | Description |
|---|---|
| `GET /health` | Liveness. No auth (for service managers). |
| `GET /api/v1/info` | Host specs: hostname, OS/arch, CPU model, RAM, disks, board. |
| `GET /api/v1/metrics` | One live sample: CPU, memory, disks, network, temperature, uptime. |
| `GET /api/v1/metrics/stream` | WebSocket. One JSON `Snapshot` frame on upgrade, then every 5 s. |

A sample `Snapshot` (the metrics/stream frame shape):

```json
{
  "metrics": {
    "timestamp": 1760500000000,
    "cpu": { "usagePercent": 12.3 },
    "memory": { "totalBytes": 17179869184, "usedBytes": 8589934592 },
    "disks": [ { "mountPoint": "/", "totalBytes": 480000000000, "freeBytes": 240000000000, "fsType": "ext4" } ],
    "network": { "bytesInPerSecond": 0, "bytesOutPerSecond": 0 },
    "temperature": 41.5,
    "uptimeSeconds": 123456
  },
  "info": {
    "hostname": "rack-01", "os": "linux", "arch": "amd64", "uptimeSeconds": 123456,
    "hardware": {
      "cpuModel": "Intel Xeon E3-1225 v3", "numCpu": 8,
      "memoryTotalBytes": 17179869184,
      "disks": [ { "name": "sda", "model": "WDC WD10EZEX", "sizeBytes": 1000204886016, "isSsd": false } ]
    }
  },
  "features": {
    "hostinfo": { "available": true },
    "cpu": { "available": true },
    "temperature": { "available": false, "reason": "not available on this Host" }
  }
}
```

The `features` map is how the app shows "not available on this Host"
instead of a wrong zero (ADR-0001): every Collector that couldn't run
is marked unavailable with a human-readable reason.

## Modes

- **full** (default): the Agent runs as root and every Collector runs.
- **limited**: unprivileged. Privileged Collectors (temperature sensors
  on some platforms) are skipped and marked unavailable; CPU, memory,
  disks, uptime, and hardware specs still work. The Agent is
  Docker-socket-capable in this mode for container counts.

Limited mode flag: `-mode limited` (installer: `--limited`).

## Platform notes

- **Linux**: procfs + sysfs; temperature from `thermal_zone*` when
  readable; hardware specs from `/proc/cpuinfo`, `/sys/block`, and DMI
  (`/sys/class/dmi/id`).
- **Windows**: Win32 (`GetSystemTimes`, `GlobalMemoryStatusEx`,
  `GetDiskFreeSpaceExW`, `GetTickCount64`). Temperature is reported
  "not available on this Host" — there's no stdlib-accessible
  temperature API; the app degrades gracefully.
- **macOS**: `syscall.Sysctl` for boot time; disk via `statfs`. CPU,
  memory, and network rates need the C `sysctlbyname`/`getifaddrs`
  APIs which the stdlib-only build can't call, so those slices are
  marked unavailable until a cgo build lands.

## Building (from source)

```sh
cd agent
CGO_ENABLED=0 go build -trimpath -o bin/agent ./cmd/agent   # host binary
go test ./...                                                # unit + smoke tests
docker build -t rackops/agent:dev .
```

Release builds (static binaries, 6 platforms, Docker image) run in CI
once the company GitHub org exists (ticket 13).

## Security

- The registration token is 192 bits of `crypto/rand`, compared in
  constant time; only its SHA-256 is kept in memory after generation.
- TLS is enabled when a cert + key are present (the installer
  generates a self-signed pair). For LAN use, replace with a public
  cert or a Tailscale/Cloudflare termination; the app is
  transport-blind (it holds an endpoint URL, not a Transport).
- `/health` is open by design (service-manager liveness); every
  data-bearing endpoint requires the token.
