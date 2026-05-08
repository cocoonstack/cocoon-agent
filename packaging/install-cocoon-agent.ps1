# Install cocoon-agent.exe as a Windows service.
#
# Run elevated. Idempotent — safe to re-run for upgrades; the existing
# service is stopped, the binary replaced, and the service restarted.
#
# Usage:
#   .\install-cocoon-agent.ps1                 # default install
#   .\install-cocoon-agent.ps1 -Port 1024      # explicit vsock port
#
# Trust model matches the Linux unit: vsock is host-local and the agent
# runs commands on behalf of host-side callers, so it runs as LocalSystem
# (equivalent to root). Privilege drop would defeat its purpose.

[CmdletBinding()]
param(
    [string]$BinarySource = "$PSScriptRoot\cocoon-agent.exe",
    [string]$InstallDir   = "$env:ProgramFiles\Cocoon",
    [uint32]$Port         = 1024,
    [string]$ServiceName  = "cocoon-agent",
    [string]$DisplayName  = "Cocoon Agent",
    [string]$Description  = "vsock-based command exec agent for Cocoon-managed VMs"
)

$ErrorActionPreference = 'Stop'

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

if (-not (Test-Path $BinarySource)) {
    throw "binary not found at $BinarySource"
}

# 1. stop existing service if present so the .exe is not in use
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing -and $existing.Status -ne 'Stopped') {
    Write-Step "stopping existing $ServiceName"
    Stop-Service -Name $ServiceName -Force
    # Stop-Service can return before the process actually exits, so wait
    # for SCM to confirm; bounded so a wedged SCM can't hang the installer.
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Service -Name $ServiceName).Status -ne 'Stopped') {
        if ((Get-Date) -ge $deadline) {
            throw "service $ServiceName did not stop within 30s; SCM may be wedged"
        }
        Start-Sleep -Milliseconds 200
    }
}

# 2. install / refresh the binary
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}
$BinaryDest = Join-Path $InstallDir 'cocoon-agent.exe'
Write-Step "copying binary to $BinaryDest"
Copy-Item -Path $BinarySource -Destination $BinaryDest -Force

# 3. create or update the service.
#
# Stage the sc.exe call in a temp .bat. cmd.exe parses the file's literal
# `key= "\"path\"..."` exactly the way sc.exe needs (the quoted-with-
# embedded-quotes binPath survives as a single arg). Calling sc.exe via
# PowerShell directly mangles the `key= value` token; calling cmd.exe /c
# with the same string also mangles it because cmd's command-line quote
# stripping rules differ between argv and "/c <string>". A .bat file is
# the boring path that all three of those parsers handle the same way.
$binPath = "`"$BinaryDest`" serve --port $Port"
function Invoke-Bat {
    param([string]$Body)
    $bat = Join-Path $env:TEMP ("cocoon-svc-{0:N}.bat" -f [guid]::NewGuid())
    Set-Content -Path $bat -Value "@echo off`r`n$Body" -Encoding ASCII -Force
    try {
        & cmd.exe /c $bat | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "sc.exe call failed (rc=$LASTEXITCODE) for: $Body" }
    } finally {
        Remove-Item -LiteralPath $bat -Force -ErrorAction SilentlyContinue
    }
}

if ($existing) {
    Write-Step "updating service $ServiceName binPath"
    Invoke-Bat ('sc.exe config {0} binPath= "\"{1}\" serve --port {2}" start= auto' -f $ServiceName, $BinaryDest, $Port)
} else {
    Write-Step "creating service $ServiceName"
    Invoke-Bat ('sc.exe create {0} binPath= "\"{1}\" serve --port {2}" start= auto DisplayName= "{3}"' -f $ServiceName, $BinaryDest, $Port, $DisplayName)
    Invoke-Bat ('sc.exe description {0} "{1}"' -f $ServiceName, $Description)
    # 24h reset window mirrors Linux's Restart=always intent — a flapping
    # bug self-heals instead of permanently bricking the agent.
    Invoke-Bat ('sc.exe failure {0} reset= 86400 actions= restart/5000/restart/5000/restart/5000' -f $ServiceName)
}

# Confirm registration before attempting Start-Service; otherwise an SCM hiccup
# in step 3 would surface here as a misleading "service not found" error.
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -eq $svc) {
    throw "service $ServiceName did not register; check that you ran elevated"
}

# 4. start. Tolerate Start-Service failure when the viosock device is absent —
# image-build VMs typically have no vsock device, so the agent can't listen yet.
# The service is registered with start=auto, so production CH (which exposes
# the vsock device) will start it automatically on boot.
Write-Step "starting $ServiceName"
try {
    Start-Service -Name $ServiceName -ErrorAction Stop
} catch {
    Write-Warning "Start-Service failed (typical when no vsock device is present in the build VM; production CH provides one): $_"
}

# 5. verify
$svc = Get-Service -Name $ServiceName
Write-Host "Service status: $($svc.Status)"

# Best-effort sanity check: the service is up and viosock is loaded.
$viosock = Get-PnpDevice -PresentOnly:$true -Class "System" -ErrorAction SilentlyContinue |
    Where-Object { $_.FriendlyName -like '*VirtIO Socket*' -or $_.InstanceId -like '*VEN_1AF4&DEV_1053*' }
if ($null -eq $viosock) {
    Write-Warning "viosock device not detected. Agent will start but cannot listen on vsock without the viosock driver (virtio-win >= 0.1.285)."
} else {
    Write-Host "viosock device present: $($viosock.FriendlyName)"
}

Write-Host ""
Write-Host "cocoon-agent installed and running on vsock port $Port" -ForegroundColor Green
Write-Host "status: Get-Service $ServiceName"
Write-Host "stop:   Stop-Service $ServiceName"
