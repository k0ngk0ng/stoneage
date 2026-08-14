#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 MESSAGE" >&2
  exit 2
fi

message="$1"
if [[ -z "$message" ]]; then
  echo "notification is empty" >&2
  exit 2
fi
case "$message" in
  *$'\n'*|*$'\r'*)
    echo "notification must be a single line" >&2
    exit 2
    ;;
esac

package_root="$(cd "$(dirname "$0")" && pwd)"
notice_directory="$package_root/runtime/legacy-server/gmsv"
notice_file="$notice_directory/admin-notice.txt"
mkdir -p "$notice_directory"

# GMSV consumes this file in its main loop. Write beside it and rename so it
# never observes a partially written UTF-8 message.
temporary="$(mktemp "$notice_directory/.admin-notice.XXXXXX")"
cleanup() {
  rm -f "$temporary"
}
trap cleanup EXIT
umask 077
printf '%s\n' "$message" >"$temporary"
mv -f "$temporary" "$notice_file"
trap - EXIT
