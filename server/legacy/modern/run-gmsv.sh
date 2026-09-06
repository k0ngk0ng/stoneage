#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="${STONEAGE_LOG_ROOT:-$game_root/gmsv/logs}"
saac_host="${STONEAGE_SAAC_HOST:-saac}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"
saac_char_dir="${STONEAGE_SAAC_CHAR_DIR:-}"
source_config="${STONEAGE_GMSV_CONFIG:-$game_root/gmsv/setup.cf}"

mkdir -p "$log_root"

attempt=0
while ! nc -z "$saac_host" "$saac_port" >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 120 ]; then
        echo "SAAC did not become ready at $saac_host:$saac_port" >&2
        exit 1
    fi
    sleep 0.1
done

# The archived setup.cf points at 127.0.0.1 because the original launcher ran
# GMSV and SAAC on one machine. In a split deployment SAAC is a peer service;
# generate an ephemeral config with only the connection endpoint changed and
# keep the user-editable setup.cf untouched on the shared runtime volume.
config_file="$(mktemp "${TMPDIR:-/tmp}/stoneage-setup.XXXXXX")"
cleanup()
{
    rm -f "$config_file"
}
trap cleanup EXIT INT TERM
# Keep the archived relative storedir for the split GMSV/SAAC layout. An
# explicit override is available for operators that keep character files in a
# separately mounted directory without sharing the rest of a GMSV runtime.
if [ -n "$saac_char_dir" ]; then
    sed -e "s/^acserv=.*/acserv=$saac_host/" \
        -e "s/^acservport=.*/acservport=$saac_port/" \
        -e "s#^storedir=.*#storedir=$saac_char_dir#" \
        "$source_config" >"$config_file"
else
    sed -e "s/^acserv=.*/acserv=$saac_host/" \
        -e "s/^acservport=.*/acservport=$saac_port/" \
        "$source_config" >"$config_file"
fi

cd "$game_root/gmsv"
exec ./gmsvjt.exe -f "$config_file" >"$log_root/gmsv.log" 2>&1
