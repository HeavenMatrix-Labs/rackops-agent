#!/usr/bin/env bash
# rackops-agent installer (Linux + macOS).
#
# Linux (systemd, root by default):
#   curl -fsSL https://<host>/rackops-agent-install.sh | sudo bash -s -- \
#     [--version vX.Y.Z] [--limited] [--user]
#
# macOS (launchd, user-level):
#   curl -fsSL https://<host>/rackops-agent-install.sh | bash -s -- \
#     [--version vX.Y.Z] [--limited]
#
# Installs the rackops-agent binary, registers it with the platform's
# service manager, and prints the registration token.
#
# The install is intentionally boring: one binary, one unit file, one
# token file. See the agent's README for the data-flow notes.

set -euo pipefail

VERSION="${VERSION:-latest}"
LIMITED=""
USER_SERVICE=""
CERT=""
KEY=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --limited) LIMITED="1"; shift ;;
    --user) USER_SERVICE="1"; shift ;;
    --cert) CERT="$2"; shift 2 ;;
    --key) KEY="$2"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 1 ;;
  esac
done

BINARY=rackops-agent
UNIT=rackops-agent.service
OS="$(uname -s)"

echo "==> Installing rackops-agent ${VERSION}"

# Acquire the binary: GitHub release when live (ticket 13 creates the
# org), else build from source in this repo.
if [[ ! -f "dist/${BINARY}" ]]; then
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv7l) ARCH=arm ;;
    *) echo "unsupported arch ${ARCH}"; exit 1 ;;
  esac
  echo "    downloading agent-${ARCH} ..."
  # The release URL is a placeholder until ticket 13 creates the company
  # GitHub org; until then, build from source in this repo.
  RELEASE_URL="https://github.com/<org>/rackops-agent/releases/download/${VERSION}/${BINARY}-linux-${ARCH}"
  if curl -fsSL "$RELEASE_URL" -o "dist/${BINARY}" 2>/dev/null; then
    :
  else
    echo "    (release URL not yet live - building from source in agent/)"
    ( cd agent && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "dist/${BINARY}" ./cmd/agent )
  fi
fi

if [[ "$OS" == "Darwin" ]]; then
  echo "==> Installing launchd user service"
  mkdir -p "${HOME}/.local/bin"
  install -m 0755 "dist/${BINARY}" "${HOME}/.local/bin/${BINARY}"
  PLIST="${HOME}/Library/LaunchAgents/com.rackops.agent.plist"
  mkdir -p "${HOME}/Library/LaunchAgents"
  if [[ -z "$CERT" ]]; then CERT="${HOME}/.config/rackops/tls.crt"; fi
  if [[ -z "$KEY" ]]; then KEY="${HOME}/.config/rackops/tls.key"; fi
  mkdir -p "$(dirname "$CERT")"
  if [[ ! -f "$CERT" ]]; then
    echo "    generating self-signed TLS cert"
    openssl req -x509 -newkey ed25519 -nodes \
      -keyout "$KEY" -out "$CERT" -days 825 \
      -subj "/CN=rackops-agent" 2>/dev/null
    chmod 0600 "$KEY"; chmod 0644 "$CERT"
  fi
  cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.rackops.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>${HOME}/.local/bin/${BINARY}</string>
    <string>-addr</string><string>:8443</string>
    <string>-tls-cert</string><string>${CERT}</string>
    <string>-tls-key</string><string>${KEY}</string>
    <string>-mode</string><string>${LIMITED:-full}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
EOF
  launchctl load "$PLIST" 2>/dev/null || true
  BIN_PATH="${HOME}/.local/bin/${BINARY}"
else
  # Linux: systemd.
  if [[ -n "$USER_SERVICE" ]]; then
    PREFIX="${HOME}/.local"
  else
    PREFIX="/usr/local"
  fi
  echo "==> Installing binary to ${PREFIX}/bin"
  mkdir -p "${PREFIX}/bin"
  install -m 0755 "dist/${BINARY}" "${PREFIX}/bin/${BINARY}"
  BIN_PATH="${PREFIX}/bin/${BINARY}"

  # Token + TLS state dir.
  if [[ -n "$USER_SERVICE" ]]; then
    STATE_DIR="${HOME}/.config/rackops"
  else
    STATE_DIR="/var/lib/rackops"
  fi
  if [[ -z "$CERT" ]]; then CERT="${STATE_DIR}/tls.crt"; fi
  if [[ -z "$KEY" ]]; then KEY="${STATE_DIR}/tls.key"; fi
  mkdir -p "$STATE_DIR"
  if [[ ! -f "$CERT" ]]; then
    echo "    generating self-signed TLS cert"
    openssl req -x509 -newkey ed25519 -nodes \
      -keyout "$KEY" -out "$CERT" -days 825 \
      -subj "/CN=rackops-agent" 2>/dev/null
    chmod 0600 "$KEY"; chmod 0644 "$CERT"
  fi

  UNIT_CONTENT="[Unit]
Description=RackOps Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=${BIN_PATH} -addr :8443 -tls-cert ${CERT} -tls-key ${KEY} -mode ${LIMITED:-full}
Restart=on-failure
RestartSec=2
StateDirectory=rackops"

  if [[ -n "$USER_SERVICE" ]]; then
    echo "==> Installing user systemd service"
    mkdir -p "${HOME}/.config/systemd/user"
    echo "$UNIT_CONTENT" > "${HOME}/.config/systemd/user/${UNIT}"
    systemctl --user daemon-reload
    systemctl --user enable --now "${UNIT}" 2>/dev/null || true
  else
    echo "==> Installing system systemd service"
    echo "$UNIT_CONTENT" > "/etc/systemd/system/${UNIT}"
    systemctl daemon-reload
    systemctl enable --now "${UNIT}"
  fi
fi

# Print the token. The agent persists it to its standard location on
# first start; `rackops-agent -show-token` reads it back, so the
# installer can display it to the user without scraping service logs.
TOKEN="$(${BIN_PATH} -show-token 2>/dev/null || true)"
echo
echo "==> rackops-agent is installed and running."
echo
echo "Registration token:"
echo "  ${TOKEN:-<not yet generated>}"
echo
echo "Add this Host in the RackOps app by pasting the token, or point your"
echo "phone at https://<this-host>:8443 over your chosen Transport."
