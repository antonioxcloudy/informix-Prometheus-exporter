[CmdletBinding()]
param(
    [switch]$IncludePrometheusBackup,
    [switch]$RequireInformix,
    [switch]$Webhook,
    [ValidateRange(1, 365)]
    [int]$RetainBackups = 7
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousLocation = Get-Location
$rollbackImages = @()
$mutationStarted = $false

function Invoke-Docker {
    param([string[]]$Arguments)
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed with exit code $LASTEXITCODE." }
}

function Get-ComposeArguments {
    $arguments = @("compose", "-f", "compose.yaml")
    if (Select-String -Path "deploy/config/odbc.ini" -Pattern '^\s*PWD_FILE\s*=' -Quiet) { $arguments += @("-f", "compose.secrets.yaml") }
    if ($Webhook) { $arguments += @("-f", "compose.webhook.yaml") }
    return $arguments
}

try {
    Set-Location -LiteralPath $repoRoot
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "Docker is not available in PATH." }
    $compose = @(Get-ComposeArguments)

    Write-Host "Running configuration preflight..."
    Invoke-Docker -Arguments ($compose + @("config", "--quiet"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "--no-deps", "--entrypoint", "promtool", "prometheus", "check", "config", "/etc/prometheus/prometheus.yml"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "--no-deps", "--entrypoint", "amtool", "alertmanager", "check-config", "/etc/alertmanager/alertmanager.yml"))

    & (Join-Path $PSScriptRoot "backup.ps1") -IncludePrometheus:$IncludePrometheusBackup -Retain $RetainBackups
    if ($LASTEXITCODE -ne 0) { throw "Pre-update backup failed." }

    foreach ($service in @("informix-exporter", "prometheus", "alertmanager", "grafana")) {
        $container = (& docker compose -f compose.yaml ps -q $service).Trim()
        if ($container) {
            $imageId = (& docker inspect --format '{{.Image}}' $container).Trim()
            $rollbackReference = "local/informix-monitoring-rollback:$($service.Replace('_', '-'))"
            Invoke-Docker -Arguments @("image", "tag", $imageId, $rollbackReference)
            $rollbackImages += [ordered]@{
                service = $service
                reference = (& docker inspect --format '{{.Config.Image}}' $container).Trim()
                image_id = $imageId
                rollback_reference = $rollbackReference
            }
        }
    }
    $rollbackFile = Join-Path $repoRoot "backups/last-update-images.json"
    $rollbackImages | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $rollbackFile -Encoding utf8

    $mutationStarted = $true
    Write-Host "Pulling service images and rebuilding the exporter..."
    Invoke-Docker -Arguments ($compose + @("pull", "--ignore-buildable"))
    Invoke-Docker -Arguments ($compose + @("build", "--pull", "informix-exporter"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "informix-exporter", "--configfile=/etc/informix-exporter/config.yaml", "--check-config"))
    Invoke-Docker -Arguments ($compose + @("up", "-d", "--wait", "--wait-timeout", "90"))

    $network = "informix-monitoring_monitoring"
    $exporterEndpoint = if ($RequireInformix) { "http://informix-exporter:8080/readyz" } else { "http://informix-exporter:8080/healthz" }
    foreach ($endpoint in @(
        $exporterEndpoint,
        "http://prometheus:9090/-/ready",
        "http://alertmanager:9093/-/ready",
        "http://grafana:3000/api/health"
    )) {
        $ready = $false
        for ($attempt = 1; $attempt -le 12; $attempt++) {
            & docker run --rm --network $network busybox:1.37.0 wget -q --spider $endpoint
            if ($LASTEXITCODE -eq 0) { $ready = $true; break }
            if ($attempt -lt 12) { Start-Sleep -Seconds 5 }
        }
        if (-not $ready) { throw "Endpoint did not become ready: $endpoint" }
    }
    if (-not $RequireInformix) {
        & docker run --rm --network $network busybox:1.37.0 wget -q --spider http://informix-exporter:8080/readyz
        if ($LASTEXITCODE -ne 0) { Write-Warning "The stack is healthy, but Informix is not currently ready. Run update.ps1 -RequireInformix or the exporter --check diagnostic for a strict database check." }
    }
    Invoke-Docker -Arguments ($compose + @("ps"))
    Write-Host "Update completed successfully. The pre-update backup is under backups/."
} catch {
    if ($mutationStarted -and $rollbackImages.Count -gt 0) {
        Write-Warning "Update failed; restoring the previous image references..."
        try {
            foreach ($image in $rollbackImages) { Invoke-Docker -Arguments @("image", "tag", $image.rollback_reference, $image.reference) }
            $compose = @(Get-ComposeArguments)
            Invoke-Docker -Arguments ($compose + @("up", "-d", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "90"))
            Write-Warning "Previous images restored. Persistent data was not modified."
        } catch {
            Write-Warning "Automatic image rollback also failed. Use backups/last-update-images.json and the latest backup to recover."
        }
    }
    throw
} finally {
    Set-Location -LiteralPath $previousLocation
}
