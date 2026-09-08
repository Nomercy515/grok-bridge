# Native hosts (Linux / macOS / Windows)

Grok Bridge can run as a **source host** from this tree on Linux, macOS, and Windows — no WSL required for the Windows path.

Grok Build CLI install is **always vendor/manual**. This repo checks for `grok`, resolves Grok home in an environment-agnostic way, and prints numbered re-run steps when something cannot be auto-installed. It does not ship an unofficial `grok` installer.

## Matrix

| | Linux | macOS | Windows |
|--|-------|-------|---------|
| Quick start | `./start.sh` | `./start.sh` | `.\start.ps1` |
| Full / host install | `./scripts/install.sh` (Tailscale + systemd) | `./scripts/install-macos.sh` (Homebrew + optional Tailscale cask) | `.\scripts\install.ps1` (winget/choco/scoop when present) |
| Prereq ensure | `scripts/ensure-prereqs.sh` | same (+ brew in install-macos) | `scripts/ensure-prereqs.ps1` |
| Grok Build ensure | `scripts/ensure-grok-build.sh` | same | `scripts/ensure-grok-build.ps1` |
| Binary | `bin/grok-bridge` | `bin/grok-bridge` | `bin\grok-bridge.exe` (native `go build`; scripts do not force `GOOS`) |
| Default Grok home | `$HOME/.grok` | `$HOME/.grok` | `%USERPROFILE%\.grok` |
| Auto-install | apt/dnf/yum/pacman/brew + Go tarball | Homebrew (go, git, curl); Tailscale cask | winget / choco / scoop for Git & Go when available; Tailscale via winget (`Tailscale.Tailscale`) or `choco install tailscale -y` |
| Never auto | Unofficial `grok` installer | Unofficial `grok` installer | Unofficial `grok` installer |

Windows PowerShell may refuse unsigned scripts. One-shot bypass, or set a user policy once:

```powershell
powershell -ExecutionPolicy Bypass -File .\start.ps1
# or once for your user:
Set-ExecutionPolicy -Scope CurrentUser RemoteSigned
```

Same `-ExecutionPolicy Bypass -File` form works for `.\scripts\install.ps1`.

## Grok Build check (all OSes)

Runs early in `start.sh` / `start.ps1` (before build/run):

1. `grok` on `PATH`, or `GROK_BRIDGE_GROK_BIN`
2. Grok home: `GROK_BRIDGE_GROK_HOME` → `GROK_HOME` → `$HOME/.grok` (Windows: `%USERPROFILE%\.grok`)
3. Optional warn if `sessions/` missing/empty (hub-native chats still work)
4. Optional reminder to start `grok agent --always-approve --no-leader serve …` when live Build send env is set

| Env | Effect |
|-----|--------|
| `SKIP_GROK_BUILD_CHECK=1` | Skip the check |
| `REQUIRE_GROK_BUILD=1` | Hard-fail (exit 2) if `grok` missing |
| `GROK_BRIDGE_INSTALL_WAIT=1` | Optional pause **only** when stdin is a TTY / the session is interactive. Non-interactive runs never block |

### `REQUIRE_GROK_BUILD` defaults (intentional)

| Entry point | Default if unset | Why |
|-------------|------------------|-----|
| `./start.sh`, `.\start.ps1` | warn + continue (hub-only) | Quick start should still build and serve the hub without Build |
| `scripts/install-macos.sh`, `.\scripts\install.ps1` | `REQUIRE_GROK_BUILD=1` | Source-host install expects Build; missing `grok` exits 2 with vendor steps |
| `scripts/install.sh` (Linux) | warn + continue | Existing Linux VM installs must not start failing just because `grok` is not installed yet. Pass `REQUIRE_GROK_BUILD=1` to fail hard |

Linux `start.sh` stays warn + continue unless you set `REQUIRE_GROK_BUILD=1`. Do not treat the macOS/Windows install default as the Linux install default.

### When a dependency cannot be auto-installed

`scripts/ensure-prereqs.sh`, `scripts/ensure-grok-build.sh`, and the Windows ensure/install scripts print numbered steps and exit. They do **not** hang on `read` unless stdin is a TTY **and** `GROK_BRIDGE_INSTALL_WAIT=1`.

```text
error: missing <dep>
Do this:
  1. …
  2. …
Then re-run: ./start.sh   (or .\start.ps1)
```

Exit `2` is the incomplete-install / missing-Build code. Some prereq-only failures (curl/Go) still return `1`. `git` missing is a warning only — start continues.

`GROK_BRIDGE_INSTALL_WAIT` is implemented in those ensure scripts (and Windows install). It is not a global pause for every script in the repo.

## Suggested flows

**Linux VM (phone via Tailscale):**

```bash
./scripts/install.sh
# or quick: ./start.sh
# warn-only if grok is missing; REQUIRE_GROK_BUILD=1 ./scripts/install.sh to fail hard
```

**macOS laptop (source host):**

```bash
./scripts/install-macos.sh   # REQUIRE_GROK_BUILD=1 by default
./start.sh
# Build-only host (no Tailscale): SKIP_TAILSCALE=1 ./scripts/install-macos.sh
```

**Windows desktop (native, no WSL):**

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
.\start.ps1
# REQUIRE_GROK_BUILD=1 is the install.ps1 default.
# Build-only host (no Tailscale) — not a dead end:
$env:SKIP_TAILSCALE=1; .\scripts\install.ps1
```

## Smoke checklist (Windows / macOS)

1. **Prereqs** — Go ≥ 1.22 and git present (auto-installed when a package manager is available). Windows: winget, else choco, else scoop. macOS: Homebrew via `./scripts/install-macos.sh`.
2. **Build** — Windows `.\start.ps1` or `.\scripts\install.ps1` writes `bin\grok-bridge.exe`. macOS `./start.sh` or `./scripts/install-macos.sh` writes `bin/grok-bridge`. Neither Windows script forces `GOOS`; a pre-set `GOOS`/`GOARCH` in that session is left alone and will affect the build.
3. **Demo** — start the hub and open `http://127.0.0.1:4020/?demo=1`.
4. **Build check, warn vs require**
   - Warn + continue (hub-only): `.\start.ps1` or `./start.sh` with `grok` missing and `REQUIRE_GROK_BUILD` unset. Expect a warning, then the hub still starts.
   - Hard fail: `REQUIRE_GROK_BUILD=1` (already the default for `.\scripts\install.ps1` and `./scripts/install-macos.sh`) prints numbered “Do this / re-run” and exits 2. Optional TTY pause only if `GROK_BRIDGE_INSTALL_WAIT=1`.
   - Skip the check: `SKIP_GROK_BUILD_CHECK=1`.
   - No Tailscale on a Build-only host: `SKIP_TAILSCALE=1` on the install script (Windows and macOS).

See also `README.md` (Native hosts section) and `docs/grok-bridge.env.example`.
