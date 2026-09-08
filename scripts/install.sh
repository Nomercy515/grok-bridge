#!/usr/bin/env bash
# Install Grok Bridge on a Linux VM/host with Tailscale for DHCP-resilient phone access.
#
# Tailscale-specific resilience:
#   - tailscaled enabled at boot
#   - grok-bridge-endpoint.service waits for MagicDNS/100.x, writes data/endpoint.json,
#     regenerates TLS SANs — so after every reboot phone bookmarks keep working
#   - grok-bridge.service runs supervise.sh after endpoint refresh
#
# Secrets (bridge secret, pairing material) are generated at install time into the
# local data dir (gitignored) — never committed to the repo.
#
# Usage:
#   ./scripts/install.sh
#   TAILSCALE_AUTHKEY=tskey-auth-… ./scripts/install.sh
#   SKIP_SYSTEMD=1 ./scripts/install.sh
#   SKIP_TAILSCALE=1 ./scripts/install.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${GROK_BRIDGE_PORT:-4020}"
DATA_DIR="${GROK_BRIDGE_DATA:-$ROOT/data}"
ENV_FILE_PROJECT="$DATA_DIR/grok-bridge.env"
ENV_FILE_SYSTEM="/etc/grok-bridge.env"
UNIT_NAME="grok-bridge.service"
ENDPOINT_UNIT_NAME="grok-bridge-endpoint.service"
UNIT_TEMPLATE="$ROOT/scripts/install-grok-bridge.service"
ENDPOINT_TEMPLATE="$ROOT/scripts/install-grok-bridge-endpoint.service"
SKIP_SYSTEMD="${SKIP_SYSTEMD:-0}"
SKIP_TAILSCALE="${SKIP_TAILSCALE:-0}"

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    echo "warning: not root and sudo not found — systemd/Tailscale system steps may fail"
  fi
fi

run_priv() {
  if [[ -n "$SUDO" ]]; then
    $SUDO "$@"
  else
    "$@"
  fi
}

info() { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }

# --- 1. Go toolchain + Grok Build readiness ---
info "Checking Go toolchain"
if ! command -v go >/dev/null 2>&1; then
  echo "error: go not found. Install Go ≥ 1.22 from https://go.dev/dl/ then re-run." >&2
  exit 1
fi
GO_VER="$(go env GOVERSION 2>/dev/null || go version)"
info "Found $GO_VER"

# shellcheck disable=SC1091
source "$ROOT/scripts/ensure-grok-build.sh"
# Warn-only on purpose: do not default REQUIRE_GROK_BUILD=1 here.
# Existing Linux VM installs must keep succeeding when grok is not installed yet.
# macOS scripts/install-macos.sh and Windows scripts/install.ps1 default require on
# (source-host setup). Pass REQUIRE_GROK_BUILD=1 to fail this script hard.
ensure_grok_build

info "Building grok-bridge binary"
mkdir -p bin
go build -o bin/grok-bridge ./cmd/grok-bridge

# --- 2. chmod scripts ---
info "Making scripts executable"
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

# --- 3. Tailscale ---
detect_os_family() {
  if [[ -f /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    echo "${ID:-unknown}"
  else
    echo "unknown"
  fi
}

install_tailscale() {
  if command -v tailscale >/dev/null 2>&1 && command -v tailscaled >/dev/null 2>&1; then
    info "Tailscale already installed"
    return 0
  fi
  local os
  os="$(detect_os_family)"
  case "$os" in
    debian|ubuntu|raspbian|linuxmint|pop)
      info "Installing Tailscale via official install script (debian/ubuntu family: $os)"
      warn "curl|bash install — verify TLS; air-gapped: SKIP_TAILSCALE=1 + package install"
      curl -fsSL https://tailscale.com/install.sh | run_priv bash
      ;;
    *)
      warn "OS '$os' is not debian/ubuntu — install Tailscale manually:"
      echo "  https://tailscale.com/download/linux"
      echo "  Then re-run: ./scripts/install.sh"
      echo "  Or: SKIP_TAILSCALE=1 ./scripts/install.sh after installing packages yourself"
      if ! command -v tailscale >/dev/null 2>&1; then
        return 1
      fi
      ;;
  esac
}

