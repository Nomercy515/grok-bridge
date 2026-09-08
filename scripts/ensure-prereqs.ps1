# Ensure Windows quick-start prerequisites (Git, Go ≥ 1.22).
# Dot-sourced by start.ps1. Bash twin: ensure-prereqs.sh
#
# Env:
#   SKIP_PREREQ_INSTALL=1 — check only; exit 1 if missing
#   GO_MIN_VERSION        — default 1.22
#   GROK_BRIDGE_INSTALL_WAIT=1 — optional pause when failing

$GoMinVersion = if ($env:GO_MIN_VERSION) { $env:GO_MIN_VERSION } else { '1.22' }

function Write-PrInfo([string]$Message) { Write-Host "==> $Message" }
function Write-PrWarn([string]$Message) { Write-Warning $Message }
function Write-PrErr([string]$Message) { Write-Host "error: $Message" -ForegroundColor Red }

function Invoke-PrWaitForUser {
    param(
        [Parameter(Mandatory = $true)][string]$Dep,
        [Parameter(Mandatory = $true)][string[]]$Steps,
        [string]$Rerun = '.\start.ps1',
        [int]$ExitCode = 2
    )
    Write-PrErr "missing $Dep"
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
    exit $ExitCode
}

function Test-GoVersionOk {
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) { return $false }
    try {
        $raw = & go env GOVERSION 2>$null
        if (-not $raw) { $raw = & go version 2>$null }
    } catch { return $false }
    if ($raw -match 'go([0-9]+)\.([0-9]+)') {
        $major = [int]$Matches[1]
        $minor = [int]$Matches[2]
        $minParts = $GoMinVersion.Split('.')
        $needMajor = [int]$minParts[0]
        $needMinor = if ($minParts.Count -gt 1) { [int]$minParts[1] } else { 0 }
        if ($major -gt $needMajor) { return $true }
        if ($major -eq $needMajor -and $minor -ge $needMinor) { return $true }
    }
    return $false
}

function Find-PackageManager {
    if (Get-Command winget -ErrorAction SilentlyContinue) { return 'winget' }
    if (Get-Command choco -ErrorAction SilentlyContinue) { return 'choco' }
    if (Get-Command scoop -ErrorAction SilentlyContinue) { return 'scoop' }
    return $null
}

function Install-WithPkgMgr {
    param([string]$Mgr, [string]$WingetId, [string]$ChocoId, [string]$ScoopId)
    switch ($Mgr) {
        'winget' {
            winget install --id $WingetId -e --accept-source-agreements --accept-package-agreements
            return $LASTEXITCODE -eq 0
        }
        'choco' {
            choco install $ChocoId -y
            return $LASTEXITCODE -eq 0
        }
        'scoop' {
            scoop install $ScoopId
            return $LASTEXITCODE -eq 0
        }
        default { return $false }
    }
}

function Ensure-Git {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        Write-PrInfo "Found git: $((Get-Command git).Source)"
        return
    }
    if ($env:SKIP_PREREQ_INSTALL -eq '1') {
        Write-PrWarn "git not found (SKIP_PREREQ_INSTALL=1) — continuing; go build may still work"
        return
    }
    $mgr = Find-PackageManager
    if ($mgr) {
        Write-PrInfo "Installing Git via $mgr"
        if (Install-WithPkgMgr -Mgr $mgr -WingetId 'Git.Git' -ChocoId 'git' -ScoopId 'git') {
            $env:Path = [System.Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
                         [System.Environment]::GetEnvironmentVariable('Path', 'User')
            if (Get-Command git -ErrorAction SilentlyContinue) {
                Write-PrInfo "Found git after install"
                return
            }
        }
    }
    Write-PrWarn "could not auto-install git — continuing; go build may still work"
    Write-Host "Manual: https://git-scm.com/download/win  or  winget install Git.Git"
}

function Ensure-Go {
    if (Test-GoVersionOk) {
        $ver = & go env GOVERSION 2>$null
        Write-PrInfo "Found Go $ver ($((Get-Command go).Source))"
        return
    }
    if (Get-Command go -ErrorAction SilentlyContinue) {
        Write-PrWarn "Go is older than $GoMinVersion"
    } else {
        Write-PrWarn "Go not found on PATH"
    }
    if ($env:SKIP_PREREQ_INSTALL -eq '1') {
        Invoke-PrWaitForUser -Dep "Go ≥ $GoMinVersion" -Steps @(
            "Install Go ≥ $GoMinVersion from https://go.dev/dl/",
            'Open a new terminal so go is on PATH'
        ) -ExitCode 1
    }
    $mgr = Find-PackageManager
    if ($mgr) {
        Write-PrInfo "Installing Go via $mgr"
        if (Install-WithPkgMgr -Mgr $mgr -WingetId 'GoLang.Go' -ChocoId 'golang' -ScoopId 'go') {
            $env:Path = [System.Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
                         [System.Environment]::GetEnvironmentVariable('Path', 'User')
            if (Test-GoVersionOk) {
                $ver = & go env GOVERSION 2>$null
                Write-PrInfo "Found Go $ver after install"
                return
            }
        }
    }
    Invoke-PrWaitForUser -Dep "Go ≥ $GoMinVersion" -Steps @(
        "Install Go ≥ $GoMinVersion from https://go.dev/dl/ (MSI) or:",
        '  winget install GoLang.Go',
        '  choco install golang -y',
        '  scoop install go',
        'Open a new PowerShell so go is on PATH'
    )
}

function Ensure-Prereqs {
    Ensure-Git
    Ensure-Go
}

# When executed directly (not dot-sourced). See ensure-grok-build.ps1 for the same check.
# `. .\file.ps1` sets InvocationName to '.' (or a Line that starts with the dot operator).
# `& .\file.ps1` is execution, not dot-source.
$scriptDotSourced = ($MyInvocation.InvocationName -eq '.') -or ($MyInvocation.Line -match '^\s*\.(?=\s|$)')
if (-not $scriptDotSourced) {
    Ensure-Prereqs
}
