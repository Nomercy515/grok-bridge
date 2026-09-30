#!/usr/bin/env bash
# macOS native host setup for Grok Bridge (source host).
# Auto-installs Go/git/curl via Homebrew when available; Tailscale via brew cask.
# Grok Build CLI cannot be invented here — prints vendor/manual steps and exits
# non-zero so you can install it and re-run.
#
# Usage:
#   ./scripts/install-macos.sh
#   SKIP_TAILSCALE=1 ./scripts/install-macos.sh
#   REQUIRE_GROK_BUILD=1 ./scripts/install-macos.sh   # fail if grok missing (default for this script)
#
# Linux Tailscale+systemd install remains: ./scripts/install.sh
# Quick start on any Unix: ./start.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKIP_TAILSCALE="${SKIP_TAILSCALE:-0}"
# macOS install treats Build as expected for a source host unless explicitly skipped.
REQUIRE_GROK_BUILD="${REQUIRE_GROK_BUILD:-1}"
export REQUIRE_GROK_BUILD

info() { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }
err() { echo "error: $*" >&2; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "$os" != "darwin" ]]; then
  err "install-macos.sh is for macOS (Darwin). On Linux use ./scripts/install.sh; or ./start.sh for quick start."
  exit 1
fi

wait_manual() {
  local dep="$1"
  shift
  err "missing ${dep}"
  echo "Do this:" >&2
  local i=1 step
  for step in "$@"; do
    echo "  ${i}. ${step}" >&2
    i=$((i + 1))
  done
  echo "Then re-run: ./scripts/install-macos.sh   (or ./start.sh)" >&2
  if [[ -t 0 && "${GROK_BRIDGE_INSTALL_WAIT:-0}" == "1" ]]; then
    read -r -p "Press Enter after completing the steps (or Ctrl-C to abort)… " _ || true
  fi
  exit 2
}

# --- Homebrew + base deps ---
if ! command -v brew >/dev/null 2>&1; then
  wait_manual "Homebrew" \
    "Install Homebrew from https://brew.sh" \
    "Open a new terminal so brew is on PATH"
fi

info "Ensuring curl, git, go via Homebrew"
brew_pkgs=()
command -v curl >/dev/null 2>&1 || brew_pkgs+=(curl)
command -v git >/dev/null 2>&1 || brew_pkgs+=(git)
need_go=0
if command -v go >/dev/null 2>&1; then
  ver="$(go env GOVERSION 2>/dev/null || go version || true)"
  if [[ ! "$ver" =~ go1\.(2[2-9]|[3-9][0-9]) ]]; then
    need_go=1
  fi
else
  need_go=1
fi
if [[ "$need_go" -eq 1 ]]; then
  brew_pkgs+=(go)
fi
if [[ ${#brew_pkgs[@]} -gt 0 ]]; then
  brew install "${brew_pkgs[@]}"
fi

# shellcheck disable=SC1091
source "$ROOT/scripts/ensure-prereqs.sh"
# Prefer check-only after brew; still allow tarball fallback if brew go is odd.
ensure_prereqs

# --- Tailscale (optional) ---
if [[ "$SKIP_TAILSCALE" != "1" ]]; then
  if command -v tailscale >/dev/null 2>&1; then
    info "Tailscale already installed"
  else
    info "Installing Tailscale via Homebrew cask"
    if brew install --cask tailscale; then
      info "Tailscale app installed — open Tailscale from Applications and sign in, then re-run if needed"
    else
      wait_manual "Tailscale" \
        "Install from https://tailscale.com/download/mac or: brew install --cask tailscale" \
        "Sign in to your tailnet" \
        "Or skip: SKIP_TAILSCALE=1 ./scripts/install-macos.sh"
    fi
  fi
else
  warn "SKIP_TAILSCALE=1 — skipping Tailscale"
fi

# --- Grok Build (manual / vendor only) ---
# shellcheck disable=SC1091
source "$ROOT/scripts/ensure-grok-build.sh"
# ensure_grok_build exits 2 when REQUIRE_GROK_BUILD=1 and grok missing
ensure_grok_build

# --- Build binary ---
info "Building grok-bridge"
mkdir -p bin data
chmod +x \
  "$ROOT/start.sh" \
  "$ROOT/scripts/ensure-prereqs.sh" \
  "$ROOT/scripts/ensure-grok-build.sh" \
  "$ROOT/scripts/install-macos.sh" \
  2>/dev/null || true
go build -o bin/grok-bridge ./cmd/grok-bridge

echo ""
echo "==================================================================="
echo " macOS Grok Bridge host ready"
echo "==================================================================="
echo " Start:  ./start.sh"
echo " Demo:   http://127.0.0.1:4020/?demo=1"
echo " Env:    see docs/grok-bridge.env.example and docs/NATIVE_HOSTS.md"
echo " Grok Build install remains vendor/manual — not bundled by this repo."
echo "==================================================================="
