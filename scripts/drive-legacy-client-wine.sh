#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
wine_bin="${STONEAGE_WINE:-$(command -v wine || true)}"
wine_prefix="${STONEAGE_WINEPREFIX:-$project_root/runtime/wine-prefix-zhcn}"
wine_locale="${STONEAGE_WINE_LOCALE:-zh_CN.GBK}"
driver="$project_root/build/tools/stoneage-client-driver.exe"

if [[ -z "$wine_bin" || ! -x "$wine_bin" ]]; then
  echo "Wine is not installed or STONEAGE_WINE is not executable." >&2
  exit 1
fi
if [[ ! -f "$driver" ]]; then
  "$project_root/scripts/build-legacy-client-driver.sh"
fi

export WINEPREFIX="$wine_prefix"
export LANG="$wine_locale"
export LC_ALL="$wine_locale"
export WINEDEBUG="${WINEDEBUG:--all}"

exec "$wine_bin" "$driver" "$@"

