# rackops-agent

The RackOps Agent for a Host. Apache-2.0.

The Agent runs on a Host, collects its metrics and hardware specs, and
serves the Agent API v1 (Host info, on-demand metrics, WebSocket metrics
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

**Linux (systemd, root by default):**

```sh
git clone https://github.com/HeavenMatrix-Labs/rack_ops && cd rack_ops
sudo ./agent/install.sh            # [--limited] [--user] [--version vX.Y.Z]
```

**macOS (launchd, user-level):**

```sh
./agent/install.sh --limited       # optional; installs a LaunchAgent
```

**Windows:** from an elevated prompt in a directory containing
`rackops-agent.exe`:

```
agent\install\windows-service.bat
```

All three write a self-signed TLS pair (Linux/macOS via `openssl`,
Windows via `openssl` if present), register the service, and print the
registration token. Binary releases are published from
`HeavenMatrix-Labs/rackops-agent`; before that repo has a release the
installer builds from the `agent/` source tree, and
`RACKOPS_AGENT_RELEASE_BASE` overrides the download location.

Paste that token into the RackOps app (Hosts → Add → Agent) to add the
Host to your Lab. The token persists on the Host across restarts; read
it again with `rackops-agent -show-token -token <path>` (the installer
prints the path), or let the app fetch nothing — it only ever holds the
value you pasted.

**Uninstall:**

```sh
sudo ./agent/uninstall.sh [--remove-data]
```

**In-place update:** re-run the installer. It stops the service before
replacing the binary (installing over a running image fails), and keeps
the existing token and TLS files, so the Host stays reachable under the
same token the app already holds.

## Agent API v1

All endpoints except `/health` require the token:

```
Authorization: Bearer <token>
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
  disks, uptime, and hardware specs still work.

Limited mode flag: `-mode limited` (installer: `--limited`).

Container management is **not** part of the Agent. Per ADR-0001, Docker
is an app-side Connector ("no Host-side changes"), so container counts
and control come from the Docker Engine API, not from this process.

## Platform notes

Every Collector reports *availability*, not just a value: a slice the
platform cannot read is marked unavailable with a reason, so the app
renders "not available on this Host" instead of a wrong zero (ADR-0001).

- **Linux**: procfs + sysfs. CPU, memory, disks, network rates, uptime,
  hardware specs and thermal_zone temperatures all work.
- **Windows**: Win32 (`GetSystemTimes`, `GlobalMemoryStatusEx`,
  `GetDiskFreeSpaceExW`, `GetTickCount64`). Temperature is reported
  unavailable — there is no stdlib-accessible temperature API. Network
  rates need the IP Helper API (`iphlpapi.dll`), which this build does
  not link, so that slice is also unavailable.
- **macOS**: **partial.** Host info, disk capacity (`statfs`) and the
  logical CPU count work. CPU usage, memory, uptime and network rates
  are unavailable: they need `sysctlbyname(3)` / `getifaddrs`, which Go's
  `syscall` package does not expose without cgo. Temperature needs the
  SMC framework (root). Bringing macOS to parity needs either a cgo
  build or a small `//go:linkname`/assembly shim — tracked as follow-up
  work, not silently papered over.

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

- The registration token is 192 bits of `crypto/rand`. Verification
  compares SHA-256 digests in constant time, so the raw token is never
  touched on the request path.
- The raw token is kept in memory only to serve `rackops-agent
  -show-token` and the install flow; it is never written to the Agent's
  logs, and the Agent API never returns it. Fetch it from a shell on the
  Host itself.
- Tokens are read from the `Authorization: Bearer` header only. There is
  deliberately no `?token=` query parameter: a token in the request line
  leaks into proxy logs, browser history and TLS-terminating middleware.
- TLS is enabled when a cert + key are present (the installer generates
  a self-signed pair). For LAN use, replace with a public cert or a
  Tailscale/Cloudflare termination; the app is transport-blind (it holds
  an endpoint URL, not a Transport).
- `/health` is open by design (service-manager liveness); every
  data-bearing endpoint requires the token.
