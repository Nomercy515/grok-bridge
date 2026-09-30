# Grok Bridge native Windows quick-start (mirrors start.sh).
# Does NOT require WSL. Builds bin\grok-bridge.exe and runs the hub.
#
# Usage:
#   .\start.ps1
#   $env:SKIP_GROK_BUILD_CHECK=1; .\start.ps1
#   $env:REQUIRE_GROK_BUILD=1; .\start.ps1
#
# See docs/NATIVE_HOSTS.md

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

. "$Root\scripts\ensure-prereqs.ps1"
Ensure-Prereqs

. "$Root\scripts\ensure-grok-build.ps1"
Ensure-GrokBuild

New-Item -ItemType Directory -Force -Path (Join-Path $Root 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Root 'data') | Out-Null

$exe = Join-Path $Root 'bin\grok-bridge.exe'
$needBuild = -not (Test-Path -LiteralPath $exe)
if (-not $needBuild) {
    $exeTime = (Get-Item -LiteralPath $exe).LastWriteTimeUtc
    $sources = Get-ChildItem -Path $Root -Recurse -Include *.go,go.mod,go.sum -File -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notmatch '\\data\\|\\bin\\|\\\.git\\' }
    foreach ($f in $sources) {
        if ($f.LastWriteTimeUtc -gt $exeTime) {
            $needBuild = $true
            break
        }
    }
}

if ($needBuild) {
    # Native Windows: do not force GOOS/GOARCH. Go defaults to the host OS/arch.
    # A pre-set GOOS/GOARCH in this session is left unchanged (so a leftover
    # cross-compile setting is not cleared or rewritten) and will affect
    # `go build` — unset them in this shell first if you need a native exe.
    Write-Host '==> Building grok-bridge.exe'
    & go build -o $exe ./cmd/grok-bridge
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

if (-not $env:GROK_BRIDGE_DATA) {
    $env:GROK_BRIDGE_DATA = Join-Path $Root 'data'
}
New-Item -ItemType Directory -Force -Path $env:GROK_BRIDGE_DATA | Out-Null

$hostBind = if ($env:GROK_BRIDGE_HOST) { $env:GROK_BRIDGE_HOST } else { '127.0.0.1' }
$port = if ($env:GROK_BRIDGE_PORT) { $env:GROK_BRIDGE_PORT } else { '4020' }
$scheme = 'http'
$sslArgs = @()
if ($env:GROK_BRIDGE_SSL_CERT -and $env:GROK_BRIDGE_SSL_KEY) {
    $scheme = 'https'
    $sslArgs = @('--ssl-cert', $env:GROK_BRIDGE_SSL_CERT, '--ssl-key', $env:GROK_BRIDGE_SSL_KEY)
}

Write-Host "Starting Grok Bridge on ${scheme}://${hostBind}:${port}  (demo: ${scheme}://127.0.0.1:${port}/?demo=1)"
if ($hostBind -eq '0.0.0.0' -or $hostBind -eq '::') {
    Write-Host "Note: bound on all interfaces — use TLS + trusted LAN or Tailscale; do not port-forward :${port}."
}

$argList = @('--host', $hostBind, '--port', $port, '--data-dir', $env:GROK_BRIDGE_DATA) + $sslArgs
& $exe @argList
exit $LASTEXITCODE