bring_up_tailscale() {
  info "Enabling tailscaled at boot (systemctl enable --now tailscaled)"
  if command -v systemctl >/dev/null 2>&1; then
    run_priv systemctl enable --now tailscaled || warn "could not enable/start tailscaled"
  else
    warn "systemctl not found — start tailscaled yourself"
  fi

  if tailscale status >/dev/null 2>&1; then
    info "Tailscale already connected"
    return 0
  fi

  if [[ -n "${TAILSCALE_AUTHKEY:-}" ]]; then
    info "Bringing Tailscale up with TAILSCALE_AUTHKEY (non-interactive)"
    run_priv tailscale up --authkey="$TAILSCALE_AUTHKEY" --ssh=false || {
      echo "error: tailscale up --authkey failed" >&2
      exit 1
    }
  else
    echo ""
    echo "-------------------------------------------------------------------"
    echo " NEXT STEP (interactive — cannot complete OAuth unattended):"
    echo "   sudo tailscale up"
    echo " Open the printed URL, authenticate, then re-run this install or"
    echo " continue once 'tailscale status' succeeds."
    echo ""
    echo " Non-interactive alternative:"
    echo "   TAILSCALE_AUTHKEY=tskey-auth-… ./scripts/install.sh"
    echo "-------------------------------------------------------------------"
    echo ""
    if [[ -t 0 ]]; then
      info "Running: sudo tailscale up (follow the auth URL)"
      run_priv tailscale up --ssh=false || warn "tailscale up did not finish — complete it and re-run install"
    fi
  fi
}

if [[ "$SKIP_TAILSCALE" != "1" ]]; then
  info "Installing / configuring Tailscale (MagicDNS phone URL — not LAN DHCP)"
  if install_tailscale; then
    bring_up_tailscale
  else
    warn "Tailscale install skipped/failed — phone access will not survive DHCP IP changes"
  fi
else
  warn "SKIP_TAILSCALE=1 — skipping Tailscale"
fi

# --- 4. Data dir + generate secrets (never committed) ---
mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR" 2>/dev/null || true
export GROK_BRIDGE_DATA="$DATA_DIR"
export GROK_BRIDGE_PORT="$PORT"

# Bridge secret: generate once into data dir if missing
SECRET_FILE="$DATA_DIR/bridge.secret"
if [[ ! -f "$SECRET_FILE" ]]; then
  info "Generating GROK_BRIDGE_SECRET into data dir (gitignored)"
  openssl rand -hex 24 > "$SECRET_FILE"
  chmod 600 "$SECRET_FILE"
fi
BRIDGE_SECRET="$(tr -d '\n' < "$SECRET_FILE")"

# Pairing material is created by the hub on first start into data/auth.json (0600).
# Optional: pre-touch so install can note the path.
info "Pairing code will be generated on first hub start (see journal / data/auth.json)"

if [[ "$SKIP_TAILSCALE" != "1" ]] && command -v tailscale >/dev/null 2>&1; then
  info "Refreshing Tailscale endpoint (endpoint.json + TLS certs)"
  if ! "$ROOT/scripts/refresh-endpoint.sh" --wait-secs 60; then
    warn "refresh-endpoint failed — generate localhost certs; re-run after 'sudo tailscale up'"
    "$ROOT/scripts/gen-dev-certs.sh"
  fi
else
  info "Generating localhost TLS certs (Tailscale SANs added by refresh-endpoint later)"
  "$ROOT/scripts/gen-dev-certs.sh"
fi

CERT_PATH="$ROOT/certs/dev-cert.pem"
KEY_PATH="$ROOT/certs/dev-key.pem"

detect_bind_host() {
  if [[ -n "${GROK_BRIDGE_HOST:-}" ]]; then
    echo "$GROK_BRIDGE_HOST"
    return
  fi
  if command -v tailscale >/dev/null 2>&1; then
    local tip
    tip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
    if [[ -n "$tip" && "$tip" == 100.* ]]; then
      echo "$tip"
      return
    fi
  fi
  echo "127.0.0.1"
}
BIND_HOST="$(detect_bind_host)"

# --- 5. Env file (secrets generated on install — not sample values) ---
info "Writing project env file: $ENV_FILE_PROJECT"
cat > "$ENV_FILE_PROJECT" <<ENV
# Generated by scripts/install.sh — secrets generated on install; do not commit.
# Canonical phone URL lives in data/endpoint.json (MagicDNS) — refreshed at boot.
GROK_BRIDGE_HOST=${BIND_HOST}
GROK_BRIDGE_PORT=${PORT}
GROK_BRIDGE_DATA=${DATA_DIR}
GROK_BRIDGE_SSL_CERT=${CERT_PATH}
GROK_BRIDGE_SSL_KEY=${KEY_PATH}
GROK_BRIDGE_SECRET=${BRIDGE_SECRET}
GROK_BRIDGE_AGENT=${GROK_BRIDGE_AGENT:-bot}
GROK_BRIDGE_AGENT_MODE=${GROK_BRIDGE_AGENT_MODE:-${GROK_BRIDGE_VALENTINE_MODE:-mock}}
GROK_BRIDGE_VALENTINE_MODE=${GROK_BRIDGE_AGENT_MODE}
ENV
chmod 600 "$ENV_FILE_PROJECT"

