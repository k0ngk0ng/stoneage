#!/bin/sh
set -eu

# Preserve a crash dump for the still-partially-modernized legacy C server.
# The runtime directory is bind-mounted back into the repository, so a dump
# remains available after Docker exits and can be inspected with matching
# symbols from the self-contained build.
ulimit -c unlimited 2>/dev/null || true

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="$game_root/logs"
control_file="$game_root/.stoneage-control"

mkdir -p "$log_root"
rm -f "$control_file"

saac_pid=""
gmsv_pid=""
saac_enabled=1
gmsv_enabled=1

process_alive()
{
    kill -0 "$1" 2>/dev/null
}

stop_process()
{
    pid="$1"
    if [ -z "$pid" ] || ! process_alive "$pid"; then
        return 0
    fi
    kill "$pid" 2>/dev/null || true
    attempt=0
    while process_alive "$pid" && [ "$attempt" -lt 50 ]; do
        sleep 0.1
        attempt=$((attempt + 1))
    done
    if process_alive "$pid"; then
        kill -9 "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
}

wait_for_port()
{
    port="$1"
    label="$2"
    attempt=0
    while ! nc -z 127.0.0.1 "$port" >/dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 100 ]; then
            echo "$label did not listen on port $port within 10 seconds" >&2
            return 1
        fi
        sleep 0.1
    done
}

start_saac()
{
    cd "$game_root/saac"
    ./saacjt.exe >"$log_root/saac.log" 2>&1 &
    saac_pid=$!
    if ! wait_for_port 9300 SAAC; then
        echo "SAAC exited during startup; see $log_root/saac.log" >&2
        return 1
    fi
}

stop_saac()
{
    stop_process "$saac_pid"
    saac_pid=""
}

start_gmsv()
{
    cd "$game_root/gmsv"
    ./gmsvjt.exe -f setup.cf >"$log_root/gmsv.log" 2>&1 &
    gmsv_pid=$!
    if ! wait_for_port 9065 GMSV; then
        echo "GMSV exited during startup; see $log_root/gmsv.log" >&2
        return 1
    fi
}

stop_gmsv()
{
    stop_process "$gmsv_pid"
    gmsv_pid=""
}

restart_saac()
{
    stop_saac
    start_saac
}

restart_gmsv()
{
    stop_gmsv
    # GMSV requires SAAC to be listening during initialization.
    wait_for_port 9300 SAAC
    start_gmsv
}

shutdown()
{
    trap - INT TERM EXIT
    stop_gmsv
    stop_saac
    rm -f "$control_file"
}

trap shutdown INT TERM EXIT

start_saac
start_gmsv

# The host-side fixed restart scripts write one of two validated commands to
# this file through control.sh. Keeping the supervisor in one container still
# allows GMSV and SAAC to be restarted independently without exposing Docker
# or arbitrary process execution to the web process.
while :; do
    if [ -s "$control_file" ]; then
        command="$(tr -d '\r\n' <"$control_file")"
        rm -f "$control_file"
        case "$command" in
        restart-gmsv)
            gmsv_enabled=1
            if ! restart_gmsv; then
                echo "GMSV restart failed; keeping supervisor alive" >&2
            fi
            ;;
        restart-saac)
            saac_enabled=1
            if ! restart_saac; then
                echo "SAAC restart failed; keeping supervisor alive" >&2
            fi
            ;;
        stop-gmsv)
            stop_gmsv
            gmsv_enabled=0
            ;;
        stop-saac)
            stop_saac
            saac_enabled=0
            ;;
        *)
            echo "Ignoring unknown server control command" >&2
            ;;
        esac
    fi

    if [ "$saac_enabled" -eq 1 ] && [ -n "$saac_pid" ] && ! process_alive "$saac_pid"; then
        echo "SAAC exited unexpectedly; inspect $log_root/saac.log" >&2
        exit 1
    fi
    if [ "$gmsv_enabled" -eq 1 ] && [ -n "$gmsv_pid" ] && ! process_alive "$gmsv_pid"; then
        echo "GMSV exited unexpectedly; inspect $log_root/gmsv.log" >&2
        exit 1
    fi
    sleep 0.2
done
