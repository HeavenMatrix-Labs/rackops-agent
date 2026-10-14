#!/usr/bin/env bash
# rackops-agent uninstaller (Linux + macOS).
#
# Linux:  curl -fsSL https://<host>/rackops-agent-uninstall.sh | sudo bash -s -- [--remove-data] [--user]
# macOS:  curl -fsSL https://<host>/rackops-agent-uninstall.sh | bash -s -- [--remove-data]
#
# Removes the service, the binary, and (optionally) the token + TLS files.
# The user's alert rules and history live in their Lab (the app's
# backend), not on the host, so removing the host state loses nothing
# but the local registration token.

set -euo pipefail

USER_SERVICE=""
REMOVE_DATA=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user) USER_SERVICE="1"; shift ;;
    --remove-data) REMOVE_DATA="1"; shift ;;
    *) echo "unknown flag: $1" >&2; exit 1 ;;
  esac
done

OS="$(uname -s)"
BINARY=rackops-agent
UNIT=rackops-agent.service

echo "==> Stopping and de-registering ${BINARY}"
if [[ "$OS" == "Darwin" ]]; then
  launchctl unload "${HOME}/Library/LaunchAgents/com.rackops.agent.plist" 2>/dev/null || true
  rm -f "${HOME}/Library/LaunchAgents/com.rackops.agent.plist"
  rm -f "${HOME}/.local/bin/${BINARY}"
else
  if [[ -n "$USER_SERVICE" ]]; then
    systemctl --user disable --now "${UNIT}" 2>/dev/null || true
    rm -f "${HOME}/.config/systemd/user/${UNIT}"
    systemctl --user daemon-reload
    rm -f "${HOME}/.local/bin/${BINARY}"
  else
    systemctl disable --now "${UNIT}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${UNIT}"
    systemctl daemon-reload
    rm -f "/usr/local/bin/${BINARY}"
  fi
fi

if [[ -n "$REMOVE_DATA" ]]; then
  echo "==> Removing token + TLS files"
  if [[ "$OS" == "Darwin" || -n "$USER_SERVICE" ]]; then
    rm -rf "${HOME}/.config/rackops"
  else
    rm -rf "/var/lib/rackops"
  fi
fi

echo
echo "rackops-agent uninstalled. Nothing else on this Host was changed."
echo "Your Lab, Hosts, and alert rules live in the RackOps app, not here."
