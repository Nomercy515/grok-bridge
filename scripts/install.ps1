# Windows native install / host setup for Grok Bridge.
# Checks deps (winget/choco/scoop when available), Grok Build readiness,
# builds bin\grok-bridge.exe. Does NOT require WSL.
#
# Usage:
#   .\scripts\install.ps1
#   $env:REQUIRE_GROK_BUILD=1; .\scripts\install.ps1
#   $env:SKIP_TAILSCALE=1; .\scripts\install.ps1
#
# Grok Build install remains vendor/manual — this script never invents an unofficial installer.

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

# Prefer Build present on a Windows source host unless caller overrides.
if (-not $env:REQUIRE_GROK_BUILD) { $env:REQUIRE_GROK_BUILD = '1' }

Write-Host '==> Windows Grok Bridge install'

. "$Root\scripts\ensure-prereqs.ps1"
Ensure-Prereqs

# Optional Tailscale hint (no silent system-wide install without a package manager id).
if ($env:SKIP_TAILSCALE -ne '1') {
    if (Get-Command tailscale -ErrorAction SilentlyContinue) {
        Write-Host '==> Tailscale already on PATH'
    } else {
        $mgr = $null
        if (Get-Command winget -ErrorAction SilentlyContinue) { $mgr = 'winget' }
        elseif (Get-Command choco -ErrorAction SilentlyContinue) { $mgr = 'choco' }
        if ($mgr -eq 'winget') {
            Write-Host '==> Attempting Tailscale via winget'
            winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements
            if ($LASTEXITCODE -ne 0) {
                Write-Host 'error: missing Tailscale' -ForegroundColor Red
                Write-Host 'Do this:'
                Write-Host '  1. Install from https://tailscale.com/download/windows'
                Write-Host '  2. Or: winget install Tailscale.Tailscale'
                Write-Host '  3. Sign in to your tailnet'
                Write-Host 'Then re-run: .\scripts\install.ps1'
                Write-Host 'Or skip: $env:SKIP_TAILSCALE=1; .\scripts\install.ps1'
                if ($env:GROK_BRIDGE_INSTALL_WAIT -eq '1' -and [Environment]::UserInteractive) {
                    Read-Host 'Press Enter after completing the steps (or Ctrl-C to abort)'
                }
                exit 2
            }
        } else {
            Write-Host 'error: missing Tailscale' -ForegroundColor Red
            Write-Host 'Do this:'
            Write-Host '  1. Install from https://tailscale.com/download/windows'
            Write-Host '  2. Sign in to your tailnet'
            Write-Host 'Then re-run: .\scripts\install.ps1'
            Write-Host 'Or skip: $env:SKIP_TAILSCALE=1; .\scripts\install.ps1'
            if ($env:GROK_BRIDGE_INSTALL_WAIT -eq '1' -and [Environment]::UserInteractive) {
                Read-Host 'Press Enter after completing the steps (or Ctrl-C to abort)'
            }
            exit 2
        }
    }
} else {
    Write-Warning 'SKIP_TAILSCALE=1 — skipping Tailscale'
}

. "$Root\scripts\ensure-grok-build.ps1"
Ensure-GrokBuild

New-Item -ItemType Directory -Force -Path (Join-Path $Root 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Root 'data') | Out-Null

Write-Host '==> Building grok-bridge.exe (GOOS=windows)'
$env:GOOS = 'windows'
$exe = Join-Path $Root 'bin\grok-bridge.exe'
& go build -o $exe ./cmd/grok-bridge
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host ''
Write-Host '==================================================================='
Write-Host ' Windows Grok Bridge host ready'
Write-Host '==================================================================='
Write-Host ' Start:  .\start.ps1'
Write-Host ' Demo:   http://127.0.0.1:4020/?demo=1'
Write-Host ' Grok home default: %USERPROFILE%\.grok'
Write-Host ' Grok Build install remains vendor/manual — not bundled by this repo.'
Write-Host ' See docs/NATIVE_HOSTS.md'
Write-Host '==================================================================='
