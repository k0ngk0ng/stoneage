#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 MESSAGE" >&2
  exit 2
fi
message="$1"
if [[ -z "$message" || "$message" == *$'\n'* || "$message" == *$'\r'* || "$message" == *$'\0'* ]]; then
  echo "notification must be a non-empty single line" >&2
  exit 2
fi
if (( ${#message} > 240 )); then
  echo "notification is limited to 240 characters" >&2
  exit 2
fi

notice_directory="${STONEAGE_RUNTIME_MOUNT:-/game}/gmsv"
mkdir -p "$notice_directory"
temporary="$(mktemp "$notice_directory/.admin-notice.XXXXXX")"
cleanup() { rm -f "$temporary"; }
trap cleanup EXIT
umask 077
printf '%s\n' "$message" >"$temporary"
mv -f "$temporary" "$notice_directory/admin-notice.txt"
trap - EXIT
