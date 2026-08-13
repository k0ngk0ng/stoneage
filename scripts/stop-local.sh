#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
gateway_pid_file="$project_root/runtime/gateway.pid"
wine_prefix="${STONEAGE_WINEPREFIX:-$project_root/runtime/wine-prefix-zhcn}"
wineserver_bin="${STONEAGE_WINESERVER:-$(command -v wineserver || true)}"

if [[ -n "$wineserver_bin" && -x "$wineserver_bin" ]] && \
   pgrep -f 'sa_2903-local[.]exe' >/dev/null 2>&1; then
  WINEPREFIX="$wine_prefix" "$wineserver_bin" -k
  WINEPREFIX="$wine_prefix" "$wineserver_bin" -w
  echo "Stopped the client in its dedicated Wine prefix."
fi

if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    kill "$gateway_pid"
    for _ in {1..50}; do
      if ! kill -0 "$gateway_pid" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
    echo "Stopped protocol gateway PID $gateway_pid."
  fi
  rm -f "$gateway_pid_file"
else
  gateway_pid="$(pgrep -f 'stoneage-gateway.*-listen' | head -n 1 || true)"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    kill "$gateway_pid"
    echo "Stopped protocol gateway PID $gateway_pid."
  fi
fi

"$project_root/scripts/stop-legacy-server.sh"
