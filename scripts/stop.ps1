[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$runDir = Join-Path $projectRoot '.run'
$statePath = Join-Path $runDir 'server.json'
$binaryPath = Join-Path $projectRoot 'bin\linkpulse.exe'

function Write-Step {
    param([string]$Message)
    Write-Host "[LinkPulse] $Message" -ForegroundColor Cyan
}

function Get-ProcessPathSafe {
    param([System.Diagnostics.Process]$Process)
    try {
        return $Process.Path
    } catch {
        return $null
    }
}

Set-Location -LiteralPath $projectRoot

if (-not (Test-Path -LiteralPath $statePath)) {
    Write-Host 'LinkPulse is not running: no runtime state file was found.' -ForegroundColor Yellow
    exit 0
}

try {
    $state = Get-Content -LiteralPath $statePath -Raw -Encoding utf8 | ConvertFrom-Json
} catch {
    throw "Runtime state is invalid: $statePath. $($_.Exception.Message)"
}

$process = Get-Process -Id $state.pid -ErrorAction SilentlyContinue
if (-not $process) {
    Remove-Item -LiteralPath $statePath -Force
    Write-Host "LinkPulse is not running. Removed stale state for PID $($state.pid)." -ForegroundColor Yellow
    exit 0
}

$actualPath = Get-ProcessPathSafe $process
$pathMatches = $false
if ($actualPath) {
    try {
        $pathMatches = [System.IO.Path]::GetFullPath($actualPath) -eq [System.IO.Path]::GetFullPath($binaryPath)
    } catch {
        $pathMatches = $false
    }
}

if (-not $pathMatches) {
    throw "PID $($state.pid) does not belong to $binaryPath. It was not stopped. Remove stale state manually after verification: $statePath"
}

Write-Step 'Waiting up to 2 seconds for pending click batches to flush...'
Start-Sleep -Seconds 2

Write-Step "Stopping LinkPulse PID=$($process.Id)..."
Stop-Process -Id $process.Id -Force
$process.WaitForExit(5000) | Out-Null

if (-not $process.HasExited) {
    throw "Process $($process.Id) did not exit within 5 seconds."
}

Remove-Item -LiteralPath $statePath -Force
Write-Host 'LinkPulse stopped.' -ForegroundColor Green
