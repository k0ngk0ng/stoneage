#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
runtime_root="$project_root/runtime/legacy-server"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"
gateway_listen="${STONEAGE_GATEWAY_LISTEN:-127.0.0.1:9065}"
gateway_upstream="${STONEAGE_GATEWAY_UPSTREAM:-127.0.0.1:$upstream_port}"
gateway_routes="${STONEAGE_GATEWAY_ROUTES:-}"
gateway_binary="$project_root/build/stoneage-gateway"
gateway_pid_file="$project_root/runtime/gateway.pid"
gateway_log="$project_root/runtime/logs/gateway.log"
gateway_trace="${STONEAGE_GATEWAY_TRACE:-0}"
gateway_trace_battle="${STONEAGE_GATEWAY_TRACE_BATTLE:-0}"
gateway_args=()
if [[ -n "$gateway_routes" ]]; then
  gateway_args+=(-routes "$gateway_routes")
  # The first listener is used for the local readiness probe and status text.
  first_gateway_route="${gateway_routes%%;*}"
  gateway_listen="${first_gateway_route%%=*}"
else
  gateway_args+=(
    -listen "$gateway_listen"
    -upstream "$gateway_upstream"
  )
fi

case "$gateway_trace" in
  1|true|TRUE|yes|YES|on|ON)
    gateway_args+=(-trace)
    ;;
  0|false|FALSE|no|NO|off|OFF|"")
    ;;
  *)
    echo "STONEAGE_GATEWAY_TRACE must be a boolean value, got: $gateway_trace" >&2
    exit 2
    ;;
esac

case "$gateway_trace_battle" in
  1|true|TRUE|yes|YES|on|ON)
    gateway_args+=(-trace-battle)
    ;;
  0|false|FALSE|no|NO|off|OFF|"")
    ;;
  *)
    echo "STONEAGE_GATEWAY_TRACE_BATTLE must be a boolean value, got: $gateway_trace_battle" >&2
    exit 2
    ;;
esac

mkdir -p "$project_root/build" "$project_root/runtime/logs"

if [[ ! -x "$runtime_root/saac/saacjt.exe" || ! -x "$runtime_root/gmsv/gmsvjt.exe" ]]; then
  "$project_root/scripts/prepare-legacy-server.sh" \
    "$project_root/server/legacy/source/2.5" "$runtime_root"
fi

STONEAGE_UPSTREAM_PORT="$upstream_port" STONEAGE_SAAC_PORT="$saac_port" \
  "$project_root/scripts/run-legacy-server.sh" "$runtime_root"

ready=0
for _ in {1..100}; do
  if nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1 && \
     nc -z 127.0.0.1 "$saac_port" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.1
done
if [[ "$ready" != "1" ]]; then
  echo "GMSV/SAAC did not become ready (GMSV 127.0.0.1:$upstream_port, SAAC 127.0.0.1:$saac_port)." >&2
  echo "Inspect $runtime_root/logs/gmsv.log and $runtime_root/logs/saac.log" >&2
  exit 1
fi

(cd "$project_root" && go build -mod=mod -o "$gateway_binary" ./cmd/stoneage-gateway)

gateway_running=0
if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    gateway_running=1
  fi
fi
if [[ "$gateway_running" != "1" ]]; then
  existing_gateway="$(pgrep -f 'stoneage-gateway.*(-listen|-routes)' | head -n 1 || true)"
  if [[ -n "$existing_gateway" ]] && kill -0 "$existing_gateway" 2>/dev/null; then
    gateway_pid="$existing_gateway"
    printf '%s\n' "$gateway_pid" >"$gateway_pid_file"
    gateway_running=1
  fi
fi
if [[ "$gateway_running" == "1" ]]; then
  echo "Protocol gateway is already running (PID $gateway_pid)."
else
  # The client is intentionally the foreground process of this launcher, but
  # the gateway must not share its terminal lifetime.  In particular Wine can
  # exit or restart while friends remain connected.  nohup plus a closed stdin
  # keeps the gateway alive after that foreground client (and its terminal)
  # goes away; stop-local.sh remains the explicit owner of shutdown.
  nohup "$gateway_binary" \
    "${gateway_args[@]}" \
    </dev/null >"$gateway_log" 2>&1 &
  gateway_pid=$!
  printf '%s\n' "$gateway_pid" >"$gateway_pid_file"
  gateway_ready=0
  for _ in {1..40}; do
    if kill -0 "$gateway_pid" 2>/dev/null && \
       nc -z "${gateway_listen%:*}" "${gateway_listen##*:}" >/dev/null 2>&1; then
      gateway_ready=1
      break
    fi
    sleep 0.05
  done
  if [[ "$gateway_ready" != "1" ]]; then
    echo "Protocol gateway failed to start; inspect $gateway_log" >&2
    exit 1
  fi
  if [[ -n "$gateway_routes" ]]; then
    echo "Started protocol gateway routes $gateway_routes (PID $gateway_pid, trace=$gateway_trace)."
  else
    echo "Started protocol gateway $gateway_listen -> $gateway_upstream (PID $gateway_pid, trace=$gateway_trace)."
  fi
fi

patch_args=(
  --source "$project_root/runtime/legacy-client/sa_2903.exe"
  --output "$project_root/runtime/legacy-client/sa_2903-local.exe"
  --bypass-wgs
)
client_servers_file="${STONEAGE_CLIENT_SERVERS_FILE:-$project_root/runtime/client-servers.toml}"
if [[ -n "${STONEAGE_CLIENT_SERVERS_FILE:-}" || -f "$client_servers_file" || -n "${STONEAGE_CLIENT_SERVERS_URL:-}" ]]; then
  if [[ -n "${STONEAGE_CLIENT_SERVERS_FILE:-}" || -f "$client_servers_file" ]]; then
    patch_args+=(--servers-file "$client_servers_file")
  fi
else
  patch_args+=(
    --host "${STONEAGE_CLIENT_HOST:-127.0.0.1}"
    --port "${STONEAGE_CLIENT_PORT:-9065}"
  )
fi
if [[ -n "${STONEAGE_CLIENT_SERVERS_URL:-}" ]]; then
  patch_args+=(--servers-url "$STONEAGE_CLIENT_SERVERS_URL")
fi
"$project_root/scripts/patch-legacy-client.py" "${patch_args[@]}"

echo "Services are ready. Starting the StoneAge 2.5 client."
exec "$project_root/scripts/run-legacy-client-wine.sh"
