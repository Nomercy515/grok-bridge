#!/usr/bin/env bash
# Ensure local quick-start prerequisites (curl, Go ≥ 1.22, git).
# Sourced by start.sh. Idempotent. Not a full Tailscale install — see scripts/install.sh.
# macOS full host: scripts/install-macos.sh. Windows twins: ensure-prereqs.ps1 / start.ps1.
#
# Env:
#   SKIP_PREREQ_INSTALL=1  — check only; exit 1 if curl/Go missing or Go too old
#   GO_INSTALL_DIR         — default: $HOME/.local/go
#   GO_MIN_VERSION         — default: 1.22
#   GO_VERSION             — tarball version when installing (default: 1.22.12)
#   GROK_BRIDGE_INSTALL_WAIT=1 — optional TTY pause when a dep cannot be installed
#
# When sourced, defines ensure_prereqs. When executed, runs it.

GO_MIN_VERSION="${GO_MIN_VERSION:-1.22}"
GO_VERSION="${GO_VERSION:-1.22.12}"
GO_INSTALL_DIR="${GO_INSTALL_DIR:-${HOME}/.local/go}"
SKIP_PREREQ_INSTALL="${SKIP_PREREQ_INSTALL:-0}"

_ensure_info() { echo "==> $*"; }
_ensure_warn() { echo "warning: $*" >&2; }
_ensure_err() { echo "error: $*" >&2; }

_pkg_install() {
  local pkgs=("$@")
  if command -v apt-get >/dev/null 2>&1; then
    if [[ "$(id -u)" -eq 0 ]]; then
      apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${pkgs[@]}"
    elif command -v sudo >/dev/null 2>&1; then
      sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${pkgs[@]}"
    else
      return 1
    fi
  elif command -v dnf >/dev/null 2>&1; then
    if [[ "$(id -u)" -eq 0 ]]; then
      dnf install -y "${pkgs[@]}"
    elif command -v sudo >/dev/null 2>&1; then
      sudo dnf install -y "${pkgs[@]}"
    else
      return 1
    fi
  elif command -v yum >/dev/null 2>&1; then
    if [[ "$(id -u)" -eq 0 ]]; then
      yum install -y "${pkgs[@]}"
    elif command -v sudo >/dev/null 2>&1; then
      sudo yum install -y "${pkgs[@]}"
    else
      return 1
    fi
  elif command -v pacman >/dev/null 2>&1; then
    if [[ "$(id -u)" -eq 0 ]]; then
      pacman -Sy --noconfirm "${pkgs[@]}"
    elif command -v sudo >/dev/null 2>&1; then
      sudo pacman -Sy --noconfirm "${pkgs[@]}"
    else
      return 1
    fi
  elif command -v brew >/dev/null 2>&1; then
    brew install "${pkgs[@]}"
  else
    return 1
  fi
}

