#!/usr/bin/env bash
# Install Grok Bridge as a systemd USER service for the invoking user.
#   scripts/install-service.sh            # install / update + start
#   scripts/install-service.sh --no-linger
# Run as your normal account (not root, not sudo). The only privileged step is
# `sudo loginctl enable-linger $USER`, which lets your user manager (and so the
# Bridge) start at boot without an interactive login.
set -euo pipefail

if [[ "$(id -u)" -eq 0 ]]; then
  echo "error: run as your normal user, not root/sudo — the Bridge must run as the account that owns ~/.grok" >&2
  exit 1
fi

LINGER=1
for a in "$@"; do
  case "$a" in
    --no-linger) LINGER=0 ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$ROOT/deploy/grok-bridge.user.service"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
DEST="$UNIT_DIR/grok-bridge.service"
STAMP="$(date +%Y%m%d-%H%M%S)"

[[ -f "$TEMPLATE" ]] || { echo "error: missing $TEMPLATE" >&2; exit 1; }
case "$ROOT" in *'|'*|*' '*) echo "error: repo path must not contain '|' or spaces: $ROOT" >&2; exit 1 ;; esac

# Old root system units would fight over :4020 and re-create root-owned files.
for u in grok-bridge.service grok-bridge-endpoint.service; do
  if systemctl is-enabled "$u" >/dev/null 2>&1 || systemctl is-active "$u" >/dev/null 2>&1; then
    if [[ "$(systemctl show -p FragmentPath --value "$u" 2>/dev/null)" == /etc/* ]]; then
      echo "error: the old system unit $u is still enabled/active." >&2
      echo "  Migrate first (see docs/ops.md, 'Migrating from the root system service'):" >&2
      echo "    sudo cp /etc/systemd/system/$u /etc/systemd/system/$u.bak-$STAMP" >&2
      echo "    sudo systemctl disable --now $u" >&2
      exit 1
    fi
  fi
done

# Files a root service left in the clone must belong to us again.
if [[ -n "$(find "$ROOT/data" "$ROOT/certs" "$ROOT/bin" -maxdepth 2 ! -user "$(id -u)" 2>/dev/null | head -n1)" ]]; then
  echo "error: some files under data/, certs/ or bin/ are not owned by $USER:" >&2
  find "$ROOT/data" "$ROOT/certs" "$ROOT/bin" -maxdepth 2 ! -user "$(id -u)" >&2
  echo "  Fix once with: sudo chown -R $USER: \"$ROOT/data\" \"$ROOT/certs\" \"$ROOT/bin\"" >&2
  exit 1
fi

# User manager: linger starts user@UID at boot and gives us a runtime dir.
if [[ "$LINGER" == 1 ]]; then
  if [[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || true)" != "yes" ]]; then
    echo "==> Enabling linger for $USER (sudo needed once: lets the Bridge start at boot without a login)"
    sudo loginctl enable-linger "$USER"
  fi
fi
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
for _ in $(seq 1 20); do [[ -S "$XDG_RUNTIME_DIR/bus" ]] && break; sleep 0.5; done
if ! systemctl --user show-environment >/dev/null 2>&1; then
  echo "error: cannot reach your systemd user manager (XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR)." >&2
  echo "  Is systemd enabled (WSL: [boot] systemd=true in /etc/wsl.conf) and linger on?" >&2
  exit 1
fi

chmod +x "$ROOT/scripts/supervise.sh" "$ROOT/scripts/selfcheck.sh"
mkdir -p "$UNIT_DIR" "$ROOT/data"
if [[ ! -x "$ROOT/bin/grok-bridge" ]]; then
  echo "==> Building bin/grok-bridge"
  (cd "$ROOT" && PATH="$HOME/.local/go/bin:$PATH" go build -o bin/grok-bridge ./cmd/grok-bridge)
fi

if [[ -f "$DEST" ]]; then
  cp -a "$DEST" "$DEST.bak-$STAMP"
  echo "==> Backed up existing unit to $DEST.bak-$STAMP"
fi
sed "s|@REPO@|$ROOT|g" "$TEMPLATE" > "$DEST.tmp"
mv "$DEST.tmp" "$DEST"
echo "==> Wrote $DEST"

systemctl --user daemon-reload
systemctl --user enable grok-bridge.service
systemctl --user restart grok-bridge.service
sleep 2
systemctl --user --no-pager status grok-bridge.service | head -n 15 || true
echo
echo "Logs:   journalctl --user -u grok-bridge -f"
echo "Linger: $(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo unknown)"
