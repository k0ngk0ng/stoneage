#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
client_root="${STONEAGE_CLIENT_ROOT:-$project_root/runtime/legacy-client}"
compat_root="$project_root/client/compat/cnc-ddraw"
upstream_release="$project_root/vendor/upstream/cnc-ddraw-v7.1.0.0/release"
expected_dll_sha256="85e0f7d530dfda134793a57cb3e76b0287dcc96892ee57162dd68f47283b03a9"

if [[ ! -d "$client_root/data" || ! -d "$client_root/map" ]]; then
  echo "StoneAge 2.5 client data is missing under $client_root." >&2
  exit 1
fi
if [[ ! -f "$upstream_release/ddraw.dll" ]]; then
  echo "Pinned cnc-ddraw release is missing: $upstream_release/ddraw.dll" >&2
  exit 1
fi
if [[ ! -f "$compat_root/ddraw.ini" ]]; then
  echo "StoneAge cnc-ddraw profile is missing: $compat_root/ddraw.ini" >&2
  exit 1
fi

actual_dll_sha256="$(shasum -a 256 "$upstream_release/ddraw.dll" | awk '{print $1}')"
if [[ "$actual_dll_sha256" != "$expected_dll_sha256" ]]; then
  echo "Refusing an unknown cnc-ddraw DLL." >&2
  echo "Expected $expected_dll_sha256, got $actual_dll_sha256." >&2
  exit 1
fi

install -m 0644 "$upstream_release/ddraw.dll" "$client_root/ddraw.dll"
install -m 0644 "$compat_root/ddraw.ini" "$client_root/ddraw.ini"

echo "Prepared StoneAge 2.5 client with cnc-ddraw 7.1.0.0."
echo "client=$client_root"
echo "ddraw_sha256=$actual_dll_sha256"
