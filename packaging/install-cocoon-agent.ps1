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

# 3. create or update the service
$binPath = "`"$BinaryDest`" serve --port $Port"
if ($existing) {
    Write-Step "updating service $ServiceName binPath"
    & sc.exe config $ServiceName binPath= "$binPath" start= auto | Out-Null
} else {
    Write-Step "creating service $ServiceName"
    & sc.exe create $ServiceName binPath= "$binPath" start= auto DisplayName= "$DisplayName" | Out-Null
    & sc.exe description $ServiceName "$Description" | Out-Null
    # 24h reset window mirrors Linux's Restart=always intent — a flapping
    # bug self-heals instead of permanently bricking the agent.
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
}

# 4. start
Write-Step "starting $ServiceName"
Start-Service -Name $ServiceName

# 5. verify
$svc = Get-Service -Name $ServiceName
Write-Host "Service status: $($svc.Status)"
if ($svc.Status -ne 'Running') {
    throw "service $ServiceName did not reach Running state (got $($svc.Status))"
}

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
