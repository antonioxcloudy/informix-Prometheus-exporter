#!/bin/sh
set -eu

# These modes do not need the proprietary runtime or a live ODBC connection.
for argument in "$@"; do
    case "${argument}" in
        -h|--help|-help|--version|-version|--check-config|-check-config)
            exec /usr/local/bin/informix_exporter "$@"
            ;;
    esac
done

if [ ! -r "${INFORMIXDIR}/lib/cli/iclit09b.so" ]; then
    echo "Informix Client SDK runtime not found at ${INFORMIXDIR}." >&2
    echo "Run: docker compose --profile setup run --rm csdk-init" >&2
    exit 1
fi

if [ ! -r "${ODBCINI}" ]; then
    echo "ODBC configuration is not readable at ${ODBCINI}." >&2
    exit 1
fi

# PWD_FILE is an exporter-side extension that keeps passwords out of odbc.ini.
# Each DSN can reference a different Docker secret. The generated file lives in
# the container tmpfs and is readable only by the exporter user.
if grep -Eq '^[[:space:]]*PWD_FILE[[:space:]]*=' "${ODBCINI}"; then
    source_odbcini="${ODBCINI}"
    runtime_odbcini=/tmp/informix-exporter-odbc.ini
    umask 077
    : > "${runtime_odbcini}"

    while IFS= read -r line || [ -n "${line}" ]; do
        if printf '%s\n' "${line}" | grep -Eq '^[[:space:]]*PWD_FILE[[:space:]]*='; then
            secret_file=$(printf '%s\n' "${line}" | sed 's/^[[:space:]]*PWD_FILE[[:space:]]*=[[:space:]]*//; s/[[:space:]]*$//')
            if [ -z "${secret_file}" ] || [ ! -r "${secret_file}" ]; then
                echo "Informix password secret is not readable: ${secret_file:-<empty path>}." >&2
                exit 1
            fi
            password=$(head -n 1 "${secret_file}")
            if [ -z "${password}" ]; then
                echo "Informix password secret is empty: ${secret_file}." >&2
                exit 1
            fi
            printf 'PWD=%s\n' "${password}" >> "${runtime_odbcini}"
        else
            printf '%s\n' "${line}" >> "${runtime_odbcini}"
        fi
    done < "${source_odbcini}"

    ODBCINI="${runtime_odbcini}"
    export ODBCINI
fi

if [ ! -r "${INFORMIXSQLHOSTS}" ]; then
    echo "Informix sqlhosts is not readable at ${INFORMIXSQLHOSTS}." >&2
    exit 1
fi

for argument in "$@"; do
    case "${argument}" in
        --check-runtime|-check-runtime)
            echo "Informix Client SDK, ODBC configuration, sqlhosts, and password files are readable."
            exit 0
            ;;
    esac
done

exec /usr/local/bin/informix_exporter "$@"
