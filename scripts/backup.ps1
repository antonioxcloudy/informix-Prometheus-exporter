[CmdletBinding()]
param(
    [string]$OutputDirectory = "./backups",
    [switch]$IncludePrometheus,
    [ValidateRange(1, 365)]
    [int]$Retain = 7
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousLocation = Get-Location
$projectName = "informix-monitoring"
$toolImage = "busybox:1.37.0"
$stoppedServices = @()
$backupPath = $null
$backupCompleted = $false

function Invoke-Docker {
    param([string[]]$Arguments)
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed with exit code $LASTEXITCODE." }
}

function Get-VolumeName {
    param([string]$LogicalName)
    $names = @(& docker volume ls --filter "label=com.docker.compose.project=$projectName" --filter "label=com.docker.compose.volume=$LogicalName" --format '{{.Name}}')
    if ($LASTEXITCODE -ne 0 -or $names.Count -ne 1) {
        throw "Expected one Docker volume for $LogicalName, found $($names.Count). Is the stack installed?"
    }
    return $names[0]
}

function Test-ServiceRunning {
    param([string]$Service)
    $container = (& docker compose -f compose.yaml ps -q $Service).Trim()
    if (-not $container) { return $false }
    return ((& docker inspect --format '{{.State.Running}}' $container).Trim() -eq "true")
}

try {
    Set-Location -LiteralPath $repoRoot
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "Docker is not available in PATH." }
    Invoke-Docker -Arguments @("compose", "version")

    $outputRoot = [IO.Path]::GetFullPath((Join-Path $repoRoot $OutputDirectory))
    New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
    $backupPath = Join-Path $outputRoot ([DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ"))
    New-Item -ItemType Directory -Path (Join-Path $backupPath "volumes") | Out-Null

    $services = @("grafana", "alertmanager")
    $volumes = @("grafana_data", "alertmanager_data")
    if ($IncludePrometheus) {
        $services += "prometheus"
        $volumes += "prometheus_data"
    }
    foreach ($service in $services) {
        if (Test-ServiceRunning $service) { $stoppedServices += $service }
    }
    if ($stoppedServices.Count -gt 0) {
        Write-Host "Stopping $($stoppedServices -join ', ') briefly for a consistent backup..."
        Invoke-Docker -Arguments (@("compose", "-f", "compose.yaml", "stop") + $stoppedServices)
    }

    $volumeManifest = @()
    foreach ($logicalName in $volumes) {
        $physicalName = Get-VolumeName $logicalName
        $archiveName = "$logicalName.tar.gz"
        Write-Host "Backing up volume $logicalName..."
        Invoke-Docker -Arguments @("run", "--rm", "-v", "${physicalName}:/source:ro", "-v", "${backupPath}:/backup", $toolImage, "tar", "-czf", "/backup/volumes/$archiveName", "-C", "/source", ".")
        $volumeManifest += [ordered]@{ logical_name = $logicalName; docker_volume = $physicalName; archive = "volumes/$archiveName" }
    }

    Write-Warning "The configuration archive contains .env, ODBC configuration and local secrets. Store it securely."
    Invoke-Docker -Arguments @("run", "--rm", "-v", "${repoRoot}:/source:ro", "-v", "${backupPath}:/backup", $toolImage, "tar", "-czf", "/backup/config.tar.gz", "-C", "/source", ".env", "deploy/config", "deploy/secrets")

    $manifest = [ordered]@{
        format_version = 1
        created_at_utc = [DateTime]::UtcNow.ToString("o")
        compose_project = $projectName
        includes_prometheus = [bool]$IncludePrometheus
        configuration_archive = "config.tar.gz"
        volumes = $volumeManifest
    }
    $manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $backupPath "manifest.json") -Encoding utf8

    $checksumLines = Get-ChildItem -LiteralPath $backupPath -Recurse -File |
        Where-Object Name -ne "checksums.sha256" |
        Sort-Object FullName |
        ForEach-Object {
            $relative = $_.FullName.Substring($backupPath.Length).TrimStart('\', '/').Replace('\', '/')
            "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant(), $relative
        }
    $checksumLines | Set-Content -LiteralPath (Join-Path $backupPath "checksums.sha256") -Encoding ascii

    $oldBackups = Get-ChildItem -LiteralPath $outputRoot -Directory |
        Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName "manifest.json") } |
        Sort-Object Name -Descending |
        Select-Object -Skip $Retain
    foreach ($oldBackup in $oldBackups) {
        $resolved = [IO.Path]::GetFullPath($oldBackup.FullName)
        if (-not $resolved.StartsWith($outputRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to prune path outside backup directory: $resolved"
        }
        Write-Host "Removing expired backup $($oldBackup.Name)..."
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }

    $backupCompleted = $true
    Write-Host "Backup completed: $backupPath"
} finally {
    if ($stoppedServices.Count -gt 0) {
        Write-Host "Restarting $($stoppedServices -join ', ')..."
        & docker compose -f compose.yaml start @stoppedServices
    }
    if (-not $backupCompleted -and $backupPath -and (Test-Path -LiteralPath $backupPath)) {
        $resolvedBackup = [IO.Path]::GetFullPath($backupPath)
        $resolvedRoot = [IO.Path]::GetFullPath((Split-Path -Parent $resolvedBackup))
        if ($resolvedBackup.StartsWith($resolvedRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            Write-Host "Removing incomplete backup $resolvedBackup..."
            Remove-Item -LiteralPath $resolvedBackup -Recurse -Force
        }
    }
    Set-Location -LiteralPath $previousLocation
}
