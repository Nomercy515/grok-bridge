#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

# Local quick-start prereqs (curl, Go ≥ 1.22, git). Full Tailscale install: scripts/install.sh
# shellcheck disable=SC1091
source "$ROOT/scripts/ensure-prereqs.sh"
ensure_prereqs

# Grok Build readiness (warn-by-default; REQUIRE_GROK_BUILD=1 to hard-fail).
# shellcheck disable=SC1091
source "$ROOT/scripts/ensure-grok-build.sh"
ensure_grok_build

chmod +x \
  "$ROOT/start.sh" \
  "$ROOT/scripts/ensure-prereqs.sh" \
  "$ROOT/scripts/ensure-grok-build.sh" \
  "$ROOT/scripts/supervise.sh" \
  "$ROOT/scripts/gen-dev-certs.sh" \
  "$ROOT/scripts/refresh-endpoint.sh" \
  "$ROOT/scripts/install.sh" \
  "$ROOT/scripts/install-macos.sh" \
  2>/dev/null || true

mkdir -p bin data

need_build=0
if [[ ! -x bin/grok-bridge ]]; then
  need_build=1
else
  # Rebuild when any tracked Go source (or go.mod/go.sum) is newer than the binary.
  while IFS= read -r -d '' f; do
    if [[ "$f" -nt bin/grok-bridge ]]; then
      need_build=1
      break
    fi
  done < <(find . \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) -print0 2>/dev/null)
fi

if [[ "$need_build" -eq 1 ]]; then
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
