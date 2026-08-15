#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="$game_root/logs"
saac_host="${STONEAGE_SAAC_HOST:-saac}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"
saac_char_dir="${STONEAGE_SAAC_CHAR_DIR:-}"

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
config_file="$(mktemp /tmp/stoneage-setup.XXXXXX)"
cleanup()
{
    rm -f "$config_file"
}
trap cleanup EXIT INT TERM
# Keep the archived relative storedir for the single-runtime layout. A
# multi-line deployment can mount the shared SAAC character directory at an
# explicit path without sharing the rest of a GMSV's runtime tree.
if [ -n "$saac_char_dir" ]; then
    sed -e "s/^acserv=.*/acserv=$saac_host/" \
        -e "s/^acservport=.*/acservport=$saac_port/" \
        -e "s#^storedir=.*#storedir=$saac_char_dir#" \
        "$game_root/gmsv/setup.cf" >"$config_file"
else
    sed -e "s/^acserv=.*/acserv=$saac_host/" \
        -e "s/^acservport=.*/acservport=$saac_port/" \
        "$game_root/gmsv/setup.cf" >"$config_file"
fi

cd "$game_root/gmsv"
exec ./gmsvjt.exe -f "$config_file" >"$log_root/gmsv.log" 2>&1
