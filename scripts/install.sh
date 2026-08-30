#!/bin/sh
set -eu

action=prepare
use_secrets=false
use_webhook=false

usage() {
    echo "Usage: $0 [prepare|validate|start] [--secrets] [--webhook]"
}

for argument in "$@"; do
    case "$argument" in
        prepare|validate|start) action="$argument" ;;
        --secrets) use_secrets=true ;;
        --webhook) use_webhook=true ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $argument" >&2; usage >&2; exit 2 ;;
    esac
done

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"
webhook_file=${ALERTMANAGER_WEBHOOK_URL_FILE:-deploy/secrets/alertmanager_webhook_url}

mkdir -p deploy/config deploy/secrets deploy/secrets/informix runtime

copy_if_missing() {
    source_file=$1
    destination=$2
    if [ -e "$destination" ]; then
        echo "Keeping existing $destination"
        return
    fi
    cp "$source_file" "$destination"
    echo "Created $destination"
}

copy_if_missing .env.example .env
copy_if_missing examples/config.yaml deploy/config/config.yaml
copy_if_missing examples/alertmanager.yml deploy/config/alertmanager.yml
if [ "$use_secrets" = true ]; then
    copy_if_missing examples/odbc.secrets.ini deploy/config/odbc.ini
    if [ ! -e deploy/secrets/informix_password ]; then
        : > deploy/secrets/informix_password
        chmod 600 deploy/secrets/informix_password
        echo "Created empty deploy/secrets/informix_password"
    fi
else
    copy_if_missing examples/odbc.ini deploy/config/odbc.ini
fi
copy_if_missing examples/sqlhosts deploy/config/sqlhosts
if [ "$use_webhook" = true ] && [ ! -e "$webhook_file" ]; then
    : > "$webhook_file"
    chmod 600 "$webhook_file"
    echo "Created empty $webhook_file"
fi

if [ "$action" = prepare ]; then
    echo "Preparation complete. Edit .env and the files under deploy/config before validating."
    exit 0
fi

command -v docker >/dev/null 2>&1 || {
    echo "Docker is not available in PATH." >&2
    exit 1
}
docker compose version >/dev/null

for required_file in .env deploy/config/config.yaml deploy/config/odbc.ini deploy/config/sqlhosts deploy/config/alertmanager.yml; do
    if [ ! -f "$required_file" ]; then
        echo "Required file is missing: $required_file. Run prepare first." >&2
        exit 1
    fi
done

if ! grep -Eq '^GRAFANA_ADMIN_PASSWORD=.+$' .env \
    || grep -Eq '^GRAFANA_ADMIN_PASSWORD=change-me$' .env; then
    if [ "$action" = start ]; then
        echo "Set a secure GRAFANA_ADMIN_PASSWORD in .env before starting the stack." >&2
        exit 1
    fi
    echo "Warning: GRAFANA_ADMIN_PASSWORD still uses its initial value; start will reject it." >&2
fi

secrets_enabled=false
if grep -Eq '^[[:space:]]*PWD_FILE[[:space:]]*=' deploy/config/odbc.ini; then
    secrets_enabled=true
    sed -n 's/^[[:space:]]*PWD_FILE[[:space:]]*=[[:space:]]*//; s/[[:space:]]*$//; /^$/d; p' deploy/config/odbc.ini |
        while IFS= read -r container_path; do
            case "$container_path" in
                /run/secrets/informix_password)
                    secret_file=${INFORMIX_PASSWORD_SECRET_FILE:-deploy/secrets/informix_password}
                    ;;
                /run/informix-secrets/*)
                    secret_name=${container_path#/run/informix-secrets/}
                    case "$secret_name" in ''|.|..|*/*) echo "Unsupported PWD_FILE path: $container_path" >&2; exit 1 ;; esac
                    secret_file="deploy/secrets/informix/$secret_name"
                    ;;
                *)
                    echo "Unsupported PWD_FILE path: $container_path" >&2
                    echo "Use /run/secrets/informix_password or /run/informix-secrets/<name>." >&2
                    exit 1
                    ;;
            esac
            if [ ! -s "$secret_file" ]; then
                echo "Informix password file is missing or empty: $secret_file" >&2
                exit 1
            fi
        done
fi

if [ "$use_webhook" = true ] \
    && ! grep -Eq '^https?://[^[:space:]]+$' "$webhook_file" 2>/dev/null; then
    echo "$webhook_file must contain one absolute HTTP or HTTPS URL." >&2
    exit 1
fi

compose() {
    if [ "$secrets_enabled" = true ] && [ "$use_webhook" = true ]; then
        docker compose -f compose.yaml -f compose.secrets.yaml -f compose.webhook.yaml "$@"
    elif [ "$secrets_enabled" = true ]; then
        docker compose -f compose.yaml -f compose.secrets.yaml "$@"
    elif [ "$use_webhook" = true ]; then
        docker compose -f compose.yaml -f compose.webhook.yaml "$@"
    else
        docker compose "$@"
    fi
}

compose config --quiet
compose run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
compose run --rm --no-deps --entrypoint amtool alertmanager check-config /etc/alertmanager/alertmanager.yml
compose build informix-exporter
compose run --rm informix-exporter --configfile=/etc/informix-exporter/config.yaml --check-config

if [ "$action" = validate ]; then
    echo "Configuration and image validation completed successfully."
    exit 0
fi

compose --profile setup run --rm csdk-init
compose run --rm informix-exporter --check-runtime
compose run --rm informix-exporter --configfile=/etc/informix-exporter/config.yaml --scrape-timeout=8s --check
compose up -d --wait --wait-timeout 90
compose ps

echo "Installation completed: exporter http://localhost:8080, Prometheus http://localhost:9090, Alertmanager http://localhost:9093, Grafana http://localhost:3000"
