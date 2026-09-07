#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p bin data
if [[ ! -x bin/grok-bridge ]]; then
  echo "==> Building grok-bridge"
  go build -o bin/grok-bridge ./cmd/grok-bridge
fi
export GROK_BRIDGE_DATA="${GROK_BRIDGE_DATA:-$(pwd)/data}"
mkdir -p "$GROK_BRIDGE_DATA"

HOST="${GROK_BRIDGE_HOST:-127.0.0.1}"
PORT="${GROK_BRIDGE_PORT:-4020}"
SCHEME="http"
SSL_ARGS=()
if [[ -n "${GROK_BRIDGE_SSL_CERT:-}" && -n "${GROK_BRIDGE_SSL_KEY:-}" ]]; then
  SCHEME="https"
  SSL_ARGS=(--ssl-cert "$GROK_BRIDGE_SSL_CERT" --ssl-key "$GROK_BRIDGE_SSL_KEY")
fi

echo "Starting Grok Bridge on ${SCHEME}://${HOST}:${PORT}  (demo: ${SCHEME}://127.0.0.1:${PORT}/?demo=1)"
if [[ "$HOST" == "0.0.0.0" || "$HOST" == "::" ]]; then
  echo "Note: bound on all interfaces — use TLS + trusted LAN or Tailscale; do not port-forward :${PORT}."
fi
exec ./bin/grok-bridge --host "$HOST" --port "$PORT" --data-dir "$GROK_BRIDGE_DATA" "${SSL_ARGS[@]+"${SSL_ARGS[@]}"}"
