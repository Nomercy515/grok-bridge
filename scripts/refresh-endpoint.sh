#!/usr/bin/env bash
# Boot-time / on-demand Tailscale endpoint refresh for Grok Bridge.
#
# Waits for Tailscale (MagicDNS + 100.x), writes data/endpoint.json, and
# regenerates TLS certs with those SANs so phone bookmarks survive host DHCP churn.
# Tailscale-specific only — does not discover LAN/DHCP addresses.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${GROK_BRIDGE_DATA:-$ROOT/data}"
PORT="${GROK_BRIDGE_PORT:-4020}"
REGEN=1
WAIT=1
WAIT_SECS="${GROK_BRIDGE_TS_WAIT:-120}"
PRINT_ONLY=0
TAILSCALE_BIN="${GROK_BRIDGE_TAILSCALE:-tailscale}"

usage() {
  cat <<USAGE
Usage: $0 [--no-regen-certs] [--no-wait] [--wait-secs N] [--port N] [--print-only]
  Waits for Tailscale MagicDNS/IPv4, writes \$DATA/endpoint.json, regenerates certs.
  Canonical phone URL = https://<MagicDNS>:\$PORT/ (fallback: Tailscale 100.x).
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --regen-certs) REGEN=1; shift ;;
    --no-regen-certs) REGEN=0; shift ;;
    --no-wait) WAIT=0; shift ;;
    --wait-secs) WAIT_SECS="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --print-only) PRINT_ONLY=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

mkdir -p "$DATA"

if ! command -v "$TAILSCALE_BIN" >/dev/null 2>&1 && [[ ! -x "$TAILSCALE_BIN" ]]; then
  echo "error: tailscale not found ($TAILSCALE_BIN)" >&2
  exit 1
fi

ts() { "$TAILSCALE_BIN" "$@"; }

read_ts() {
  TS_IP=""
  TS_DNS=""
  local TS_JSON
  if TS_JSON="$(ts status --json 2>/dev/null)"; then
    TS_IP="$(printf '%s' "$TS_JSON" | python3 -c '
import json,sys
d=json.load(sys.stdin)
self=d.get("Self") or {}
ips=self.get("TailscaleIPs") or []
v4=[i for i in ips if ":" not in i]
print(v4[0] if v4 else "")
' 2>/dev/null || true)"
    TS_DNS="$(printf '%s' "$TS_JSON" | python3 -c '
import json,sys
d=json.load(sys.stdin)
self=d.get("Self") or {}
print((self.get("DNSName") or "").rstrip("."))
' 2>/dev/null || true)"
  fi
  if [[ -z "$TS_IP" ]]; then
    TS_IP="$(ts ip -4 2>/dev/null || true)"
  fi
  [[ -n "$TS_IP" ]]
}

if [[ "$WAIT" -eq 1 ]]; then
  echo "Waiting up to ${WAIT_SECS}s for Tailscale (MagicDNS / 100.x)…"
  deadline=$(( SECONDS + WAIT_SECS ))
  while ! read_ts; do
    if (( SECONDS >= deadline )); then
      echo "error: Tailscale not ready after ${WAIT_SECS}s (run: sudo tailscale up)" >&2
      exit 1
    fi
    sleep 2
  done
else
  if ! read_ts; then
    echo "error: could not read Tailscale IPv4 (is 'tailscale up' done?)" >&2
    exit 1
  fi
fi

if [[ -n "$TS_DNS" ]]; then
  CANONICAL_HOST="$TS_DNS"
else
  CANONICAL_HOST="$TS_IP"
fi
URL="https://${CANONICAL_HOST}:${PORT}/"
URL_IP="https://${TS_IP}:${PORT}/"

ENDPOINT_JSON="$DATA/endpoint.json"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

python3 - "$ENDPOINT_JSON" "$TS_IP" "$TS_DNS" "$PORT" "$URL" "$URL_IP" "$NOW" <<'PY'
import json, sys, os
path, ts_ip, ts_dns, port, url, url_ip, now = sys.argv[1:]
doc = {
    "source": "tailscale",
    "tailscale_ipv4": ts_ip or None,
    "magicdns": ts_dns or None,
    "port": int(port),
    "url": url,
    "url_ip": url_ip,
    "updated_at": now,
    "note": "Canonical phone bookmark = url (MagicDNS when available). Host LAN DHCP is ignored.",
}
tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2)
    f.write("\n")
os.replace(tmp, path)
print(f"Wrote {path}")
PY

echo "=== Grok Bridge Tailscale endpoint ==="
echo "  MagicDNS       : ${TS_DNS:-"(none — enable MagicDNS in tailnet DNS settings)"}"
echo "  Tailscale IPv4 : $TS_IP"
echo "  Canonical URL  : $URL"
echo "  (bookmark MagicDNS; do not use eth0/DHCP LAN IP)"
echo ""

if [[ "$PRINT_ONLY" -eq 1 ]]; then
  exit 0
fi

if [[ "$REGEN" -eq 1 ]]; then
  ARGS=("$TS_IP")
  if [[ -n "$TS_DNS" ]]; then
    ARGS+=("$TS_DNS")
  fi
  echo "Regenerating TLS certs with SANs: localhost 127.0.0.1 ${ARGS[*]}"
  "$ROOT/scripts/gen-dev-certs.sh" "${ARGS[@]}"
fi

ENV_SNIPPET="$DATA/grok-bridge.endpoint.env"
cat > "$ENV_SNIPPET" <<ENV
GROK_BRIDGE_ENDPOINT_URL=${URL}
GROK_BRIDGE_TAILSCALE_IP=${TS_IP}
GROK_BRIDGE_MAGICDNS=${TS_DNS}
ENV
chmod 644 "$ENV_SNIPPET"
echo "Wrote $ENV_SNIPPET"
