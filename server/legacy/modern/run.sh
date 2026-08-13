#!/bin/sh
set -eu

# Preserve a crash dump for the still-partially-modernized legacy C server.
# The runtime directory is bind-mounted back into the repository, so a dump
# remains available after Docker exits and can be inspected with matching
# symbols from the self-contained build.
ulimit -c unlimited 2>/dev/null || true

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="$game_root/logs"

mkdir -p "$log_root"

shutdown()
{
    trap - INT TERM EXIT
    if [ -n "${gmsv_pid:-}" ]; then
        kill "$gmsv_pid" 2>/dev/null || true
    fi
    if [ -n "${saac_pid:-}" ]; then
        kill "$saac_pid" 2>/dev/null || true
    fi
    wait 2>/dev/null || true
}

trap shutdown INT TERM EXIT

cd "$game_root/saac"
./saacjt.exe >"$log_root/saac.log" 2>&1 &
saac_pid=$!

# GMSV connects to SAAC during initialization. Wait briefly for SAAC instead
# of relying on machine speed or a fixed startup delay.
attempt=0
while ! nc -z 127.0.0.1 9300 >/dev/null 2>&1; do
    if ! kill -0 "$saac_pid" 2>/dev/null; then
        echo "SAAC exited during startup; see $log_root/saac.log" >&2
        exit 1
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 100 ]; then
        echo "SAAC did not listen on port 9300 within 10 seconds" >&2
        exit 1
    fi
    sleep 0.1
done

cd "$game_root/gmsv"
./gmsvjt.exe -f setup.cf >"$log_root/gmsv.log" 2>&1 &
gmsv_pid=$!

# Keep the container alive while both legacy processes are alive. Exiting the
# container if either process dies makes failures visible to health checks.
while kill -0 "$saac_pid" 2>/dev/null && kill -0 "$gmsv_pid" 2>/dev/null; do
    sleep 1
done

echo "A legacy server process exited; inspect $log_root" >&2
exit 1
