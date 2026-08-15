#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
role="${STONEAGE_RUNTIME_ROLE:-}"
defaults="/opt/stoneage/defaults"

copy_file()
{
    source="$1"
    target="$2"
    install -m "${3:-0644}" "$source" "$target"
}

replace_tree()
{
    source="$1"
    target="$2"
    rm -rf "$target"
    mkdir -p "$(dirname "$target")"
    cp -a "$source" "$target"
}

init_gmsv()
{
    root="$game_root/gmsv"
    mkdir -p "$root"
    copy_file "$defaults/gmsv/gmsvjt.exe" "$root/gmsvjt.exe" 0755
    replace_tree "$defaults/gmsv/data" "$root/data"
    for directory in Dengon Schedule; do
        if [ -d "$defaults/gmsv/$directory" ]; then
            replace_tree "$defaults/gmsv/$directory" "$root/$directory"
        fi
    done
    for file in badpetstring.txt log.cf; do
        if [ -f "$defaults/gmsv/$file" ]; then
            copy_file "$defaults/gmsv/$file" "$root/$file"
        fi
    done
    if [ ! -f "$root/setup.cf" ]; then
        copy_file "$defaults/gmsv/setup.cf" "$root/setup.cf"
    fi
    mkdir -p "$root/log" "$root/lostpet" "$root/logs"
}

init_saac()
{
    root="$game_root/saac"
    mkdir -p "$root"
    copy_file "$defaults/saac/saacjt.exe" "$root/saacjt.exe" 0755
    if [ -f "$defaults/saac/badpetstring.txt" ]; then
        copy_file "$defaults/saac/badpetstring.txt" "$root/badpetstring.txt"
    fi
    if [ ! -f "$root/acserv.cf" ]; then
        copy_file "$defaults/saac/acserv.cf" "$root/acserv.cf"
    fi
    mkdir -p "$root/char" "$root/char_sleep" "$root/db" "$root/mail" \
        "$root/log" "$root/lock" "$root/data/family" \
        "$root/data/fmpointdir" "$root/data/fmsmemodir" "$root/logs"
}

case "$role" in
gmsv)
    init_gmsv
    ;;
saac)
    init_saac
    ;;
both)
    init_gmsv
    init_saac
    ;;
*)
    echo "STONEAGE_RUNTIME_ROLE must be gmsv, saac, or both" >&2
    exit 2
    ;;
esac

exec "$@"