_version_ge() {
  # Return 0 if $1 >= $2 (dotted numeric versions).
  local a="$1" b="$2"
  local IFS=.
  # shellcheck disable=SC2206
  local av=($a) bv=($b)
  local i max=${#av[@]}
  if [[ ${#bv[@]} -gt $max ]]; then max=${#bv[@]}; fi
  for ((i = 0; i < max; i++)); do
    local x=${av[i]:-0} y=${bv[i]:-0}
    x=${x%%[!0-9]*}
    y=${y%%[!0-9]*}
    x=${x:-0}
    y=${y:-0}
    if ((x > y)); then return 0; fi
    if ((x < y)); then return 1; fi
  done
  return 0
}

_go_semver() {
  local raw
  raw="$(go env GOVERSION 2>/dev/null || go version 2>/dev/null || true)"
  if [[ "$raw" =~ go([0-9]+\.[0-9]+(\.[0-9]+)?) ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo ""
  fi
}

_go_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local ver
  ver="$(_go_semver)"
  [[ -n "$ver" ]] || return 1
  _version_ge "$ver" "$GO_MIN_VERSION"
}

_detect_go_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "" ;;
  esac
}

_install_go_tarball() {
  local os arch url tmp parent
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_detect_go_arch)"
  if [[ -z "$arch" ]]; then
    _ensure_err "unsupported CPU architecture '$(uname -m)' for automatic Go install"
    _ensure_err "Install Go ≥ ${GO_MIN_VERSION} from https://go.dev/dl/ then re-run."
    return 1
  fi
  case "$os" in
    linux|darwin) ;;
    *)
      _ensure_err "unsupported OS '$os' for automatic Go install"
      _ensure_err "Install Go ≥ ${GO_MIN_VERSION} from https://go.dev/dl/ then re-run."
      return 1
      ;;
  esac

  url="https://go.dev/dl/go${GO_VERSION}.${os}-${arch}.tar.gz"
  parent="$(dirname "$GO_INSTALL_DIR")"
  _ensure_info "Downloading Go ${GO_VERSION} (${os}/${arch}) into ${GO_INSTALL_DIR}"
  tmp="$(mktemp -d)"
  if ! curl -fsSL "$url" -o "${tmp}/go.tgz"; then
    rm -rf "$tmp"
    _ensure_err "failed to download Go tarball from go.dev"
    return 1
  fi
  mkdir -p "$parent"
  rm -rf "$GO_INSTALL_DIR"
  # Official archive extracts as ./go — unpack beside the target, then rename if needed.
  tar -C "$tmp" -xzf "${tmp}/go.tgz"
  if [[ ! -d "${tmp}/go" ]]; then
    rm -rf "$tmp"
    _ensure_err "unexpected Go tarball layout"
    return 1
  fi
  mv "${tmp}/go" "$GO_INSTALL_DIR"
  rm -rf "$tmp"
  export PATH="${GO_INSTALL_DIR}/bin:${PATH}"
  hash -r 2>/dev/null || true
}

_ensure_curl() {
  if command -v curl >/dev/null 2>&1; then
    return 0
  fi
  if [[ "$SKIP_PREREQ_INSTALL" == "1" ]]; then
    _ensure_err "curl is required but not found (SKIP_PREREQ_INSTALL=1)"
    return 1
  fi
  _ensure_info "Installing curl"
  if ! _pkg_install curl; then
    _ensure_err "could not install curl — install it manually and re-run"
    return 1
  fi
  if ! command -v curl >/dev/null 2>&1; then
    _ensure_err "curl still not found after install attempt"
    return 1
  fi
}

_ensure_go() {
  if [[ -x "${GO_INSTALL_DIR}/bin/go" ]]; then
    export PATH="${GO_INSTALL_DIR}/bin:${PATH}"
    hash -r 2>/dev/null || true
  fi

  if _go_ok; then
    _ensure_info "Found Go $(_go_semver) ($(command -v go))"
    return 0
  fi

  if command -v go >/dev/null 2>&1; then
    _ensure_warn "Go $(_go_semver) is older than ${GO_MIN_VERSION}"
  else
    _ensure_warn "Go not found on PATH"
  fi

  if [[ "$SKIP_PREREQ_INSTALL" == "1" ]]; then
    _ensure_err "Go ≥ ${GO_MIN_VERSION} is required (SKIP_PREREQ_INSTALL=1)"
    return 1
  fi

  local os
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  if [[ "$os" == "darwin" ]] && command -v brew >/dev/null 2>&1; then
    _ensure_info "Installing Go via Homebrew"
    if brew install go; then
      hash -r 2>/dev/null || true
      if _go_ok; then
        _ensure_info "Found Go $(_go_semver) ($(command -v go))"
        return 0
      fi
      _ensure_warn "brew install go finished but Go ≥ ${GO_MIN_VERSION} still not on PATH — trying tarball"
    else
      _ensure_warn "brew install go failed — trying official tarball"
    fi
  fi

  if ! command -v curl >/dev/null 2>&1; then
    _ensure_err "curl is required to download Go"
    return 1
  fi
  _install_go_tarball || return 1

  if ! _go_ok; then
    _ensure_err "Go ≥ ${GO_MIN_VERSION} still unavailable after install"
    _ensure_err "Install from https://go.dev/dl/ then re-run (or set PATH to include ${GO_INSTALL_DIR}/bin)"
    return 1
  fi
  _ensure_info "Found Go $(_go_semver) ($(command -v go))"
}

_ensure_git() {
  if command -v git >/dev/null 2>&1; then
    return 0
  fi
  if [[ "$SKIP_PREREQ_INSTALL" == "1" ]]; then
    _ensure_warn "git not found (SKIP_PREREQ_INSTALL=1) — continuing; go build may still work"
    return 0
  fi
  _ensure_info "Installing git"
  if ! _pkg_install git; then
    _ensure_warn "could not install git — continuing; go build may still work"
    return 0
  fi
  if ! command -v git >/dev/null 2>&1; then
    _ensure_warn "git still not found after install attempt — continuing"
  fi
}

ensure_prereqs() {
  _ensure_curl
  _ensure_go
  _ensure_git
}

if [[ "${BASH_SOURCE[0]-}" == "${0}" ]]; then
  set -euo pipefail
  ensure_prereqs
fi
