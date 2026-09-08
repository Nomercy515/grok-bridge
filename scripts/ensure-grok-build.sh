#!/usr/bin/env bash
# Ensure Grok Build readiness for Bridge as a source host.
# Sourced by start.sh (and scripts/install*.sh). Idempotent.
# PowerShell twin: scripts/ensure-grok-build.ps1
#
# Checks (env-agnostic — never hardcode WSL/user paths):
#   - grok on PATH, or GROK_BRIDGE_GROK_BIN
#   - Grok home: GROK_BRIDGE_GROK_HOME → GROK_HOME → $HOME/.grok
#   - Optional warn if sessions/ missing/empty (hub-native chats still work)
#   - Optional reminder to start `grok agent serve` for live Build send
#
# Env:
#   SKIP_GROK_BUILD_CHECK=1  — skip entirely
#   REQUIRE_GROK_BUILD=1     — hard-fail (exit 2) if grok missing
#                            — default: warn + continue (hub-only)
#   GROK_BRIDGE_GROK_BIN     — explicit path to grok binary
#   GROK_BRIDGE_INSTALL_WAIT=1 + TTY — optional interactive pause before exit
#
# When sourced, defines ensure_grok_build. When executed, runs it.

SKIP_GROK_BUILD_CHECK="${SKIP_GROK_BUILD_CHECK:-0}"
REQUIRE_GROK_BUILD="${REQUIRE_GROK_BUILD:-0}"

_gb_info() { echo "==> $*"; }
_gb_warn() { echo "warning: $*" >&2; }
_gb_err() { echo "error: $*" >&2; }

# Shared “wait for user / re-run” pattern. Exit 2 = incomplete install.
# Does not block on read unless stdin is a TTY and GROK_BRIDGE_INSTALL_WAIT=1.
_gb_wait_for_user() {
  local dep="$1"
  shift
  _gb_err "missing ${dep}"
  echo "Do this:" >&2
  local i=1
  local step
  for step in "$@"; do
    echo "  ${i}. ${step}" >&2
    i=$((i + 1))
  done
  echo "Then re-run: ./start.sh   (or ./scripts/install.sh / ./scripts/install-macos.sh)" >&2
  if [[ -t 0 && "${GROK_BRIDGE_INSTALL_WAIT:-0}" == "1" ]]; then
    read -r -p "Press Enter after completing the steps (or Ctrl-C to abort)… " _ || true
  fi
  return 2
}

_gb_resolve_home() {
  if [[ -n "${GROK_BRIDGE_GROK_HOME:-}" ]]; then
    echo "${GROK_BRIDGE_GROK_HOME}"
    return
  fi
  if [[ -n "${GROK_HOME:-}" ]]; then
    echo "${GROK_HOME}"
    return
  fi
  if [[ -n "${HOME:-}" ]]; then
    echo "${HOME}/.grok"
    return
  fi
  echo ""
}

_gb_find_bin() {
  if [[ -n "${GROK_BRIDGE_GROK_BIN:-}" ]]; then
    if [[ -x "${GROK_BRIDGE_GROK_BIN}" ]]; then
      echo "${GROK_BRIDGE_GROK_BIN}"
      return 0
    fi
    _gb_warn "GROK_BRIDGE_GROK_BIN=${GROK_BRIDGE_GROK_BIN} is not executable"
    return 1
  fi
  if command -v grok >/dev/null 2>&1; then
    command -v grok
    return 0
  fi
  return 1
}

_gb_sessions_note() {
  local home="$1"
  local sessions="${home}/sessions"
  if [[ -z "$home" ]]; then
    _gb_warn "Grok home could not be resolved (set GROK_BRIDGE_GROK_HOME or GROK_HOME)"
    return
  fi
  _gb_info "Grok home: ${home}"
  if [[ ! -d "$sessions" ]]; then
    _gb_warn "sessions/ missing under ${home} — hub can still start for hub-native chats; Build list will be empty"
    return
  fi
  # Empty if no entries (ignore . and ..)
  local count
  count="$(find "$sessions" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "${count:-0}" -eq 0 ]]; then
    _gb_warn "sessions/ is empty under ${home} — hub-native chats still work; Build list will be empty"
  else
    _gb_info "Found Build sessions under ${sessions}"
  fi
}

_gb_agent_reminder() {
  # Soft hint when live Build send env looks desired / not configured.
  if [[ -n "${GROK_BRIDGE_GROK_AGENT_WS:-}" || -n "${GROK_BRIDGE_GROK_AGENT_SECRET:-}" || "${GROK_BRIDGE_GROK_AGENT_AUTO_START:-0}" == "1" ]]; then
    _gb_info "Live Build send: ensure agent is up, e.g."
    echo "    grok agent --always-approve --no-leader serve --bind 127.0.0.1:2419 --secret <token>" >&2
    echo "  or set GROK_BRIDGE_GROK_AGENT_WS / GROK_BRIDGE_GROK_AGENT_SECRET / GROK_BRIDGE_GROK_AGENT_AUTO_START" >&2
  fi
}

ensure_grok_build() {
  if [[ "$SKIP_GROK_BUILD_CHECK" == "1" ]]; then
    _gb_info "Skipping Grok Build check (SKIP_GROK_BUILD_CHECK=1)"
    return 0
  fi

  _gb_info "Checking Grok Build readiness"

  local bin=""
  if bin="$(_gb_find_bin)"; then
    _gb_info "Found grok: ${bin}"
  else
    if [[ "$REQUIRE_GROK_BUILD" == "1" ]]; then
      _gb_wait_for_user "grok (Grok Build CLI)" \
        "Install Grok Build from the vendor / product docs for your OS (no unofficial installer is bundled here)." \
        "Ensure the grok binary is on PATH, or set GROK_BRIDGE_GROK_BIN to its full path." \
        "Optionally set GROK_BRIDGE_GROK_HOME (or GROK_HOME) if sessions are not under \$HOME/.grok."
      return 2
    fi
    _gb_warn "grok not found on PATH (and GROK_BRIDGE_GROK_BIN unset)"
    _gb_warn "Continuing in hub-only mode (Build list/send unavailable). Set REQUIRE_GROK_BUILD=1 to fail hard."
    _gb_warn "To enable Build later: install Grok Build from vendor docs, then re-run ./start.sh"
  fi

  local home
  home="$(_gb_resolve_home)"
  _gb_sessions_note "$home"
  _gb_agent_reminder
  return 0
}

if [[ "${BASH_SOURCE[0]-}" == "${0}" ]]; then
  set -euo pipefail
  ensure_grok_build
fi
