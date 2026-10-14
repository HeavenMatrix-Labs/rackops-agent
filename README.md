# rackops-agent

Per-host Agent for the RackOps homelab management app. Apache-2.0.

The Agent collects host metrics and exposes a local API so the RackOps app can
monitor and manage this Host. v0.1 is a skeleton: version command + health
endpoint. The full Agent API (metrics stream, actions, alert rules) lands in
ticket 02.

## Data flow (what leaves this machine)

- **Out:** the Agent reports alert events (ticket 11) to the user's RackOps
  backend and streams metrics only to the user's own phone/app over the
  user-chosen Transport (LAN, Tailscale, Cloudflare, VPN). Nothing is sent to
  a public service.
- **In:** the user's own registration token and alert rules.
- No analytics, no telemetry, no code downloads after install.

## Building

```sh
go build -o bin/agent ./cmd/agent
go test ./...
docker build -t rackops/agent:dev .
```

Release builds (static binaries, 6 platforms, Docker image) run in CI once the
company GitHub org exists (ticket 13).