ENV_FILE_FOR_UNIT="$ENV_FILE_PROJECT"
if [[ "$SKIP_SYSTEMD" != "1" ]] && command -v systemctl >/dev/null 2>&1; then
  info "Installing system env file: $ENV_FILE_SYSTEM"
  run_priv cp "$ENV_FILE_PROJECT" "$ENV_FILE_SYSTEM"
  run_priv chmod 600 "$ENV_FILE_SYSTEM" || true
  ENV_FILE_FOR_UNIT="$ENV_FILE_SYSTEM"
fi

# --- 6–7. systemd units ---
install_unit_from_template() {
  local template="$1" dest_name="$2"
  local tmp
  tmp="$(mktemp)"
  sed \
    -e "s|__ROOT__|${ROOT}|g" \
    -e "s|__ENV_FILE__|${ENV_FILE_FOR_UNIT}|g" \
    -e "s|__DATA__|${DATA_DIR}|g" \
    -e "s|__PORT__|${PORT}|g" \
    "$template" > "$tmp"
  run_priv cp "$tmp" "/etc/systemd/system/${dest_name}"
  rm -f "$tmp"
}

if [[ "$SKIP_SYSTEMD" == "1" ]]; then
  info "SKIP_SYSTEMD=1 — not installing systemd units"
elif ! command -v systemctl >/dev/null 2>&1; then
  warn "systemctl not found — start manually after Tailscale:"
  echo "  ./scripts/refresh-endpoint.sh && set -a; source $ENV_FILE_PROJECT; set +a; ./scripts/supervise.sh"
else
  info "Installing systemd units: $ENDPOINT_UNIT_NAME then $UNIT_NAME"
  install_unit_from_template "$ENDPOINT_TEMPLATE" "$ENDPOINT_UNIT_NAME"
  install_unit_from_template "$UNIT_TEMPLATE" "$UNIT_NAME"
  run_priv systemctl daemon-reload
  info "Enabling endpoint refresh + hub at boot"
  run_priv systemctl enable "$ENDPOINT_UNIT_NAME" || warn "enable $ENDPOINT_UNIT_NAME failed"
  run_priv systemctl enable "$UNIT_NAME" || warn "enable $UNIT_NAME failed"
  if [[ "$SKIP_TAILSCALE" != "1" ]]; then
    run_priv systemctl start "$ENDPOINT_UNIT_NAME" || warn "start $ENDPOINT_UNIT_NAME failed (complete tailscale up, then: sudo systemctl start $ENDPOINT_UNIT_NAME)"
  fi
  if ! run_priv systemctl start "$UNIT_NAME"; then
    warn "systemctl start $UNIT_NAME failed — check: journalctl -u $UNIT_NAME -e"
  fi
fi

CANON=""
if [[ -f "$DATA_DIR/endpoint.json" ]]; then
  CANON="$(python3 -c "import json; print(json.load(open('$DATA_DIR/endpoint.json')).get('url') or '')" 2>/dev/null || true)"
fi

echo ""
echo "==================================================================="
echo " Grok Bridge install complete (Tailscale-resilient)"
echo "==================================================================="
echo " After every VM reboot:"
echo "   1) tailscaled starts"
echo "   2) grok-bridge-endpoint waits for MagicDNS/100.x,"
echo "      writes data/endpoint.json, regenerates TLS SANs"
echo "   3) grok-bridge supervise starts the hub"
echo ""
echo " Bookmark the MagicDNS URL (not eth0/DHCP):"
if [[ -n "$CANON" ]]; then
  echo "   $CANON"
else
  echo "   (pending Tailscale) sudo tailscale up"
  echo "   then: sudo systemctl start grok-bridge-endpoint"
  echo "   or:  ./scripts/refresh-endpoint.sh"
fi
echo ""
echo " Bind host: ${BIND_HOST}"
if [[ "$BIND_HOST" == "0.0.0.0" || "$BIND_HOST" == "::" ]]; then
  echo " WARNING: all-interfaces bind (explicit GROK_BRIDGE_HOST=${BIND_HOST})."
  echo " Prefer Tailscale IP or 127.0.0.1; never port-forward :${PORT}."
elif [[ "$BIND_HOST" == 100.* ]]; then
  echo " Bound to Tailscale IP (not 0.0.0.0). Override with GROK_BRIDGE_HOST if needed."
else
  echo " Bound to localhost-safe default. For Tailscale reachability set"
  echo " GROK_BRIDGE_HOST to your 100.x address, or explicitly 0.0.0.0 (opt-in)."
fi
echo " Secrets: generated on install into ${DATA_DIR}/ (gitignored)."
echo " Pairing code: journalctl -u grok-bridge -e"
echo " Hub exposes GET /api/endpoint and endpoint{} on /api/auth/status"
echo " Never port-forward :${PORT} to the public internet."
echo " Tailscale install uses curl|bash from tailscale.com — prefer packages /"
echo " SKIP_TAILSCALE=1 for air-gapped hosts (see https://tailscale.com/download/linux)."
echo "==================================================================="
