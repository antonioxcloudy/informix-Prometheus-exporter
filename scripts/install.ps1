[CmdletBinding()]
param(
    [ValidateSet("Prepare", "Validate", "Start")]
    [string]$Action = "Prepare",
    [switch]$Secrets,
    [switch]$Webhook
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousLocation = Get-Location

function Invoke-Docker {
    param([string[]]$Arguments)

    & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Docker command failed with exit code $LASTEXITCODE."
    }
}

function Copy-TemplateIfMissing {
    param([string]$Source, [string]$Destination)

    if (Test-Path -LiteralPath $Destination) {
        Write-Host "Keeping existing $Destination"
        return
    }
    Copy-Item -LiteralPath $Source -Destination $Destination
    Write-Host "Created $Destination"
}

function Get-ComposeArguments {
    $arguments = @("compose", "-f", "compose.yaml")
    if (Select-String -Path "deploy/config/odbc.ini" -Pattern '^\s*PWD_FILE\s*=' -Quiet) {
        $arguments += @("-f", "compose.secrets.yaml")
    }
    if ($Webhook) {
        $arguments += @("-f", "compose.webhook.yaml")
    }
    return $arguments
}

function Get-WebhookSecretFile {
    if ($env:ALERTMANAGER_WEBHOOK_URL_FILE) {
        return $env:ALERTMANAGER_WEBHOOK_URL_FILE
    }
    return "deploy/secrets/alertmanager_webhook_url"
}

function Get-LocalInformixSecretFiles {
    $files = @()
    foreach ($line in Get-Content -LiteralPath "deploy/config/odbc.ini") {
        if ($line -notmatch '^\s*PWD_FILE\s*=\s*(.*?)\s*$') { continue }
        $containerPath = $Matches[1]
        if ($containerPath -eq "/run/secrets/informix_password") {
            $files += $(if ($env:INFORMIX_PASSWORD_SECRET_FILE) { $env:INFORMIX_PASSWORD_SECRET_FILE } else { "deploy/secrets/informix_password" })
            continue
        }
        $prefix = "/run/informix-secrets/"
        if ($containerPath.StartsWith($prefix, [StringComparison]::Ordinal)) {
            $name = $containerPath.Substring($prefix.Length)
            if ($name -and $name -notmatch '[/\\]' -and $name -notin @(".", "..")) {
                $files += Join-Path "deploy/secrets/informix" $name
                continue
            }
        }
        throw "Unsupported PWD_FILE path: $containerPath. Use /run/secrets/informix_password or /run/informix-secrets/<name>."
    }
    return $files | Select-Object -Unique
}

function Assert-PreparedConfiguration {
    param([switch]$RequireSecureGrafanaPassword)

    foreach ($path in @(".env", "deploy/config/config.yaml", "deploy/config/odbc.ini", "deploy/config/sqlhosts", "deploy/config/alertmanager.yml")) {
        if (-not (Test-Path -LiteralPath $path)) {
            throw "Required file is missing: $path. Run the Prepare action first."
        }
    }

    $grafanaPassword = Get-Content -LiteralPath ".env" |
        Where-Object { $_ -match '^GRAFANA_ADMIN_PASSWORD=' } |
        Select-Object -First 1
    if (-not $grafanaPassword -or $grafanaPassword -eq "GRAFANA_ADMIN_PASSWORD=change-me") {
        if ($RequireSecureGrafanaPassword) {
            throw "Set a secure GRAFANA_ADMIN_PASSWORD in .env before starting the stack."
        }
        Write-Warning "GRAFANA_ADMIN_PASSWORD still uses its initial value; Start will reject it."
    }

    if (Select-String -Path "deploy/config/odbc.ini" -Pattern '^\s*PWD_FILE\s*=' -Quiet) {
        foreach ($secretFile in Get-LocalInformixSecretFiles) {
            if (-not (Test-Path -LiteralPath $secretFile) -or (Get-Item -LiteralPath $secretFile).Length -eq 0) {
                throw "Informix password file is missing or empty: $secretFile"
            }
        }
    }

    if ($Webhook) {
        $webhookFile = Get-WebhookSecretFile
        if (-not (Test-Path -LiteralPath $webhookFile) -or (Get-Item -LiteralPath $webhookFile).Length -eq 0) {
            throw "$webhookFile is missing or empty."
        }
        $webhookUrl = (Get-Content -Raw -LiteralPath $webhookFile).Trim()
        $parsedUrl = [Uri]$webhookUrl
        if (-not $parsedUrl.IsAbsoluteUri -or $parsedUrl.Scheme -notin @("http", "https")) {
            throw "$webhookFile must contain one absolute HTTP or HTTPS URL."
        }
    }
}

