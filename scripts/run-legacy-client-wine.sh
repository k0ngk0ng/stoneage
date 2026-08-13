#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
client_root="${STONEAGE_CLIENT_ROOT:-$project_root/runtime/legacy-client}"
client_exe="${STONEAGE_CLIENT_EXE:-sa_2903-local.exe}"
wine_bin="${STONEAGE_WINE:-$(command -v wine || true)}"
wine_prefix="${STONEAGE_WINEPREFIX:-$project_root/runtime/wine-prefix-zhcn}"
wine_log="${STONEAGE_WINE_LOG:-$project_root/runtime/logs/wine-client.log}"
wine_desktop="${STONEAGE_WINE_DESKTOP:-}"
wine_locale="${STONEAGE_WINE_LOCALE:-zh_CN.GBK}"
wine_configurer="$project_root/scripts/configure-legacy-client-wine.sh"
client_preparer="$project_root/scripts/prepare-legacy-client.sh"
client_args=(
  updated
  realbin:15
  adrnbin:15
  sprbin:4
  spradrnbin:5
  encode:108
)

if [[ -n "${STONEAGE_CLIENT_ARGS:-}" ]]; then
  read -r -a client_args <<<"$STONEAGE_CLIENT_ARGS"
elif [[ -n "${STONEAGE_CLIENT_LAUNCH_TOKEN:-}" ]]; then
  # Backward compatibility for the earlier one-token launcher override.
  client_args=("$STONEAGE_CLIENT_LAUNCH_TOKEN")
fi

if [[ -z "$wine_bin" || ! -x "$wine_bin" ]]; then
  echo "Wine is not installed or STONEAGE_WINE is not executable." >&2
  exit 1
fi
if [[ ! -f "$client_root/$client_exe" ]]; then
  echo "Missing patched client: $client_root/$client_exe" >&2
  echo "Run scripts/patch-legacy-client.py first." >&2
  exit 1
fi

mkdir -p "$wine_prefix" "$(dirname "$wine_log")"
export WINEPREFIX="$wine_prefix"
# StoneAge 2.5 predates Unicode. Its executable and data files contain
# Simplified Chinese in Windows code page 936 (GBK), so the Wine prefix must
# be created under the matching locale. A prefix first created as en-US keeps
# ACP 1252 permanently and turns every legacy Chinese message into mojibake.
export LANG="$wine_locale"
export LC_ALL="$wine_locale"
# Current Wine on Apple Silicon uses the new WoW64 mode: the prefix itself is
# 64-bit but it can run this 32-bit x86 client. Older Wine builds that still
# require a pure win32 prefix can opt in explicitly.
if [[ -n "${STONEAGE_WINEARCH:-}" ]]; then
  export WINEARCH="$STONEAGE_WINEARCH"
fi
export WINEDEBUG="${WINEDEBUG:--all}"
export MVK_CONFIG_LOG_LEVEL="${MVK_CONFIG_LOG_LEVEL:-0}"
if [[ -n "${WINEDLLOVERRIDES:-}" ]]; then
  export WINEDLLOVERRIDES="ddraw=n,b;$WINEDLLOVERRIDES"
else
  export WINEDLLOVERRIDES="ddraw=n,b"
fi

"$client_preparer"
"$wine_configurer" "$wine_prefix"

cd "$client_root"
echo "Starting $client_exe with Wine and cnc-ddraw (locale $wine_locale, CP936); log: $wine_log"
echo "Client arguments: ${client_args[*]}"
if [[ -n "$wine_desktop" ]]; then
  exec "$wine_bin" explorer "/desktop=$wine_desktop" "$client_exe" "${client_args[@]}" >"$wine_log" 2>&1
fi
exec "$wine_bin" "$client_exe" "${client_args[@]}" >"$wine_log" 2>&1
