[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Backup,
    [switch]$ValidateOnly,
    [switch]$RestoreConfig,
    [switch]$Start,
    [switch]$Webhook,
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousLocation = Get-Location
$projectName = "informix-monitoring"
$toolImage = "busybox:1.37.0"
$allowedVolumes = @("grafana_data", "alertmanager_data", "prometheus_data")

function Invoke-Docker {
    param([string[]]$Arguments)
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed with exit code $LASTEXITCODE." }
}

function Get-VolumeName {
    param([string]$LogicalName)
    $names = @(& docker volume ls --filter "label=com.docker.compose.project=$projectName" --filter "label=com.docker.compose.volume=$LogicalName" --format '{{.Name}}')
    if ($LASTEXITCODE -ne 0 -or $names.Count -ne 1) { throw "Expected one Docker volume for $LogicalName after Compose create." }
    return $names[0]
}

function Get-ComposeArguments {
    $arguments = @("compose", "-f", "compose.yaml")
    if (Select-String -Path "deploy/config/odbc.ini" -Pattern '^\s*PWD_FILE\s*=' -Quiet) { $arguments += @("-f", "compose.secrets.yaml") }
    if ($Webhook) { $arguments += @("-f", "compose.webhook.yaml") }
    return $arguments
}

try {
    Set-Location -LiteralPath $repoRoot
    $backupPath = [IO.Path]::GetFullPath((Join-Path $repoRoot $Backup))
    foreach ($required in @("manifest.json", "checksums.sha256", "config.tar.gz", "volumes")) {
        if (-not (Test-Path -LiteralPath (Join-Path $backupPath $required))) { throw "Invalid backup: missing $required." }
    }
    $manifest = Get-Content -Raw -LiteralPath (Join-Path $backupPath "manifest.json") | ConvertFrom-Json
    if ($manifest.format_version -ne 1 -or $manifest.compose_project -ne $projectName) { throw "Unsupported backup format or Compose project." }

    foreach ($line in Get-Content -LiteralPath (Join-Path $backupPath "checksums.sha256")) {
        if ($line -notmatch '^([0-9a-fA-F]{64})  (.+)$') { throw "Invalid checksum line: $line" }
        $file = [IO.Path]::GetFullPath((Join-Path $backupPath $Matches[2]))
        if (-not $file.StartsWith($backupPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Checksum path escapes the backup directory." }
        if (-not (Test-Path -LiteralPath $file)) { throw "Backup file is missing: $($Matches[2])" }
        if ((Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash -ne $Matches[1]) { throw "Checksum mismatch: $($Matches[2])" }
    }

    $archives = @(Get-ChildItem -LiteralPath (Join-Path $backupPath "volumes") -Filter "*.tar.gz" -File)
    if ($archives.Count -eq 0) { throw "The backup contains no volume archives." }
    foreach ($archive in $archives) {
        $logicalName = $archive.Name.Substring(0, $archive.Name.Length - ".tar.gz".Length)
        if ($logicalName -notin $allowedVolumes) { throw "Refusing unknown volume archive: $($archive.Name)" }
    }

    if ($ValidateOnly) {
        Write-Host "Backup validation completed successfully: $backupPath"
        return
    }

    Write-Warning "Restore replaces the selected Docker volumes. The current monitoring data in those volumes will be deleted."
    if (-not $Force) {
        $confirmation = Read-Host "Type RESTORE to continue"
        if ($confirmation -cne "RESTORE") { throw "Restore cancelled." }
    }

    if ($RestoreConfig) {
        Write-Host "Restoring .env, deploy/config and deploy/secrets..."
        Invoke-Docker -Arguments @("run", "--rm", "-v", "${repoRoot}:/target", "-v", "${backupPath}:/backup:ro", $toolImage, "tar", "-xzf", "/backup/config.tar.gz", "-C", "/target")
    }

    $compose = @(Get-ComposeArguments)

    Invoke-Docker -Arguments ($compose + @("down", "--remove-orphans"))
    foreach ($archive in $archives) {
        $logicalName = $archive.Name.Substring(0, $archive.Name.Length - ".tar.gz".Length)
        $oldNames = @(& docker volume ls --filter "label=com.docker.compose.project=$projectName" --filter "label=com.docker.compose.volume=$logicalName" --format '{{.Name}}')
        foreach ($oldName in $oldNames) {
            if ($oldName) { Invoke-Docker -Arguments @("volume", "rm", $oldName) }
        }
    }

    Invoke-Docker -Arguments ($compose + @("create"))
    foreach ($archive in $archives) {
        $logicalName = $archive.Name.Substring(0, $archive.Name.Length - ".tar.gz".Length)
        $physicalName = Get-VolumeName $logicalName
        Write-Host "Restoring volume $logicalName..."
        Invoke-Docker -Arguments @("run", "--rm", "-v", "${physicalName}:/target", "-v", "${backupPath}:/backup:ro", $toolImage, "tar", "-xzf", "/backup/volumes/$($archive.Name)", "-C", "/target")
    }

    if ($Start) {
        Invoke-Docker -Arguments ($compose + @("up", "-d", "--wait", "--wait-timeout", "90"))
        Invoke-Docker -Arguments ($compose + @("ps"))
    } else {
        Write-Host "Restore completed with containers stopped. Start them with: docker compose up -d --wait"
    }
} finally {
    Set-Location -LiteralPath $previousLocation
}
