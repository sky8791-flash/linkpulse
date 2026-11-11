[CmdletBinding()]
param(
    [switch]$NoBrowser
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$runDir = Join-Path $projectRoot '.run'
$logsDir = Join-Path $projectRoot 'logs'
$statePath = Join-Path $runDir 'server.json'
$envPath = Join-Path $projectRoot '.env'
$envExamplePath = Join-Path $projectRoot '.env.example'
$binaryPath = Join-Path $projectRoot 'bin\linkpulse.exe'
$postgresServiceName = 'postgresql-x64-18'

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

function Test-ProjectProcess {
    param(
        [int]$ProcessId,
        [string]$ExpectedPath
    )
    $process = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
    if (-not $process) {
        return $null
    }
    $actualPath = Get-ProcessPathSafe $process
    if (-not $actualPath) {
        return $null
    }
    try {
        if ([System.IO.Path]::GetFullPath($actualPath) -eq [System.IO.Path]::GetFullPath($ExpectedPath)) {
            return $process
        }
    } catch {
        return $null
    }
    return $null
}

function Import-DotEnv {
    param([string]$Path)
    foreach ($line in Get-Content -LiteralPath $Path -Encoding utf8) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) {
            continue
        }
        $separator = $trimmed.IndexOf('=')
        if ($separator -lt 1) {
            continue
        }
        $name = $trimmed.Substring(0, $separator).Trim()
        $value = $trimmed.Substring($separator + 1).Trim()
        if ($value.Length -ge 2) {
            $first = $value[0]
            $last = $value[$value.Length - 1]
            if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
                $value = $value.Substring(1, $value.Length - 2)
            }
        }
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}

function Wait-ForReady {
    param(
        [string]$Url,
        [System.Diagnostics.Process]$Process,
        [int]$TimeoutSeconds = 30
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($Process.HasExited) {
            return $false
        }
        try {
            $response = Invoke-WebRequest -Uri $Url -TimeoutSec 2 -UseBasicParsing
            if ($response.StatusCode -eq 200) {
                return $true
            }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    return $false
}

Set-Location -LiteralPath $projectRoot
New-Item -ItemType Directory -Path $runDir -Force | Out-Null
New-Item -ItemType Directory -Path $logsDir -Force | Out-Null

if (-not (Test-Path -LiteralPath $envPath)) {
    if (-not (Test-Path -LiteralPath $envExamplePath)) {
        throw "Missing .env and .env.example in $projectRoot"
    }
    Copy-Item -LiteralPath $envExamplePath -Destination $envPath
    Write-Step 'Created .env from .env.example.'
}
Import-DotEnv $envPath

$httpAddress = if ($env:HTTP_ADDR) { $env:HTTP_ADDR } else { ':8080' }
$port = 8080
if ($httpAddress -match ':(\d+)$') {
    $port = [int]$Matches[1]
}
$readyUrl = "http://127.0.0.1:$port/readyz"
$baseUrl = if ($env:BASE_URL) { $env:BASE_URL.TrimEnd('/') } else { "http://localhost:$port" }

if (Test-Path -LiteralPath $statePath) {
    try {
        $state = Get-Content -LiteralPath $statePath -Raw -Encoding utf8 | ConvertFrom-Json
        $existing = Test-ProjectProcess -ProcessId $state.pid -ExpectedPath $binaryPath
        if ($existing) {
            if (Wait-ForReady -Url $readyUrl -Process $existing -TimeoutSeconds 8) {
                Write-Step "LinkPulse is already running. PID=$($existing.Id)"
                Write-Host "Open: $baseUrl" -ForegroundColor Green
                if (-not $NoBrowser) {
                    Start-Process $baseUrl
                }
                exit 0
            }
            throw "A LinkPulse process is running (PID=$($existing.Id)) but is not ready."
        }
    } catch {
        Write-Warning "Ignoring stale runtime state: $($_.Exception.Message)"
    }
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
}

$listener = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
    $ownerPath = if ($owner) { Get-ProcessPathSafe $owner } else { $null }
    throw "Port $port is already in use by PID=$($listener.OwningProcess) ($ownerPath). Change HTTP_ADDR in .env or stop that process."
}

$databaseUrl = [string]$env:DATABASE_URL
if ($databaseUrl -match '://[^@]+@(?:localhost|127\.0\.0\.1)(?::(\d+))?/') {
    $service = Get-Service -Name $postgresServiceName -ErrorAction SilentlyContinue
    if ($service -and $service.Status -ne 'Running') {
        Write-Step "Starting PostgreSQL service $postgresServiceName..."
        try {
            Start-Service -Name $postgresServiceName
            $service.WaitForStatus('Running', [TimeSpan]::FromSeconds(20))
        } catch {
            throw "PostgreSQL is not running and the service could not be started. Run this script as Administrator or start '$postgresServiceName' manually. $($_.Exception.Message)"
        }
    }
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go was not found in PATH.'
}

Write-Step 'Building server...'
& go build -trimpath -o $binaryPath .\cmd\server
if ($LASTEXITCODE -ne 0) {
    throw "go build failed with exit code $LASTEXITCODE"
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$stdoutLog = Join-Path $logsDir "server-$stamp.log"
$stderrLog = Join-Path $logsDir "server-$stamp.err.log"

Write-Step 'Starting LinkPulse in the background...'
$process = Start-Process `
    -FilePath $binaryPath `
    -WorkingDirectory $projectRoot `
    -RedirectStandardOutput $stdoutLog `
    -RedirectStandardError $stderrLog `
    -WindowStyle Hidden `
    -PassThru

$state = [ordered]@{
    pid = $process.Id
    executable = $binaryPath
    startedAt = (Get-Date).ToString('o')
    baseUrl = $baseUrl
    stdoutLog = $stdoutLog
    stderrLog = $stderrLog
}
$state | ConvertTo-Json | Set-Content -LiteralPath $statePath -Encoding utf8

if (-not (Wait-ForReady -Url $readyUrl -Process $process -TimeoutSeconds 30)) {
    $errors = @()
    if (Test-Path -LiteralPath $stderrLog) {
        $errors += Get-Content -LiteralPath $stderrLog -Tail 20 -Encoding utf8
    }
    if (Test-Path -LiteralPath $stdoutLog) {
        $errors += Get-Content -LiteralPath $stdoutLog -Tail 20 -Encoding utf8
    }
    if (-not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    $detail = ($errors | Where-Object { $_ } | Select-Object -Last 20) -join [Environment]::NewLine
    throw "LinkPulse did not become ready. Logs: $stdoutLog`n$detail"
}

Write-Host ''
Write-Host 'LinkPulse is running.' -ForegroundColor Green
Write-Host "URL:     $baseUrl"
Write-Host "PID:     $($process.Id)"
Write-Host "API Key: $env:API_KEY"
Write-Host "Log:     $stdoutLog"
Write-Host ''
Write-Host 'Use stop.bat to stop the service.'

if (-not $NoBrowser) {
    Start-Process $baseUrl
}
