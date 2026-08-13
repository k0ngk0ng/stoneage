#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
compiler="${STONEAGE_MINGW_CC:-$(command -v i686-w64-mingw32-gcc || true)}"
source_file="$project_root/tools/legacy-client-driver/main.c"
output_dir="$project_root/build/tools"
output_file="$output_dir/stoneage-client-driver.exe"

if [[ -z "$compiler" || ! -x "$compiler" ]]; then
  echo "The 32-bit MinGW compiler i686-w64-mingw32-gcc is required." >&2
  exit 1
fi

mkdir -p "$output_dir"
"$compiler" -std=c11 -O2 -Wall -Wextra \
  -Wl,--subsystem,console \
  -o "$output_file" "$source_file"

echo "Built $output_file"
