#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
include_prometheus=false
require_informix=false
use_webhook=false
retain=7
rollback_file="$repo_root/backups/last-update-images.tsv"
mutation_started=false
update_succeeded=false

usage() { echo "Usage: $0 [--include-prometheus-backup] [--require-informix] [--webhook] [--retain COUNT]"; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --include-prometheus-backup) include_prometheus=true; shift ;;
        --require-informix) require_informix=true; shift ;;
        --webhook) use_webhook=true; shift ;;
        --retain) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; retain=$2; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

cd "$repo_root"
compose_files="-f compose.yaml"
if grep -Eq '^[[:space:]]*PWD_FILE[[:space:]]*=' deploy/config/odbc.ini; then compose_files="$compose_files -f compose.secrets.yaml"; fi
if [ "$use_webhook" = true ]; then compose_files="$compose_files -f compose.webhook.yaml"; fi
compose() {
    # shellcheck disable=SC2086
    docker compose $compose_files "$@"
}

rollback() {
    [ "$mutation_started" = true ] || return 0
    [ "$update_succeeded" = false ] || return 0
    echo "WARNING: Update failed; restoring previous image references..." >&2
    if [ -f "$rollback_file" ]; then
        while IFS='|' read -r service reference image_id rollback_reference; do
            [ -n "$service" ] || continue
            docker image tag "$rollback_reference" "$reference" || true
        done < "$rollback_file"
        compose up -d --no-build --force-recreate --wait --wait-timeout 90 || \
            echo "ERROR: Automatic rollback failed. Use $rollback_file and the latest backup to recover." >&2
    fi
}
trap rollback EXIT INT TERM

echo "Running configuration preflight..."
compose config --quiet
compose run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
compose run --rm --no-deps --entrypoint amtool alertmanager check-config /etc/alertmanager/alertmanager.yml

if [ "$include_prometheus" = true ]; then
    "$repo_root/scripts/backup.sh" --include-prometheus --retain "$retain"
else
    "$repo_root/scripts/backup.sh" --retain "$retain"
fi

: > "$rollback_file"
for service in informix-exporter prometheus alertmanager grafana; do
    container=$(docker compose -f compose.yaml ps -q "$service")
    if [ -n "$container" ]; then
        reference=$(docker inspect --format '{{.Config.Image}}' "$container")
        image_id=$(docker inspect --format '{{.Image}}' "$container")
        rollback_reference="local/informix-monitoring-rollback:$service"
        docker image tag "$image_id" "$rollback_reference"
        printf '%s|%s|%s|%s\n' "$service" "$reference" "$image_id" "$rollback_reference" >> "$rollback_file"
    fi
done

mutation_started=true
echo "Pulling service images and rebuilding the exporter..."
compose pull --ignore-buildable
compose build --pull informix-exporter
compose run --rm informix-exporter --configfile=/etc/informix-exporter/config.yaml --check-config
compose up -d --wait --wait-timeout 90

network=informix-monitoring_monitoring
exporter_endpoint=http://informix-exporter:8080/healthz
if [ "$require_informix" = true ]; then exporter_endpoint=http://informix-exporter:8080/readyz; fi
for endpoint in \
    "$exporter_endpoint" \
    http://prometheus:9090/-/ready \
    http://alertmanager:9093/-/ready \
    http://grafana:3000/api/health; do
    ready=false
    attempt=1
    while [ "$attempt" -le 12 ]; do
        if docker run --rm --network "$network" busybox:1.37.0 wget -q --spider "$endpoint"; then ready=true; break; fi
        [ "$attempt" -eq 12 ] || sleep 5
        attempt=$((attempt + 1))
    done
    [ "$ready" = true ] || { echo "Endpoint did not become ready: $endpoint" >&2; exit 1; }
done
if [ "$require_informix" = false ] && ! docker run --rm --network "$network" busybox:1.37.0 wget -q --spider http://informix-exporter:8080/readyz; then
    echo "WARNING: The stack is healthy, but Informix is not currently ready. Use --require-informix for a strict database check." >&2
fi
compose ps
update_succeeded=true
trap - EXIT INT TERM
echo "Update completed successfully. The pre-update backup is under backups/."
