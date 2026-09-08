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

# install.ps1 defines this first; do not replace that version when dot-sourced.
if (-not (Get-Command -Name Update-SessionPath -CommandType Function -ErrorAction SilentlyContinue)) {
function Update-SessionPath {
    param([string[]]$PrependDirs = @())

    # Prepend newly discovered dirs (Tailscale, caller extras) onto the current
    # process PATH, then merge Machine+User. Never discard $env:Path — a
    # session-only go (profile, zip, mise) must stay findable.

    $ordered = New-Object System.Collections.Generic.List[string]
    foreach ($dir in $PrependDirs) {
        if ($dir) { [void]$ordered.Add($dir) }
    }

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
}

function Test-ToolPresent {
    param([Parameter(Mandatory = $true)][string]$Name)

    if (Get-Command $Name -ErrorAction SilentlyContinue) { return $true }
    if ($Name -notmatch '\.exe$' -and (Get-Command "$Name.exe" -ErrorAction SilentlyContinue)) {
        return $true
    }

    # Get-Command can miss a binary added to PATH after an earlier miss in this
    # session. Probe the refreshed PATH so an already-installed tool still counts.
    $leaf = if ($Name -match '\.(exe|cmd|bat)$') { $Name } else { "$Name.exe" }
    foreach ($dir in ($env:Path -split ';')) {
        if ([string]::IsNullOrWhiteSpace($dir)) { continue }
        if (Test-Path -LiteralPath (Join-Path $dir.Trim() $leaf)) { return $true }
    }
    return $false
}

function Install-WithPkgMgr {
    param(
        [string]$Mgr,
        [string]$WingetId,
        [string]$ChocoId,
        [string]$ScoopId,
        [string]$CommandName
    )

    $code = 1
    switch ($Mgr) {
        'winget' {
            winget install --id $WingetId -e --accept-source-agreements --accept-package-agreements
            $code = $LASTEXITCODE
        }
        'choco' {
            choco install $ChocoId -y
            $code = $LASTEXITCODE
        }
        'scoop' {
            scoop install $ScoopId
            $code = $LASTEXITCODE
        }
        default { return $false }
    }
    if ($null -eq $code) { $code = 0 }
    if ($code -eq 0) { return $true }

    # winget/choco often exit non-zero when the package is already installed.
    # Same pattern as install.ps1 Tailscale: refresh PATH and re-check the tool
    # before treating that as a hard failure (so start.ps1 does not exit 2).
    if ($Mgr -in @('winget', 'choco', 'scoop')) {
        Update-SessionPath
        if ($CommandName -and (Test-ToolPresent -Name $CommandName)) {
            return $true
        }
    }
    return $false
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
        # Always refresh after the attempt. winget non-zero (already installed)
        # must not skip the re-probe, and must not wipe a session-only PATH.
        [void](Install-WithPkgMgr -Mgr $mgr -WingetId 'Git.Git' -ChocoId 'git' -ScoopId 'git' -CommandName 'git')
        Update-SessionPath
        if (Get-Command git -ErrorAction SilentlyContinue) {
            Write-PrInfo "Found git after install: $((Get-Command git).Source)"
            return
        }
        if (Test-ToolPresent -Name 'git') {
            Write-PrInfo "Found git after install"
            return
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
        # Always refresh after the attempt. winget/choco non-zero when Go is
        # already installed used to skip this re-probe and start.ps1 exited 2.
        [void](Install-WithPkgMgr -Mgr $mgr -WingetId 'GoLang.Go' -ChocoId 'golang' -ScoopId 'go' -CommandName 'go')
        Update-SessionPath
        if (Get-Command go -ErrorAction SilentlyContinue) {
            if (Test-GoVersionOk) {
                $ver = & go env GOVERSION 2>$null
                Write-PrInfo "Found Go $ver after install"
                return
            }
            Write-PrInfo "Found go after install: $((Get-Command go).Source)"
            return
        }
        if (Test-ToolPresent -Name 'go') {
            Write-PrInfo "Found go after install"
            return
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
