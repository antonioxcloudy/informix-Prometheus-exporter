#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_root="$repo_root/backups"
include_prometheus=false
retain=7
project_name=informix-monitoring
tool_image=busybox:1.37.0
stopped_services=""

usage() { echo "Usage: $0 [--output DIR] [--include-prometheus] [--retain COUNT]"; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; output_root=$2; shift 2 ;;
        --include-prometheus) include_prometheus=true; shift ;;
        --retain) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; retain=$2; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done
case "$retain" in ''|*[!0-9]*) echo "--retain must be a positive integer." >&2; exit 2 ;; esac
[ "$retain" -ge 1 ] || { echo "--retain must be at least 1." >&2; exit 2; }

cd "$repo_root"
command -v docker >/dev/null 2>&1 || { echo "Docker is not available in PATH." >&2; exit 1; }
docker compose version >/dev/null
mkdir -p "$output_root"
output_root=$(CDPATH= cd -- "$output_root" && pwd)
backup_path="$output_root/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$backup_path/volumes"

is_running() {
    container=$(docker compose -f compose.yaml ps -q "$1")
    [ -n "$container" ] && [ "$(docker inspect --format '{{.State.Running}}' "$container")" = true ]
}
volume_name() {
    names=$(docker volume ls --filter "label=com.docker.compose.project=$project_name" --filter "label=com.docker.compose.volume=$1" --format '{{.Name}}')
    [ "$(printf '%s\n' "$names" | sed '/^$/d' | wc -l)" -eq 1 ] || { echo "Expected one Docker volume for $1." >&2; exit 1; }
    printf '%s' "$names"
}
restart_services() {
    if [ -n "$stopped_services" ]; then
        echo "Restarting$stopped_services..."
        # shellcheck disable=SC2086
        docker compose -f compose.yaml start $stopped_services >/dev/null || true
    fi
}
trap restart_services EXIT INT TERM

services="grafana alertmanager"
volumes="grafana_data alertmanager_data"
if [ "$include_prometheus" = true ]; then
    services="$services prometheus"
    volumes="$volumes prometheus_data"
fi
for service in $services; do
    if is_running "$service"; then stopped_services="$stopped_services $service"; fi
done
if [ -n "$stopped_services" ]; then
    echo "Stopping$stopped_services briefly for a consistent backup..."
    # shellcheck disable=SC2086
    docker compose -f compose.yaml stop $stopped_services >/dev/null
fi

manifest_volumes=""
for logical_name in $volumes; do
    physical_name=$(volume_name "$logical_name")
    echo "Backing up volume $logical_name..."
    docker run --rm -v "$physical_name:/source:ro" -v "$backup_path:/backup" "$tool_image" \
        tar -czf "/backup/volumes/$logical_name.tar.gz" -C /source .
    entry="{\"logical_name\":\"$logical_name\",\"docker_volume\":\"$physical_name\",\"archive\":\"volumes/$logical_name.tar.gz\"}"
    if [ -n "$manifest_volumes" ]; then manifest_volumes="$manifest_volumes,$entry"; else manifest_volumes=$entry; fi
done

echo "WARNING: config.tar.gz contains .env, ODBC configuration and local secrets. Store it securely." >&2
docker run --rm -v "$repo_root:/source:ro" -v "$backup_path:/backup" "$tool_image" \
    tar -czf /backup/config.tar.gz -C /source .env deploy/config deploy/secrets

printf '%s\n' "{\"format_version\":1,\"created_at_utc\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"compose_project\":\"$project_name\",\"includes_prometheus\":$include_prometheus,\"configuration_archive\":\"config.tar.gz\",\"volumes\":[$manifest_volumes]}" \
    > "$backup_path/manifest.json"
(cd "$backup_path" && find . -type f ! -name checksums.sha256 -print | sort | sed 's#^./##' | while IFS= read -r file; do sha256sum "$file"; done > checksums.sha256)

find "$output_root" -mindepth 1 -maxdepth 1 -type d -exec test -f '{}/manifest.json' \; -print |
    sort -r | awk -v keep="$retain" 'NR > keep' | while IFS= read -r old_backup; do
        case "$old_backup" in "$output_root"/*) echo "Removing expired backup $(basename "$old_backup")..."; rm -rf -- "$old_backup" ;; *) echo "Refusing to prune $old_backup" >&2; exit 1 ;; esac
    done

trap - EXIT INT TERM
restart_services
echo "Backup completed: $backup_path"