try {
    Set-Location -LiteralPath $repoRoot

    New-Item -ItemType Directory -Force -Path "deploy/config", "deploy/secrets", "deploy/secrets/informix", "runtime" | Out-Null
    Copy-TemplateIfMissing ".env.example" ".env"
    Copy-TemplateIfMissing "examples/config.yaml" "deploy/config/config.yaml"
    Copy-TemplateIfMissing "examples/alertmanager.yml" "deploy/config/alertmanager.yml"
    if ($Secrets) {
        Copy-TemplateIfMissing "examples/odbc.secrets.ini" "deploy/config/odbc.ini"
        if (-not (Test-Path -LiteralPath "deploy/secrets/informix_password")) {
            New-Item -ItemType File -Path "deploy/secrets/informix_password" | Out-Null
            Write-Host "Created empty deploy/secrets/informix_password"
        }
    } else {
        Copy-TemplateIfMissing "examples/odbc.ini" "deploy/config/odbc.ini"
    }
    Copy-TemplateIfMissing "examples/sqlhosts" "deploy/config/sqlhosts"
    $webhookFile = Get-WebhookSecretFile
    if ($Webhook -and -not (Test-Path -LiteralPath $webhookFile)) {
        New-Item -ItemType File -Path $webhookFile | Out-Null
        Write-Host "Created empty $webhookFile"
    }

    if ($Action -eq "Prepare") {
        Write-Host "Preparation complete. Edit .env and the files under deploy/config before validating."
        return
    }

    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "Docker is not available in PATH."
    }
    Invoke-Docker -Arguments @("compose", "version")
    Assert-PreparedConfiguration -RequireSecureGrafanaPassword:($Action -eq "Start")
    $compose = @(Get-ComposeArguments)
    Invoke-Docker -Arguments ($compose + @("config", "--quiet"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "--no-deps", "--entrypoint", "promtool", "prometheus", "check", "config", "/etc/prometheus/prometheus.yml"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "--no-deps", "--entrypoint", "amtool", "alertmanager", "check-config", "/etc/alertmanager/alertmanager.yml"))
    Invoke-Docker -Arguments ($compose + @("build", "informix-exporter"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "informix-exporter", "--configfile=/etc/informix-exporter/config.yaml", "--check-config"))

    if ($Action -eq "Validate") {
        Write-Host "Configuration and image validation completed successfully."
        return
    }

    Invoke-Docker -Arguments ($compose + @("--profile", "setup", "run", "--rm", "csdk-init"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "informix-exporter", "--check-runtime"))
    Invoke-Docker -Arguments ($compose + @("run", "--rm", "informix-exporter", "--configfile=/etc/informix-exporter/config.yaml", "--scrape-timeout=8s", "--check"))
    Invoke-Docker -Arguments ($compose + @("up", "-d", "--wait", "--wait-timeout", "90"))
    Invoke-Docker -Arguments ($compose + @("ps"))

    Write-Host "Installation completed: exporter http://localhost:8080, Prometheus http://localhost:9090, Alertmanager http://localhost:9093, Grafana http://localhost:3000"
} finally {
    Set-Location -LiteralPath $previousLocation
}
