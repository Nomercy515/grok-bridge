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
# Tailscale package ids used here are official: winget Tailscale.Tailscale, choco tailscale.

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

# Prefer Build present on a Windows source host unless caller overrides.
if (-not $env:REQUIRE_GROK_BUILD) { $env:REQUIRE_GROK_BUILD = '1' }

function Update-SessionPath {
    param([string[]]$PrependDirs = @())

    # Prepend Tailscale (and any other newly discovered dirs) onto the current
    # process PATH, then merge Machine+User entries that are not already present.
    # Never replace $env:Path — a session-only go (profile, zip, mise) must stay
    # findable after a Tailscale install attempt, or the later `go build` fails.
    $ordered = New-Object System.Collections.Generic.List[string]
    foreach ($dir in $PrependDirs) {
        if ($dir) { [void]$ordered.Add($dir) }
    }

    # Tailscale's MSI drops tailscale.exe here even when the installer has not
    # updated this session's PATH yet. Prepend so Get-Command finds it.
    foreach ($dirRoot in @(${env:ProgramFiles}, ${env:ProgramFiles(x86)})) {
        if (-not $dirRoot) { continue }
        $tsDir = Join-Path $dirRoot 'Tailscale'
        if (Test-Path -LiteralPath (Join-Path $tsDir 'tailscale.exe')) {
            [void]$ordered.Add($tsDir)
        }
    }

    if ($env:Path) {
        foreach ($dir in ($env:Path -split ';')) {
            if ($dir) { [void]$ordered.Add($dir) }
        }
    }

    $machine = [System.Environment]::GetEnvironmentVariable('Path', 'Machine')
    $user = [System.Environment]::GetEnvironmentVariable('Path', 'User')
    foreach ($chunk in @($machine, $user)) {
        if (-not $chunk) { continue }
        foreach ($dir in ($chunk -split ';')) {
            if ($dir) { [void]$ordered.Add($dir) }
        }
    }

    # Standard install locations, appended so they do not shadow a session-only go.
    foreach ($dirRoot in @(${env:ProgramFiles}, ${env:ProgramFiles(x86)})) {
        if (-not $dirRoot) { continue }
        $goBin = Join-Path $dirRoot 'Go\bin'
        if (Test-Path -LiteralPath (Join-Path $goBin 'go.exe')) {
            [void]$ordered.Add($goBin)
        }
        foreach ($gitRel in @('Git\cmd', 'Git\bin')) {
            $gitDir = Join-Path $dirRoot $gitRel
            if (Test-Path -LiteralPath (Join-Path $gitDir 'git.exe')) {
                [void]$ordered.Add($gitDir)
            }
        }
    }

    $seen = @{}
    $merged = New-Object System.Collections.Generic.List[string]
    foreach ($dir in $ordered) {
        $trimmed = "$dir".Trim()
        if (-not $trimmed) { continue }
        $key = $trimmed.TrimEnd([char]92).ToLowerInvariant()
        if ($seen.ContainsKey($key)) { continue }
        $seen[$key] = $true
        [void]$merged.Add($trimmed)
    }

    if ($merged.Count -gt 0) {
        $env:Path = ($merged -join ';')
    }
}

function Test-TailscalePresent {
    if (Get-Command tailscale -ErrorAction SilentlyContinue) { return $true }
    if (Get-Command tailscale.exe -ErrorAction SilentlyContinue) { return $true }
    foreach ($dirRoot in @(${env:ProgramFiles}, ${env:ProgramFiles(x86)})) {
        if (-not $dirRoot) { continue }
        if (Test-Path -LiteralPath (Join-Path $dirRoot 'Tailscale\tailscale.exe')) { return $true }
    }
    return $false
}

function Exit-TailscaleManual {
    Write-Host 'error: missing Tailscale' -ForegroundColor Red
    Write-Host 'Do this:' -ForegroundColor Yellow
    Write-Host '  1. Install from https://tailscale.com/download/windows'
    Write-Host '  2. Or: winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements'
    Write-Host '  3. Or: choco install tailscale -y'
    Write-Host '  4. Open Tailscale and sign in to your tailnet'
    Write-Host 'Then re-run: .\scripts\install.ps1' -ForegroundColor Yellow
    Write-Host 'Or skip: $env:SKIP_TAILSCALE=1; .\scripts\install.ps1'
    if ($env:GROK_BRIDGE_INSTALL_WAIT -eq '1' -and [Environment]::UserInteractive) {
        Read-Host 'Press Enter after completing the steps (or Ctrl-C to abort)'
    }
    exit 2
}

Write-Host '==> Windows Grok Bridge install'

. "$Root\scripts\ensure-prereqs.ps1"
Ensure-Prereqs

# Optional Tailscale. winget id Tailscale.Tailscale; Chocolatey id is `tailscale`
# (https://community.chocolatey.org/packages/tailscale — maintained by Tailscale).
if ($env:SKIP_TAILSCALE -ne '1') {
    if (Test-TailscalePresent) {
        Write-Host '==> Tailscale already on PATH'
    } else {
        $mgr = $null
        if (Get-Command winget -ErrorAction SilentlyContinue) { $mgr = 'winget' }
        elseif (Get-Command choco -ErrorAction SilentlyContinue) { $mgr = 'choco' }

        $installed = $false
        if ($mgr -eq 'winget') {
            Write-Host '==> Attempting Tailscale via winget'
            winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements
            $code = $LASTEXITCODE
            # winget is non-zero when the package is already present; refresh PATH
            # and re-check before treating that as a hard failure.
            Update-SessionPath
            if ($code -eq 0 -or (Test-TailscalePresent)) {
                $installed = $true
            }
        } elseif ($mgr -eq 'choco') {
            Write-Host '==> Attempting Tailscale via Chocolatey'
            choco install tailscale -y
            $code = $LASTEXITCODE
            Update-SessionPath
            if ($code -eq 0 -or (Test-TailscalePresent)) {
                $installed = $true
            }
        }

        if (-not $installed) {
            Exit-TailscaleManual
        }

        # Match install-macos.sh: app may be installed before the CLI is on PATH.
        if (Test-TailscalePresent) {
            Write-Host '==> Tailscale installed — open Tailscale and sign in to your tailnet'
        } else {
            Write-Host '==> Tailscale install reported success — open Tailscale and sign in to your tailnet (open a new shell if tailscale.exe is not yet on PATH)'
        }
    }
} else {
    Write-Warning 'SKIP_TAILSCALE=1 — skipping Tailscale'
}

. "$Root\scripts\ensure-grok-build.ps1"
Ensure-GrokBuild

New-Item -ItemType Directory -Force -Path (Join-Path $Root 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Root 'data') | Out-Null

# Native Windows: do not force GOOS/GOARCH. Go defaults to the host OS/arch.
# A pre-set GOOS/GOARCH in this session (leftover from a cross-compile) is left
# unchanged and will affect `go build` — unset them first if you need a native exe.
Write-Host '==> Building grok-bridge.exe'
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
