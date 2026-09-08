# Native hosts (Linux / macOS / Windows)

Grok Bridge can run as a **source host** from this tree on Linux, macOS, and Windows — no WSL required for the Windows path.

Grok Build CLI install is **always vendor/manual**. This repo checks for `grok`, resolves Grok home in an environment-agnostic way, and prints re-run steps when something cannot be auto-installed.

## Matrix

| | Linux | macOS | Windows |
|--|-------|-------|---------|
| Quick start | `./start.sh` | `./start.sh` | `.\start.ps1` |
| Full / host install | `./scripts/install.sh` (Tailscale + systemd) | `./scripts/install-macos.sh` (Homebrew + optional Tailscale cask) | `.\scripts\install.ps1` (winget/choco/scoop when present) |
| Prereq ensure | `scripts/ensure-prereqs.sh` | same (+ brew in install-macos) | `scripts/ensure-prereqs.ps1` |
| Grok Build ensure | `scripts/ensure-grok-build.sh` | same | `scripts/ensure-grok-build.ps1` |
| Binary | `bin/grok-bridge` | `bin/grok-bridge` | `bin\grok-bridge.exe` (`GOOS=windows`) |
| Default Grok home | `$HOME/.grok` | `$HOME/.grok` | `%USERPROFILE%\.grok` |
| Auto-install | apt/dnf/yum/pacman/brew + Go tarball | Homebrew (go, git, curl); Tailscale cask | winget / choco / scoop for Git & Go when available |
| Never auto | Unofficial `grok` installer | Unofficial `grok` installer | Unofficial `grok` installer |

## Grok Build check (all OSes)

Runs early in `start.sh` / `start.ps1` (before build/run):

1. `grok` on `PATH`, or `GROK_BRIDGE_GROK_BIN`
2. Grok home: `GROK_BRIDGE_GROK_HOME` → `GROK_HOME` → `$HOME/.grok` (Windows: `%USERPROFILE%\.grok`)
3. Optional warn if `sessions/` missing/empty (hub-native chats still work)
4. Optional reminder to start `grok agent --always-approve --no-leader serve …` when live Build send env is set

| Env | Effect |
|-----|--------|
| `SKIP_GROK_BUILD_CHECK=1` | Skip the check |
| `REQUIRE_GROK_BUILD=1` | Hard-fail (exit 2) if `grok` missing; default is warn + continue (hub-only) |
| `GROK_BRIDGE_INSTALL_WAIT=1` | Optional interactive pause when stdin is a TTY / interactive session |

When a dependency cannot be auto-installed, scripts print:

```text
error: missing <dep>
Do this:
  1. …
  2. …
Then re-run: ./start.sh   (or .\start.ps1)
```

and exit `2` (or `1` for some prereq-only failures). They do **not** block on `read` in non-interactive CI.

## Suggested flows

**Linux VM (phone via Tailscale):**

```bash
./scripts/install.sh
# or quick: ./start.sh
```

**macOS laptop (source host):**

```bash
./scripts/install-macos.sh   # REQUIRE_GROK_BUILD=1 by default
./start.sh
```

**Windows desktop (native, no WSL):**

```powershell
.\scripts\install.ps1        # REQUIRE_GROK_BUILD=1 by default
.\start.ps1
```

See also `README.md` (Native hosts section) and `docs/grok-bridge.env.example`.
