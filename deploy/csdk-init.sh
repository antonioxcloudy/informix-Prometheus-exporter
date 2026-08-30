#!/bin/sh
set -eu

archive=/input/informix-client-runtime-linux-amd64.tar.gz
target=/opt/informix
marker="${target}/.runtime-sha256"

if [ ! -r "$archive" ]; then
    echo "Informix runtime archive is not readable: $archive" >&2
    exit 1
fi

checksum="$(sha256sum "$archive" | awk '{print $1}')"

if [ -f "$marker" ]; then
    installed_checksum="$(cat "$marker")"
    if [ "$installed_checksum" = "$checksum" ] \
        && [ -r "${target}/lib/cli/iclit09b.so" ]; then
        echo "Informix Client SDK runtime is already installed ($checksum)."
        exit 0
    fi

    echo "The informix_csdk volume contains a different runtime." >&2
    echo "Remove that Docker volume explicitly before installing another archive." >&2
    exit 1
fi

if [ -n "$(find "$target" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
    echo "The informix_csdk volume is not empty; refusing to overwrite it." >&2
    exit 1
fi

if tar -tzf "$archive" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
    echo "The archive contains an unsafe path and will not be extracted." >&2
    exit 1
fi

tar -xzf "$archive" -C "$target" --no-same-owner

for required_path in \
    lib/cli/iclit09b.so \
    lib/esql/libifgls.so \
    lib/esql/libifglx.so \
    gls \
    msg
do
    if [ ! -e "${target}/${required_path}" ]; then
        echo "Required runtime path is missing after extraction: $required_path" >&2
        exit 1
    fi
done

printf '%s\n' "$checksum" > "$marker"
chmod -R a+rX "$target"

echo "Informix Client SDK runtime installed in the Docker volume ($checksum)."
