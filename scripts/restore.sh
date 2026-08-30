#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backup_path=""
restore_config=false
validate_only=false
start_stack=false
use_webhook=false
force=false
project_name=informix-monitoring
tool_image=busybox:1.37.0

usage() { echo "Usage: $0 --backup DIR [--validate-only] [--restore-config] [--start] [--webhook] [--force]"; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --backup) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; backup_path=$2; shift 2 ;;
        --validate-only) validate_only=true; shift ;;
        --restore-config) restore_config=true; shift ;;
        --start) start_stack=true; shift ;;
        --webhook) use_webhook=true; shift ;;
        --force) force=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done
[ -n "$backup_path" ] || { usage >&2; exit 2; }
cd "$repo_root"
backup_path=$(CDPATH= cd -- "$backup_path" && pwd)
for required in manifest.json checksums.sha256 config.tar.gz volumes; do
    [ -e "$backup_path/$required" ] || { echo "Invalid backup: missing $required." >&2; exit 1; }
done
grep -q '"format_version"[[:space:]]*:[[:space:]]*1' "$backup_path/manifest.json" || { echo "Unsupported backup format." >&2; exit 1; }
grep -q '"compose_project"[[:space:]]*:[[:space:]]*"informix-monitoring"' "$backup_path/manifest.json" || { echo "Wrong Compose project." >&2; exit 1; }
(cd "$backup_path" && sha256sum -c checksums.sha256)

archives=$(find "$backup_path/volumes" -mindepth 1 -maxdepth 1 -type f -name '*.tar.gz' -print)
[ -n "$archives" ] || { echo "The backup contains no volume archives." >&2; exit 1; }
for archive in $archives; do
    logical_name=$(basename "$archive" .tar.gz)
    case "$logical_name" in grafana_data|alertmanager_data|prometheus_data) ;; *) echo "Refusing unknown volume archive: $logical_name" >&2; exit 1 ;; esac
done

if [ "$validate_only" = true ]; then
    echo "Backup validation completed successfully: $backup_path"
    exit 0
fi

echo "WARNING: Restore replaces selected Docker volumes and deletes their current data." >&2
if [ "$force" != true ]; then
    printf 'Type RESTORE to continue: '
    IFS= read -r confirmation
    [ "$confirmation" = RESTORE ] || { echo "Restore cancelled." >&2; exit 1; }
fi

if [ "$restore_config" = true ]; then
    echo "Restoring .env, deploy/config and deploy/secrets..."
    docker run --rm -v "$repo_root:/target" -v "$backup_path:/backup:ro" "$tool_image" tar -xzf /backup/config.tar.gz -C /target
fi

compose_files="-f compose.yaml"
if grep -Eq '^[[:space:]]*PWD_FILE[[:space:]]*=' deploy/config/odbc.ini; then compose_files="$compose_files -f compose.secrets.yaml"; fi
if [ "$use_webhook" = true ]; then compose_files="$compose_files -f compose.webhook.yaml"; fi
compose() {
    # shellcheck disable=SC2086
    docker compose $compose_files "$@"
}

compose down --remove-orphans
for archive in $archives; do
    logical_name=$(basename "$archive" .tar.gz)
    old_names=$(docker volume ls --filter "label=com.docker.compose.project=$project_name" --filter "label=com.docker.compose.volume=$logical_name" --format '{{.Name}}')
    for old_name in $old_names; do docker volume rm "$old_name" >/dev/null; done
done
compose create
for archive in $archives; do
    logical_name=$(basename "$archive" .tar.gz)
    physical_name=$(docker volume ls --filter "label=com.docker.compose.project=$project_name" --filter "label=com.docker.compose.volume=$logical_name" --format '{{.Name}}')
    [ "$(printf '%s\n' "$physical_name" | sed '/^$/d' | wc -l)" -eq 1 ] || { echo "Expected one volume for $logical_name." >&2; exit 1; }
    echo "Restoring volume $logical_name..."
    docker run --rm -v "$physical_name:/target" -v "$backup_path:/backup:ro" "$tool_image" tar -xzf "/backup/volumes/$(basename "$archive")" -C /target
done

if [ "$start_stack" = true ]; then
    compose up -d --wait --wait-timeout 90
    compose ps
else
    echo "Restore completed with containers stopped. Start them with: docker compose up -d --wait"
fi
