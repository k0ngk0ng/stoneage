#!/usr/bin/env bash
set -euo pipefail

source_root="${1:-vendor/upstream/2.5}"
runtime_root="${2:-runtime/legacy-server}"

required=(
  "$source_root/saac/saacjt.exe"
  "$source_root/gmsv/gmsvjt.exe"
  "$source_root/gmsv/setup.cf"
  "$source_root/gmsv/data/map/mapset.txt"
)

for path in "${required[@]}"; do
  if [[ ! -s "$path" ]]; then
    echo "Missing legacy server artifact: $path" >&2
    echo "Import and build the 2.5 server before preparing its runtime." >&2
    exit 1
  fi
done

project_root="$(cd "$(dirname "$0")/.." && pwd)"
source_root="$(cd "$source_root" && pwd)"
mkdir -p "$runtime_root"
runtime_root="$(cd "$runtime_root" && pwd)"

mkdir -p \
  "$runtime_root/saac/db/int" \
  "$runtime_root/saac/db/string" \
  "$runtime_root/saac/char" \
  "$runtime_root/saac/char_sleep" \
  "$runtime_root/saac/log" \
  "$runtime_root/saac/lock" \
  "$runtime_root/saac/mail" \
  "$runtime_root/saac/data/family" \
  "$runtime_root/saac/data/fmpointdir" \
  "$runtime_root/saac/data/fmsmemodir" \
  "$runtime_root/gmsv/log" \
  "$runtime_root/gmsv/lostpet" \
  "$runtime_root/logs"

install -m 0755 "$source_root/saac/saacjt.exe" "$runtime_root/saac/saacjt.exe"
install -m 0755 "$source_root/gmsv/gmsvjt.exe" "$runtime_root/gmsv/gmsvjt.exe"
install -m 0644 \
  "$project_root/server/legacy/config/acserv.local.cf" \
  "$runtime_root/saac/acserv.cf"

# These are immutable game definitions in the local runtime. Player and
# account data live in the separately-created SAAC directories above.
cp -R "$source_root/gmsv/data" "$runtime_root/gmsv/"
for directory in Dengon Schedule; do
  if [[ -d "$source_root/gmsv/$directory" ]]; then
    cp -R "$source_root/gmsv/$directory" "$runtime_root/gmsv/"
  fi
done
for file in announce.txt loopannounce.txt lockip.txt; do
  if [[ -f "$source_root/gmsv/$file" ]]; then
    install -m 0644 "$source_root/gmsv/$file" "$runtime_root/gmsv/$file"
  fi
done
if [[ -f "$source_root/gmsv/log.cf" ]]; then
  install -m 0644 "$source_root/gmsv/log.cf" "$runtime_root/gmsv/log/log.cf"
fi

# Retain the archive's complete feature configuration, changing only the
# local process identity and connection settings. awk keeps this portable
# between the macOS and Linux implementations of sed.
awk '
BEGIN {
  replacement["debuglevel"] = "1"
  replacement["acserv"] = "127.0.0.1"
  replacement["acservport"] = "9300"
  replacement["acpasswd"] = "test"
  replacement["gameservname"] = "stoneage-local"
  replacement["gameservid"] = "stoneage-local"
  replacement["port"] = "9065"
  # The legacy server indexes Connect[] directly by the operating-system file
  # descriptor. It opens more than twenty log files before accepting players,
  # so the archive default fdnum=10 rejects every client as "server full".
  replacement["fdnum"] = "128"
  replacement["topdir"] = "."
  replacement["storedir"] = "../saac/char"
}
{
  split($0, fields, "=")
  key = fields[1]
  if (key in replacement) {
    print key "=" replacement[key]
  } else {
    print $0
  }
}
' "$source_root/gmsv/setup.cf" >"$runtime_root/gmsv/setup.cf"

echo "Prepared clean local runtime at $runtime_root"
echo "No archived character or account records were copied."
