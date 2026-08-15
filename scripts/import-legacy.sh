#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
archive_root="${STONEAGE_ARCHIVE:-$project_root/vendor/archives}"
destination="${1:-server/legacy/source}"

server_archive="$archive_root/stoneage2.5.tar.gz"
client_archive="$archive_root/sa_182_client_code.rar"

for source_archive in "$server_archive" "$client_archive"; do
  if [[ ! -r "$source_archive" ]]; then
    echo "Missing readable archive: $source_archive" >&2
    echo "Restore the archives under vendor/archives, or set STONEAGE_ARCHIVE." >&2
    exit 1
  fi
done

mkdir -p "$destination"

# bsdtar can report malformed historic filename encodings while still extracting
# every source file needed by the build. Preserve that diagnostic, then verify
# explicit sentinel files instead of trusting the archive exit code alone.
bsdtar -xf "$server_archive" -C "$destination" || true
bsdtar -xf "$client_archive" -C "$destination"

sentinels=(
  "$destination/2.5/gmsv/main.c"
  "$destination/2.5/gmsv/data/map/mapset.txt"
  "$destination/2.5/saac/main.c"
  "$destination/code_sa_client/SYSTEM/LSSPROTO_CLI.CPP"
  "$destination/code_sa_client/SYSTEM/DIRECTDRAW.CPP"
)

for sentinel in "${sentinels[@]}"; do
  if [[ ! -s "$sentinel" ]]; then
    echo "Import incomplete; expected file is missing: $sentinel" >&2
    exit 1
  fi
done

echo "Legacy sources imported into $destination"
