#!/usr/bin/env bash
# Stop and remove the Grok Bridge systemd USER service for the invoking user.
#   scripts/uninstall-service.sh                 # keeps linger on
#   scripts/uninstall-service.sh --disable-linger
# The removed unit is kept as a timestamped .bak next to where it was.
set -euo pipefail
if [[ "$(id -u)" -eq 0 ]]; then
  echo "error: run as your normal user, not root/sudo" >&2
  exit 1
fi
DISABLE_LINGER=0
[[ "${1:-}" == "--disable-linger" ]] && DISABLE_LINGER=1
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
DEST="$UNIT_DIR/grok-bridge.service"

systemctl --user disable --now grok-bridge.service 2>/dev/null || true
if [[ -f "$DEST" ]]; then
  mv "$DEST" "$DEST.bak-$(date +%Y%m%d-%H%M%S)"
  echo "==> Removed $DEST (backup kept)"
fi
systemctl --user daemon-reload || true
if [[ "$DISABLE_LINGER" == 1 ]]; then
  echo "==> Disabling linger for $USER (sudo)"
  sudo loginctl disable-linger "$USER"
fi
echo "Grok Bridge user service removed."
