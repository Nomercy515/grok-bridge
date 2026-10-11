#!/usr/bin/env bash
# Startup self-check for grok-bridge.service (ExecStartPre). One line per check
# to the journal. Non-fatal except when the grok binary is missing (exit 1).
# Root cause it guards against (Oct 2026): the unit ran as root, so the
# auto-started `grok agent serve` used /root/.grok — no login, no sessions —
# and every session/load failed with FS_NOT_FOUND.
set -uo pipefail
BIND="${GROK_BRIDGE_GROK_AGENT_BIND:-127.0.0.1:2419}"
PORT="${BIND##*:}"
GH="${GROK_BRIDGE_GROK_HOME:-${GROK_HOME:-$HOME/.grok}}"
log() { echo "selfcheck $1 $2: $3"; }

if [[ "$(id -u)" == "0" ]]; then
  log FAIL run_user "running as root (HOME=$HOME) — ~/.grok resolves to /root/.grok; install as a user service: scripts/install-service.sh"
else
  log ok run_user "user=$(id -un) HOME=$HOME grok_home=$GH"
fi

BIN="$(command -v grok || true)"
if [[ -z "$BIN" ]]; then
  log FAIL grok_binary "grok not on PATH ($PATH) — install Grok Build or fix PATH in the unit"
  exit 1
fi
log ok grok_binary "$BIN"

OUT="$(timeout 15 "$BIN" models 2>&1 || true)"
if grep -qi "not authenticated" <<<"$OUT"; then
  log FAIL grok_auth "not logged in as $(id -un) — run: grok login"
elif grep -qi "logged in" <<<"$OUT"; then
  log ok grok_auth "logged in"
else
  log FAIL grok_auth "could not confirm login (offline or timeout)"
fi

if [[ -d "$GH/sessions" ]]; then
  log ok sessions_dir "$GH/sessions"
else
  log FAIL sessions_dir "$GH/sessions missing — set GROK_BRIDGE_GROK_HOME"
fi

L="$(ss -Hltnp "sport = :$PORT" 2>/dev/null || true)"
if [[ -z "$L" ]]; then
  log ok agent_port "$BIND free"
elif grep -q 'users:' <<<"$L"; then
  log ok agent_port "$BIND held by own process: $(grep -o 'users:.*' <<<"$L")"
else
  log FAIL agent_port "$BIND held by another user's process (stale root agent?) — sudo fuser -k $PORT/tcp"
fi
exit 0
