# Ensure Grok Build readiness for Bridge as a source host (Windows).
# Dot-sourced by start.ps1 / install.ps1. Bash twin: ensure-grok-build.sh
#
# Env:
#   SKIP_GROK_BUILD_CHECK=1  — skip
#   REQUIRE_GROK_BUILD=1     — throw / exit 2 if grok missing (default: warn + continue)
#   GROK_BRIDGE_GROK_BIN     — explicit path to grok.exe
#   GROK_BRIDGE_GROK_HOME / GROK_HOME — home override; else %USERPROFILE%\.grok
#   GROK_BRIDGE_INSTALL_WAIT=1 — optional Read-Host pause when failing

function Write-GbInfo([string]$Message) { Write-Host "==> $Message" }
function Write-GbWarn([string]$Message) { Write-Warning $Message }
function Write-GbErr([string]$Message) { Write-Host "error: $Message" -ForegroundColor Red }

function Invoke-GbWaitForUser {
    param(
        [Parameter(Mandatory = $true)][string]$Dep,
        [Parameter(Mandatory = $true)][string[]]$Steps,
        [string]$Rerun = '.\start.ps1'
    )
    Write-GbErr "missing $Dep"
    Write-Host "Do this:" -ForegroundColor Yellow
    $i = 1
    foreach ($step in $Steps) {
        Write-Host ("  {0}. {1}" -f $i, $step)
        $i++
    }
    Write-Host "Then re-run: $Rerun" -ForegroundColor Yellow
    if ($env:GROK_BRIDGE_INSTALL_WAIT -eq '1' -and [Environment]::UserInteractive) {
        Read-Host "Press Enter after completing the steps (or Ctrl-C to abort)"
    }
    exit 2
}

function Resolve-GrokHome {
    if (-not [string]::IsNullOrWhiteSpace($env:GROK_BRIDGE_GROK_HOME)) {
        return $env:GROK_BRIDGE_GROK_HOME.TrimEnd('\', '/')
    }
    if (-not [string]::IsNullOrWhiteSpace($env:GROK_HOME)) {
        return $env:GROK_HOME.TrimEnd('\', '/')
    }
    if (-not [string]::IsNullOrWhiteSpace($env:USERPROFILE)) {
        return Join-Path $env:USERPROFILE '.grok'
    }
    return ''
}

function Find-GrokBin {
    if (-not [string]::IsNullOrWhiteSpace($env:GROK_BRIDGE_GROK_BIN)) {
        $explicit = $env:GROK_BRIDGE_GROK_BIN
        # Leaf/file only — a directory path must not count as the grok binary.
        if (Test-Path -LiteralPath $explicit -PathType Leaf) {
            return (Resolve-Path -LiteralPath $explicit).Path
        }
        Write-GbWarn "GROK_BRIDGE_GROK_BIN=$explicit is not a file"
        return $null
    }
    foreach ($name in @('grok', 'grok.exe')) {
        $cmd = Get-Command $name -ErrorAction SilentlyContinue |
            Where-Object {
                $_.CommandType -eq 'Application' -and
                $_.Source -and
                (Test-Path -LiteralPath $_.Source -PathType Leaf)
            } |
            Select-Object -First 1
        if ($cmd) { return $cmd.Source }
    }
    return $null
}

function Show-SessionsNote([string]$HomeDir) {
    if ([string]::IsNullOrWhiteSpace($HomeDir)) {
        Write-GbWarn "Grok home could not be resolved (set GROK_BRIDGE_GROK_HOME or GROK_HOME)"
        return
    }
    Write-GbInfo "Grok home: $HomeDir"
    $sessions = Join-Path $HomeDir 'sessions'
    if (-not (Test-Path -LiteralPath $sessions)) {
        Write-GbWarn "sessions/ missing under $HomeDir — hub can still start for hub-native chats; Build list will be empty"
        return
    }
    $entries = @(Get-ChildItem -LiteralPath $sessions -Force -ErrorAction SilentlyContinue)
    if ($entries.Count -eq 0) {
        Write-GbWarn "sessions/ is empty under $HomeDir — hub-native chats still work; Build list will be empty"
    } else {
        Write-GbInfo "Found Build sessions under $sessions"
    }
}

function Show-AgentReminder {
    if ($env:GROK_BRIDGE_GROK_AGENT_WS -or $env:GROK_BRIDGE_GROK_AGENT_SECRET -or $env:GROK_BRIDGE_GROK_AGENT_AUTO_START -eq '1') {
        Write-GbInfo "Live Build send: ensure agent is up, e.g."
        Write-Host "    grok agent --always-approve --no-leader serve --bind 127.0.0.1:2419 --secret <token>"
        Write-Host "  or set GROK_BRIDGE_GROK_AGENT_WS / GROK_BRIDGE_GROK_AGENT_SECRET / GROK_BRIDGE_GROK_AGENT_AUTO_START"
    }
}

function Ensure-GrokBuild {
    if ($env:SKIP_GROK_BUILD_CHECK -eq '1') {
        Write-GbInfo "Skipping Grok Build check (SKIP_GROK_BUILD_CHECK=1)"
        return
    }

    Write-GbInfo "Checking Grok Build readiness"
    $bin = Find-GrokBin
    if ($bin) {
        Write-GbInfo "Found grok: $bin"
    } else {
        if ($env:REQUIRE_GROK_BUILD -eq '1') {
            Invoke-GbWaitForUser -Dep 'grok (Grok Build CLI)' -Steps @(
                'Install Grok Build from the vendor / product docs for Windows (no unofficial installer is bundled here).',
                'Ensure grok.exe is on PATH, or set GROK_BRIDGE_GROK_BIN to its full path.',
                'Optionally set GROK_BRIDGE_GROK_HOME (or GROK_HOME) if sessions are not under %USERPROFILE%\.grok.'
            ) -Rerun '.\start.ps1'
        }
        Write-GbWarn "grok not found on PATH (and GROK_BRIDGE_GROK_BIN unset)"
        Write-GbWarn "Continuing in hub-only mode (Build list/send unavailable). Set REQUIRE_GROK_BUILD=1 to fail hard."
        Write-GbWarn "To enable Build later: install Grok Build from vendor docs, then re-run .\start.ps1"
    }

    $home = Resolve-GrokHome
    Show-SessionsNote $home
    Show-AgentReminder
}

# When executed directly (not dot-sourced).
# InvocationName '.' is the classic `. .\file.ps1` signal on Windows PowerShell
# and PowerShell 7. Some hosts only record the dot operator on Line. `& .\file.ps1`
# is a call, not a dot-source, and must still run Ensure-GrokBuild.
$scriptDotSourced = ($MyInvocation.InvocationName -eq '.') -or ($MyInvocation.Line -match '^\s*\.(?=\s|$)')
if (-not $scriptDotSourced) {
    Ensure-GrokBuild
}
