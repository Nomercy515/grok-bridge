#!/usr/bin/env bash
# Install/enable Grok Bridge systemd units without regenerating secrets or rewriting env.
# Runs the hub as the repo owner (not root) so brand/login and ~/.grok resolve correctly.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="${GROK_BRIDGE_DATA:-$ROOT/data}"
ENV_FILE_PROJECT="$DATA_DIR/grok-bridge.env"
ENV_FILE_SYSTEM="/etc/grok-bridge.env"
PORT="${GROK_BRIDGE_PORT:-4020}"
UNIT_NAME="grok-bridge.service"
ENDPOINT_UNIT_NAME="grok-bridge-endpoint.service"

if [[ ! -f "$ENV_FILE_PROJECT" ]]; then
  echo "error: missing $ENV_FILE_PROJECT — start the hub once or run full install first" >&2
  exit 1
fi
if [[ ! -x "$ROOT/scripts/supervise.sh" ]]; then
  chmod +x "$ROOT/scripts/supervise.sh" "$ROOT/scripts/refresh-endpoint.sh" 2>/dev/null || true
fi
if [[ ! -x "$ROOT/bin/grok-bridge" ]]; then
  echo "error: missing $ROOT/bin/grok-bridge — build first" >&2
  exit 1
fi

# Prefer explicit override, else repo directory owner, else invoking user.
resolve_service_user() {
  if [[ -n "${GROK_BRIDGE_SERVICE_USER:-}" ]]; then
    printf '%s\n' "$GROK_BRIDGE_SERVICE_USER"
    return
  fi
  local owner
  owner="$(stat -c '%U' "$ROOT" 2>/dev/null || true)"
  if [[ -n "$owner" && "$owner" != "root" ]]; then
    printf '%s\n' "$owner"
    return
  fi
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "$SUDO_USER"
    return
  fi
  if [[ "$(id -u)" -ne 0 ]]; then
    id -un
    return
  fi
  echo "error: set GROK_BRIDGE_SERVICE_USER to the human account (e.g. eti_enne1)" >&2
  exit 1
}

SERVICE_USER="$(resolve_service_user)"
SERVICE_GROUP="$(id -gn "$SERVICE_USER" 2>/dev/null || printf '%s\n' "$SERVICE_USER")"
SERVICE_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
if [[ -z "$SERVICE_HOME" ]]; then
  SERVICE_HOME="/home/$SERVICE_USER"
fi
GROK_HOME_DIR="${GROK_BRIDGE_GROK_HOME:-$SERVICE_HOME/.grok}"

# Ensure brand + Grok home without wiping secrets.
ensure_env_kv() {
  local file="$1" key="$2" val="$3"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    # Keep existing value (do not clobber operator overrides).
    return 0
  fi
  printf '%s=%s\n' "$key" "$val" >> "$file"
}

ensure_env_kv "$ENV_FILE_PROJECT" "GROK_BRIDGE_GROK_HOME" "$GROK_HOME_DIR"
# Display name for "X's sessions list" (GECOS is often empty on WSL).
if ! grep -q "^GROK_BRIDGE_USER_NAME=" "$ENV_FILE_PROJECT" 2>/dev/null; then
  # Prefer first GECOS field, else login.
  gecos=""
  display=""
  gecos="$(getent passwd "$SERVICE_USER" | cut -d: -f5 | cut -d, -f1 | xargs 2>/dev/null || true)"
  display="${gecos:-$SERVICE_USER}"
  # Nice default for this host when GECOS empty.
  if [[ "$SERVICE_USER" == "eti_enne1" && -z "$gecos" ]]; then
    display="Étienne"
  fi
  ensure_env_kv "$ENV_FILE_PROJECT" "GROK_BRIDGE_USER_NAME" "$display"
fi
chmod 600 "$ENV_FILE_PROJECT" 2>/dev/null || true

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  command -v sudo >/dev/null || { echo "error: need root or sudo" >&2; exit 1; }
  SUDO=sudo
fi
run_priv() { if [[ -n "$SUDO" ]]; then $SUDO "$@"; else "$@"; fi; }

echo "==> Service user: $SERVICE_USER (home=$SERVICE_HOME grok=$GROK_HOME_DIR)"
echo "==> Installing system env from existing project env (no secret rewrite)"
run_priv cp "$ENV_FILE_PROJECT" "$ENV_FILE_SYSTEM"
run_priv chmod 600 "$ENV_FILE_SYSTEM"

# Certs / endpoint files may have been created as root; make writable by service user.
if [[ -d "$ROOT/certs" ]]; then
  run_priv chown -R "$SERVICE_USER:$SERVICE_GROUP" "$ROOT/certs" || true
fi
run_priv chown "$SERVICE_USER:$SERVICE_GROUP" \
  "$DATA_DIR/endpoint.json" "$DATA_DIR/grok-bridge.endpoint.env" 2>/dev/null || true

install_unit() {
  local template="$1" dest="$2" tmp
  tmp="$(mktemp)"
  sed -e "s|__ROOT__|${ROOT}|g" \
      -e "s|__ENV_FILE__|${ENV_FILE_SYSTEM}|g" \
      -e "s|__DATA__|${DATA_DIR}|g" \
      -e "s|__PORT__|${PORT}|g" \
      -e "s|__USER__|${SERVICE_USER}|g" \
      -e "s|__GROUP__|${SERVICE_GROUP}|g" \
      -e "s|__HOME__|${SERVICE_HOME}|g" \
      -e "s|__GROK_HOME__|${GROK_HOME_DIR}|g" \
      "$template" > "$tmp"
  run_priv cp "$tmp" "/etc/systemd/system/${dest}"
  rm -f "$tmp"
}

echo "==> Installing $ENDPOINT_UNIT_NAME and $UNIT_NAME"
install_unit "$ROOT/scripts/install-grok-bridge-endpoint.service" "$ENDPOINT_UNIT_NAME"
install_unit "$ROOT/scripts/install-grok-bridge.service" "$UNIT_NAME"
run_priv systemctl daemon-reload
run_priv systemctl enable "$ENDPOINT_UNIT_NAME" "$UNIT_NAME"
run_priv systemctl start "$ENDPOINT_UNIT_NAME" || echo "warning: endpoint start failed (check Tailscale)"
run_priv systemctl restart "$UNIT_NAME" || run_priv systemctl start "$UNIT_NAME"
echo "==> Status"
run_priv systemctl --no-pager --full status "$ENDPOINT_UNIT_NAME" "$UNIT_NAME" || true
echo "Done. Units enabled for multi-user.target (WSL distro start)."
echo "Brand user: $(grep '^GROK_BRIDGE_USER_NAME=' "$ENV_FILE_PROJECT" | cut -d= -f2- || true)"
