#!/usr/bin/env bash
# Generate a local self-signed cert for Grok Bridge HTTPS/WSS (dev / LAN / Tailscale).
# Prefer mkcert for phones (trusted by the OS): https://github.com/FiloSottile/mkcert
#
# Usage:
#   ./scripts/gen-dev-certs.sh
#   ./scripts/gen-dev-certs.sh 100.x.y.z
#   ./scripts/gen-dev-certs.sh 100.x.y.z myhost.tailnet.ts.net
#   ./scripts/gen-dev-certs.sh --san IP:100.1.2.3 --san DNS:myhost.tailnet.ts.net
#
# Any non-flag args are treated as IP or DNS names (auto-detected).
# Flag form: --san IP:… or --san DNS:…
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${GROK_BRIDGE_CERT_DIR:-$ROOT/certs}"
mkdir -p "$OUT"

# Always include localhost
SANS=("DNS:localhost" "IP:127.0.0.1")
EXTRA_DISPLAY=()

is_ipv4() {
  local s="$1"
  [[ "$s" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

is_ipv6() {
  local s="$1"
  [[ "$s" == *:* ]] && [[ "$s" != *[[:space:]]* ]]
}

add_san() {
  local entry="$1"
  local i
  for i in "${SANS[@]}"; do
    if [[ "$i" == "$entry" ]]; then
      return 0
    fi
  done
  SANS+=("$entry")
}

add_host_or_ip() {
  local arg="$1"
  if [[ -z "$arg" ]]; then
    return 0
  fi
  EXTRA_DISPLAY+=("$arg")
  if is_ipv4 "$arg" || is_ipv6 "$arg"; then
    add_san "IP:${arg}"
  else
    add_san "DNS:${arg}"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --san)
      shift
      if [[ $# -eq 0 ]]; then
        echo "error: --san requires IP:… or DNS:…" >&2
        exit 1
      fi
      add_san "$1"
      EXTRA_DISPLAY+=("$1")
      shift
      ;;
    --help|-h)
      echo "Usage: $0 [IP_OR_HOSTNAME ...] [--san IP:x | --san DNS:name ...]"
      exit 0
      ;;
    -*)
      echo "error: unknown option: $1" >&2
      exit 1
      ;;
    *)
      add_host_or_ip "$1"
      shift
      ;;
  esac
done

SAN="$(IFS=,; echo "${SANS[*]}")"

CONF="$(mktemp)"
trap 'rm -f "$CONF"' EXIT
cat > "$CONF" <<CFG
[req]
default_bits = 2048
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_req

[dn]
CN = grok-bridge-dev

[v3_req]
subjectAltName = ${SAN}
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
CFG

openssl req -x509 -nodes -newkey rsa:2048 \
  -keyout "$OUT/dev-key.pem" \
  -out "$OUT/dev-cert.pem" \
  -days 825 \
  -config "$CONF"

chmod 600 "$OUT/dev-key.pem"
chmod 644 "$OUT/dev-cert.pem"

echo "Wrote:"
echo "  cert: $OUT/dev-cert.pem"
echo "  key:  $OUT/dev-key.pem"
echo "  SAN:  $SAN"
echo ""
echo "Run hub with TLS:"
echo "  export GROK_BRIDGE_SSL_CERT=$OUT/dev-cert.pem"
echo "  export GROK_BRIDGE_SSL_KEY=$OUT/dev-key.pem"
echo "  ./start.sh"
echo ""
echo "Browsers will warn on self-signed certs. For phones, install mkcert and run:"
MKCERT_NAMES="localhost 127.0.0.1"
if [[ ${#EXTRA_DISPLAY[@]} -gt 0 ]]; then
  for e in "${EXTRA_DISPLAY[@]}"; do
    # Strip IP:/DNS: prefix if present for mkcert args
    e="${e#IP:}"
    e="${e#DNS:}"
    MKCERT_NAMES="${MKCERT_NAMES} ${e}"
  done
fi
echo "  mkcert -install"
echo "  mkcert -cert-file $OUT/dev-cert.pem -key-file $OUT/dev-key.pem ${MKCERT_NAMES}"
