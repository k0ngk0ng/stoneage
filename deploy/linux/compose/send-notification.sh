#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 MESSAGE" >&2
  exit 2
fi
message="$1"
if [[ -z "$message" || "$message" == *$'\n'* || "$message" == *$'\r'* ]]; then
  echo "notification must be a non-empty single line" >&2
  exit 2
fi
# The operator has already validated the UTF-8 character count and converted
# the argument to CP936. Check bytes here because Alpine's shell locale counts
# the encoded argument as bytes, while GMSV consumes a 1024-byte C line.
if (( ${#message} > 1023 )); then
	echo "notification is too large for the CP936 queue" >&2
	exit 2
fi

notice_directory="${STONEAGE_NOTICE_DIR:-${STONEAGE_RUNTIME_MOUNT:-/game}/gmsv}"
mkdir -p "$notice_directory"
temporary="$(mktemp "$notice_directory/.admin-notice.XXXXXX")"
cleanup() { rm -f "$temporary"; }
trap cleanup EXIT
umask 077
printf '%s\n' "$message" >"$temporary"
mv -f "$temporary" "$notice_directory/admin-notice.txt"
trap - EXIT
