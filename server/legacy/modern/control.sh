#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
case "${1:-}" in
restart-gmsv|restart-saac|stop-gmsv|stop-saac)
    command="$1"
    ;;
*)
    echo "unsupported server control command" >&2
    exit 2
    ;;
esac

temporary="$game_root/.stoneage-control.$$"
printf '%s\n' "$command" >"$temporary"
mv -f "$temporary" "$game_root/.stoneage-control"
