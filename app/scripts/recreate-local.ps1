<#
.SYNOPSIS
    Rebuild the local odds image and replace the running container.

.DESCRIPTION
    `docker restart` reuses a container's original image and environment, so a
    change such as GODEBUG=tlssecpmlkem=0 only reaches the process in a new
    container. This script builds, stops the old container and renames it (a
    stopped container stays down under "unless-stopped" and is still there to
    restore), starts a fresh one, and then checks the two things that decide
    whether the fix is actually in effect: the container's own environment
    carries the setting, and /healthz answers with per-book health.

    That setting is what makes the app's REST client acceptable to one book's
    edge from inside a Linux container; without it the listing answers HTTP 403
    with an HTML block page and the book never seeds. See README §13 and
    research/BETMGM_DOCKER_DIAGNOSIS.md.

.PARAMETER Image
    Image tag to build and run. Default: betstamp-odds:local.

.PARAMETER Name
    Container name to replace. Default: betstamp-odds-local.

.PARAMETER Port
    Host port forwarded to the container's 8080. Default: 8080.

.PARAMETER NoBuild
    Reuse the existing image instead of building it. The image is still checked
    for the setting before the running container is touched.

.EXAMPLE
    powershell -File app\scripts\recreate-local.ps1

.EXAMPLE
    powershell -File app\scripts\recreate-local.ps1 -Port 8081 -Name betstamp-odds-check -NoBuild

.NOTES
    Works on Windows PowerShell 5.1 and PowerShell 7. If the execution policy
    refuses local scripts, run it as:
        powershell -ExecutionPolicy Bypass -File app\scripts\recreate-local.ps1
#>
[CmdletBinding()]
param(
    [string]$Image = 'betstamp-odds:local',
    [string]$Name = 'betstamp-odds-local',
    [int]$Port = 8080,
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'

$required = 'tlssecpmlkem=0'
$appRoot = Split-Path -Parent $PSScriptRoot
$healthURL = "http://localhost:$Port/healthz"

function Invoke-Docker {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    $output = & docker @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($ArgumentList -join ' ') exited $LASTEXITCODE"
    }
    return $output
}

# Takes the JSON that `docker inspect --format '{{json .Config.Env}}'` prints.
# GODEBUG may carry several settings ("a=1,tlssecpmlkem=0"), so match on the
# value rather than on an exact string.
function Test-GodebugSetting {
    param([string]$EnvironmentJSON)

    $entries = $EnvironmentJSON | ConvertFrom-Json
    foreach ($entry in $entries) {
        if ($entry -like 'GODEBUG=*' -and $entry.Contains($required)) { return $true }
    }
    return $false
}

try {
    $null = Invoke-Docker @('version', '--format', '{{.Server.Version}}')
}
catch {
    throw "The Docker engine is not reachable: $($_.Exception.Message)`nStart Docker Desktop and run this again."
}

if (-not $NoBuild) {
    Write-Host "building $Image from $appRoot"
    & docker build --build-arg CMD=server -t $Image $appRoot
    if ($LASTEXITCODE -ne 0) { throw "docker build exited $LASTEXITCODE" }
}

# Check the image before stopping anything: a rebuild that silently lost the
# ENV line would otherwise replace a working container with a failing one.
try {
    $imageEnv = (Invoke-Docker @('image', 'inspect', '--format', '{{json .Config.Env}}', $Image)) -join ''
}
catch {
    throw "No image $Image ($($_.Exception.Message)). Run without -NoBuild to build it."
}
if (-not (Test-GodebugSetting $imageEnv)) {
    throw "Image $Image does not set GODEBUG=$required. Rebuild without -NoBuild; as it is, the container's REST reads would be refused with HTTP 403."
}

$existing = (Invoke-Docker @('ps', '-aq', '-f', ('name=^/{0}$' -f $Name))) -join ''
$backup = $null
if ($existing) {
    $backup = '{0}.before-{1}' -f $Name, (Get-Date -Format 'yyyyMMdd-HHmmss')
    Write-Host "stopping $Name; keeping it as $backup"
    $null = Invoke-Docker @('stop', '-t', '20', $Name)
    $null = Invoke-Docker @('rename', $Name, $backup)
}

Write-Host "starting $Name on port $Port"
$null = Invoke-Docker @('run', '-d', '--name', $Name, '--restart', 'unless-stopped', '-p', "${Port}:8080", $Image)

$inspect = (Invoke-Docker @('inspect', '--format', '{{.State.Status}} {{json .Config.Env}}', $Name)) -join ''
$status, $containerEnv = $inspect -split ' ', 2
if ($status -ne 'running' -or -not (Test-GodebugSetting $containerEnv)) {
    & docker logs --tail 20 $Name
    throw "Container $Name is '$status' and its environment does not carry GODEBUG=$required."
}

# /healthz is 200 as soon as it serves; the body is the per-book state.
$deadline = (Get-Date).AddSeconds(40)
$health = $null
while (-not $health -and (Get-Date) -lt $deadline) {
    try {
        $health = Invoke-RestMethod -Uri $healthURL -TimeoutSec 3
    }
    catch {
        Start-Sleep -Milliseconds 500
    }
}
if (-not $health) {
    & docker logs --tail 30 $Name
    if ($backup) {
        Write-Host "roll back:  docker rm -f $Name; docker rename $backup $Name; docker start $Name"
    }
    throw "No answer from $healthURL within 40 s."
}

Write-Host "`n${Name} is running, and $healthURL answered:"
$rows = foreach ($p in $health.books.PSObject.Properties) {
    [pscustomobject]@{
        book      = $p.Name
        state     = $p.Value.state
        lastError = $p.Value.lastError
    }
}
$rows | Format-Table -AutoSize | Out-Host

if ($backup) {
    Write-Host "previous container kept as $backup (stopped)"
    Write-Host "roll back:  docker rm -f $Name; docker rename $backup $Name; docker start $Name"
}
