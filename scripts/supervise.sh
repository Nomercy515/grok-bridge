#!/usr/bin/env bash
# Supervise Grok Bridge hub: restart on crash and honor API restart flags.
# LAN / Tailscale only — do not expose :4020 to the public internet.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${GROK_BRIDGE_PORT:-4020}"
DATA="${GROK_BRIDGE_DATA:-$ROOT/data}"
export GROK_BRIDGE_DATA="$DATA"
mkdir -p "$DATA" bin

# Load project env when present (brand / Grok home / secrets). Systemd
# EnvironmentFile may already set these; we only fill blanks via set -a source
# then re-assert DATA so endpoint snippets cannot clobber the data dir.
if [[ -f "$DATA/grok-bridge.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$DATA/grok-bridge.env"
  set +a
fi
export GROK_BRIDGE_DATA="$DATA"

# Bind to Tailscale 100.x whenever available (stable node IP). Env/localhost are fallbacks only.
resolve_host() {
  local tip=""
  if [[ -f "$DATA/grok-bridge.endpoint.env" ]]; then
    tip="$(awk -F= '/^GROK_BRIDGE_TAILSCALE_IP=/{print $2; exit}' "$DATA/grok-bridge.endpoint.env")"
  fi
  if [[ -z "$tip" ]] && command -v tailscale >/dev/null 2>&1; then
    tip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
  fi
  if [[ -n "$tip" && "$tip" == 100.* ]]; then
    printf '%s\n' "$tip"
    return
  fi
  if [[ -n "${GROK_BRIDGE_HOST:-}" ]]; then
    printf '%s\n' "$GROK_BRIDGE_HOST"
    return
  fi
  printf '%s\n' "127.0.0.1"
}
HOST="$(resolve_host)"
export GROK_BRIDGE_HOST="$HOST"

SCHEME="http"
SSL_ARGS=()
CURL_INSECURE=()
if [[ -n "${GROK_BRIDGE_SSL_CERT:-}" && -n "${GROK_BRIDGE_SSL_KEY:-}" ]]; then
  SCHEME="https"
  SSL_ARGS=(--ssl-cert "$GROK_BRIDGE_SSL_CERT" --ssl-key "$GROK_BRIDGE_SSL_KEY")
  CURL_INSECURE=(-k)
fi

if [[ ! -x "$ROOT/bin/grok-bridge" ]]; then
  echo "Building grok-bridge…"
  (cd "$ROOT" && go build -o bin/grok-bridge ./cmd/grok-bridge)
fi

FLAG="$DATA/restart.requested"
PID=""
backoff=1

cleanup() {
  if [[ -n "${PID:-}" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

health_ok() {
  curl -fsS --max-time 2 "${CURL_INSECURE[@]+"${CURL_INSECURE[@]}"}" \
    "${SCHEME}://127.0.0.1:${PORT}/health" >/dev/null 2>&1
}

start_hub() {
  rm -f "$FLAG"
  "$ROOT/bin/grok-bridge" --host "$HOST" --port "$PORT" --data-dir "$DATA" \
    "${SSL_ARGS[@]+"${SSL_ARGS[@]}"}" &
  PID=$!
  for _ in $(seq 1 30); do
    if health_ok; then
      backoff=1
      return 0
    fi
    if ! kill -0 "$PID" 2>/dev/null; then
      wait "$PID" || true
      PID=""
      return 1
    fi
    sleep 0.2
  done
  return 0
}

echo "Grok Bridge supervisor — ${SCHEME}://${HOST}:${PORT} (data: $DATA)"
echo "Phone: Tailscale (preferred) or trusted LAN with TLS — never port-forward :${PORT}."
echo "Ctrl+C stops supervisor and hub."

while true; do
  if [[ -z "${PID:-}" ]] || ! kill -0 "$PID" 2>/dev/null; then
    echo "$(date -Is) starting hub…"
    if ! start_hub; then
      echo "$(date -Is) hub failed to start; retry in ${backoff}s"
      sleep "$backoff"
      backoff=$(( backoff < 30 ? backoff * 2 : 30 ))
      continue
    fi
    echo "$(date -Is) hub pid=$PID healthy"
  fi

  if [[ -f "$FLAG" ]]; then
    echo "$(date -Is) restart flag seen — stopping hub for re-exec"
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    PID=""
    rm -f "$FLAG"
    sleep 0.5
    continue
  fi

  if ! kill -0 "$PID" 2>/dev/null; then
    wait "$PID" 2>/dev/null || true
    echo "$(date -Is) hub exited — restarting"
    PID=""
    sleep 0.5
    continue
  fi

  sleep 1
done
