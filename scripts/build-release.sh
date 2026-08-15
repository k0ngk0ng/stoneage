#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
dist_root="$project_root/dist"
stage_root="$dist_root/.stage"
version="${STONEAGE_RELEASE_VERSION:-2.5-revival-20260813}"
mac_bundle="$dist_root/StoneAge Revival.app"
windows_stage="$stage_root/StoneAge-Revival-Win11-x64"
linux_stage="$stage_root/StoneAge-Revival-Server-linux-amd64"
mac_resources="$mac_bundle/Contents/Resources"

require_file()
{
  if [[ ! -f "$1" ]]; then
    echo "Missing release input: $1" >&2
    exit 1
  fi
}

require_dir()
{
  if [[ ! -d "$1" ]]; then
    echo "Missing release input: $1" >&2
    exit 1
  fi
}

require_file "$project_root/runtime/legacy-client/sa_2903.exe"
require_file "$project_root/runtime/legacy-client/sa_2903-local.exe"
require_file "$project_root/runtime/legacy-client/ddraw.dll"
require_file "$project_root/runtime/legacy-server/gmsv/gmsvjt.exe"
require_file "$project_root/runtime/legacy-server/saac/saacjt.exe"
require_dir "$project_root/runtime/legacy-client/data"
require_dir "$project_root/runtime/legacy-client/map"

verify_sha256()
{
  path="$1"
  expected="$2"
  actual="$(shasum -a 256 "$path" | awk '{print $1}')"
  if [[ "$actual" != "$expected" ]]; then
    echo "SHA-256 mismatch for $path" >&2
    echo "Expected: $expected" >&2
    echo "Actual:   $actual" >&2
    exit 1
  fi
}

# These two files are the immutable archived client and the reviewed local
# protocol patch. Refuse to publish an accidentally modified executable.
verify_sha256 "$project_root/runtime/legacy-client/sa_2903.exe" \
  "9abb989b207d2db6eeb0a95fbc13cfb681eca8a20e9ddc169edb64a03497a3ee"
verify_sha256 "$project_root/runtime/legacy-client/sa_2903-local.exe" \
  "b6710978f34a88089d28ef6b8ac75167f7edaa4a3c8ef5591accf2b8059b6230"

rm -rf "$stage_root" "$mac_bundle"
mkdir -p \
  "$stage_root" \
  "$mac_bundle/Contents/MacOS" \
  "$mac_resources/bin" \
  "$windows_stage/bin" \
  "$linux_stage/bin"

echo "Building Go gateways..."
(cd "$project_root" && go test -mod=mod ./...)
(cd "$project_root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -mod=mod -trimpath -ldflags "-s -w" \
  -o "$mac_resources/bin/stoneage-gateway" ./cmd/stoneage-gateway)
(cd "$project_root" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -mod=mod -trimpath -ldflags "-s -w" \
  -o "$windows_stage/bin/stoneage-gateway.exe" ./cmd/stoneage-gateway)
(cd "$project_root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -mod=mod -trimpath -ldflags "-s -w" \
  -o "$linux_stage/bin/stoneage-gateway" ./cmd/stoneage-gateway)
(cd "$project_root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -mod=mod -trimpath -ldflags "-s -w" \
  -o "$linux_stage/bin/stoneage-admin" ./cmd/stoneage-admin)
(cd "$project_root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -mod=mod -trimpath -ldflags "-s -w" \
  -o "$linux_stage/bin/stoneage-operator" ./cmd/stoneage-operator)

echo "Building Linux amd64 legacy server..."
STONEAGE_SERVER_PLATFORM=linux/amd64 \
  "$project_root/scripts/build-legacy-server.sh" "$project_root/server/legacy/source/2.5"

server_arch="$(file "$project_root/server/legacy/source/2.5/gmsv/gmsvjt.exe")"
if [[ "$server_arch" != *"x86-64"* ]]; then
  echo "Expected a Linux x86-64 GMSV, got: $server_arch" >&2
  exit 1
fi

linux_runtime="$linux_stage/runtime/legacy-server"
"$project_root/scripts/prepare-legacy-server.sh" \
  "$project_root/server/legacy/source/2.5" "$linux_runtime"

sanitize_server_runtime()
{
  runtime="$1"

  # Definitions and binaries are publishable; crash dumps, diagnostic logs,
  # archived test accounts and hand-made configuration backups are not.
  rm -f "$runtime/saac/core" "$runtime/saac/badpetstring.txt"
  find "$runtime" -type f \( \
    -name '*.bak' -o \
    -name '*.legacy-test-backup' -o \
    -name '*.pre-gameplay-test' \
  \) -delete
  find "$runtime/saac/log" -type f -delete
  find "$runtime/logs" -type f -delete
  find "$runtime/gmsv/log" -type f ! -name log.cf -delete

  # prepare-legacy-server.sh creates these writable stores empty. Keeping the
  # cleanup explicit prevents a future implementation change from leaking a
  # developer's accounts, characters, mail, locks or family data.
  for writable_store in \
    "$runtime/saac/char" \
    "$runtime/saac/char_sleep" \
    "$runtime/saac/db" \
    "$runtime/saac/lock" \
    "$runtime/saac/mail" \
    "$runtime/saac/data/family" \
    "$runtime/saac/data/fmpointdir" \
    "$runtime/saac/data/fmsmemodir"; do
    find "$writable_store" -type f -delete
  done
}

install_demo_character()
{
  runtime="$1"
  mkdir -p "$runtime/saac/char/0x18"
  install -m 0644 \
    "$project_root/runtime/legacy-server/saac/char/0x18/probe.0.char" \
    "$runtime/saac/char/0x18/probe.0.char"
}

sanitize_server_runtime "$linux_runtime"

# Keep the working macOS runtime native to Apple Silicon. The release build
# above temporarily replaces the source-tree binaries with amd64 artifacts.
echo "Restoring local Linux arm64 legacy server..."
"$project_root/scripts/build-legacy-server.sh" "$project_root/server/legacy/source/2.5"
"$project_root/scripts/prepare-legacy-server.sh" \
  "$project_root/server/legacy/source/2.5" "$stage_root/local-arm64-runtime"
install -m 0755 "$stage_root/local-arm64-runtime/gmsv/gmsvjt.exe" \
  "$project_root/runtime/legacy-server/gmsv/gmsvjt.exe"
install -m 0755 "$stage_root/local-arm64-runtime/saac/saacjt.exe" \
  "$project_root/runtime/legacy-server/saac/saacjt.exe"
sanitize_server_runtime "$stage_root/local-arm64-runtime"

arm_server_arch="$(file "$stage_root/local-arm64-runtime/gmsv/gmsvjt.exe")"
if [[ "$arm_server_arch" != *"ARM aarch64"* ]]; then
  echo "Expected a Linux ARM aarch64 GMSV, got: $arm_server_arch" >&2
  exit 1
fi

copy_client()
{
  destination="$1"
  mkdir -p "$destination"
  cp -R "$project_root/runtime/legacy-client/data" "$destination/"
  cp -R "$project_root/runtime/legacy-client/map" "$destination/"
  for file in \
    Stoneage2.ico sa_2903.exe sa_2903-local.exe \
    ddraw.dll ddraw.ini; do
    if [[ -f "$project_root/runtime/legacy-client/$file" ]]; then
      cp -p "$project_root/runtime/legacy-client/$file" "$destination/$file"
    fi
  done
  # The launcher can regenerate sa_2903-local.exe from the immutable source
  # when a user selects a TOML/HTTP server list at startup.
  cp -p "$project_root/config/client-servers.toml.example" \
    "$destination/client-servers.toml.example"

  # These files are recreated by the client. They can contain the last login,
  # password, chat history, mail, album and per-player preferences from the
  # developer's test session, so a release must start without them.
  rm -f "$destination"/CHAT_*.TXT
  rm -f \
    "$destination/data/savedata.dat" \
    "$destination/data/chathis.dat" \
    "$destination/data/chatreg.dat" \
    "$destination/data/mail.dat" \
    "$destination/data/album.dat" \
    "$destination/data"/album_*.dat \
    "$destination/data/AISetting.dat"
}

copy_runtime_support()
{
  destination="$1"
  mkdir -p "$destination/scripts" "$destination/server/legacy/modern"
  cp -p \
    "$project_root/scripts/run-legacy-server.sh" \
    "$project_root/scripts/stop-legacy-server.sh" \
    "$destination/scripts/"
  cp -R "$project_root/server/legacy/modern/patches" \
    "$destination/server/legacy/modern/"
  cp -p \
    "$project_root/server/legacy/modern/run.sh" \
    "$project_root/server/legacy/modern/run-saac.sh" \
    "$project_root/server/legacy/modern/run-gmsv.sh" \
    "$project_root/server/legacy/modern/control.sh" \
    "$destination/server/legacy/modern/"
}

echo "Assembling macOS application..."
copy_client "$mac_resources/client"
mkdir -p "$mac_resources/scripts"
cp -p "$project_root/scripts/patch-legacy-client.py" \
  "$mac_resources/scripts/patch-legacy-client.py"
cp -R "$stage_root/local-arm64-runtime" "$mac_resources/server"
install_demo_character "$mac_resources/server"
copy_runtime_support "$mac_resources/support"
cp -p "$project_root/vendor/assets/fonts/NotoSansCJKsc-Regular.otf" \
  "$mac_resources/NotoSansCJKsc-Regular.otf"
cp -p "$project_root/vendor/assets/fonts/OFL.txt" "$mac_resources/OFL-font.txt"
cp -p "$project_root/vendor/upstream/cnc-ddraw-v7.1.0.0/LICENSE" \
  "$mac_resources/LICENSE-cnc-ddraw.txt"
cp -p "$project_root/docs/release-macos.md" "$mac_resources/使用說明.md"
cp -p "$project_root/docs/verified-features.md" "$mac_resources/已驗證功能.md"

sed "s/@VERSION@/$version/g" "$project_root/release/macos/Info.plist.in" \
  >"$mac_bundle/Contents/Info.plist"
install -m 0755 "$project_root/release/macos/stoneage-revival" \
  "$mac_bundle/Contents/MacOS/stoneage-revival"
install -m 0755 "$project_root/release/macos/stoneage-stop" \
  "$mac_resources/停止伺服器.command"

echo "Assembling Win11 package..."
copy_client "$windows_stage/client"
mkdir -p "$windows_stage/docs"
cp -p "$project_root/release/windows/Start-StoneAge.ps1" "$windows_stage/"
cp -p "$project_root/release/windows/Stop-StoneAge.ps1" "$windows_stage/"
cp -p "$project_root/release/windows/Configure-Server.ps1" "$windows_stage/"
cp -p "$project_root/scripts/patch-legacy-client.py" "$windows_stage/"
cp -p "$project_root/config/client-servers.toml.example" "$windows_stage/"
cp -p "$project_root/release/windows/README.txt" "$windows_stage/"
cp -p "$project_root/vendor/upstream/cnc-ddraw-v7.1.0.0/LICENSE" \
  "$windows_stage/LICENSE-cnc-ddraw.txt"
cp -p "$project_root/docs/windows.md" "$windows_stage/docs/Windows-11.md"
cp -p "$project_root/docs/networking.md" "$windows_stage/docs/Networking.md"
cp -p "$project_root/docs/verified-features.md" "$windows_stage/docs/Verified-Features.md"

echo "Assembling Linux amd64 server package..."
install_demo_character "$linux_runtime"
copy_runtime_support "$linux_stage"
install -m 0755 "$project_root/release/linux/start-server.sh" "$linux_stage/start-server.sh"
install -m 0755 "$project_root/release/linux/start-game.sh" "$linux_stage/start-game.sh"
install -m 0755 "$project_root/release/linux/start-gateway.sh" "$linux_stage/start-gateway.sh"
install -m 0755 "$project_root/release/linux/stop-server.sh" "$linux_stage/stop-server.sh"
install -m 0755 "$project_root/release/linux/stop-game.sh" "$linux_stage/stop-game.sh"
install -m 0755 "$project_root/release/linux/stop-gateway.sh" "$linux_stage/stop-gateway.sh"
install -m 0755 "$project_root/release/linux/status-server.sh" "$linux_stage/status-server.sh"
install -m 0755 "$project_root/release/linux/restart-server.sh" "$linux_stage/restart-server.sh"
install -m 0755 "$project_root/release/linux/restart-game.sh" "$linux_stage/restart-game.sh"
install -m 0755 "$project_root/release/linux/restart-gmsv.sh" "$linux_stage/restart-gmsv.sh"
install -m 0755 "$project_root/release/linux/restart-saac.sh" "$linux_stage/restart-saac.sh"
install -m 0755 "$project_root/release/linux/stop-gmsv.sh" "$linux_stage/stop-gmsv.sh"
install -m 0755 "$project_root/release/linux/stop-saac.sh" "$linux_stage/stop-saac.sh"
install -m 0755 "$project_root/release/linux/restart-gateway.sh" "$linux_stage/restart-gateway.sh"
install -m 0755 "$project_root/release/linux/send-notification.sh" "$linux_stage/send-notification.sh"
install -m 0755 "$project_root/release/linux/start-admin.sh" "$linux_stage/start-admin.sh"
install -m 0755 "$project_root/release/linux/stop-admin.sh" "$linux_stage/stop-admin.sh"
install -m 0755 "$project_root/release/linux/status-admin.sh" "$linux_stage/status-admin.sh"
cp -p "$project_root/release/linux/README.md" "$linux_stage/README.md"
cp -p "$project_root/docs/networking.md" "$linux_stage/NETWORKING.md"
cp -p "$project_root/docs/database.md" "$linux_stage/DATABASE.md"

printf '%s\n' "$version" >"$mac_resources/VERSION"
printf '%s\n' "$version" >"$windows_stage/VERSION"
printf '%s\n' "$version" >"$linux_stage/VERSION"

make_manifest()
{
  root="$1"
  manifest="$root/SHA256SUMS"
  (cd "$root" && find . -type f ! -name SHA256SUMS -print0 | \
    LC_ALL=C sort -z | xargs -0 shasum -a 256) >"$manifest"
}

codesign --force --sign - "$mac_resources/bin/stoneage-gateway"
make_manifest "$mac_bundle/Contents/Resources"
make_manifest "$windows_stage"
make_manifest "$linux_stage"

verify_release_tree()
{
  root="$1"
  unexpected="$(find "$root" -type f \( \
    -name '*.bak' -o \
    -name '*.legacy-test-backup' -o \
    -name '*.pre-gameplay-test' -o \
    -name 'core' -o \
    -name 'badpetstring.txt' -o \
    -name 'savedata.dat' -o \
    -name 'chathis.dat' -o \
    -name 'chatreg.dat' -o \
    -name 'mail.dat' -o \
    -name 'album.dat' -o \
    -name 'album_*.dat' -o \
    -name 'AISetting.dat' -o \
    -name 'CHAT_*.TXT' \
  \) -print -quit)"
  if [[ -n "$unexpected" ]]; then
    echo "Unexpected private/runtime file in release: $unexpected" >&2
    exit 1
  fi
}

verify_release_tree "$mac_bundle"
verify_release_tree "$windows_stage"
verify_release_tree "$linux_stage"

# Ad-hoc signing prevents modern macOS from treating the assembled bundle as
# structurally unsigned. It is intentionally not notarized or Developer-ID
# signed; the documentation explains the first-launch Gatekeeper behavior.
# This must run after the resource manifest is final.
codesign --force --sign - "$mac_bundle"

echo "Creating archives..."
rm -f \
  "$dist_root/StoneAge-Revival-macOS-arm64.zip" \
  "$dist_root/StoneAge-Revival-Win11-x64.zip" \
  "$dist_root/StoneAge-Revival-Server-linux-amd64.tar.gz" \
  "$dist_root/SHA256SUMS"
ditto -c -k --sequesterRsrc --keepParent "$mac_bundle" \
  "$dist_root/StoneAge-Revival-macOS-arm64.zip"
(cd "$stage_root" && zip -q -r -9 -X \
  "$dist_root/StoneAge-Revival-Win11-x64.zip" \
  "$(basename "$windows_stage")")
(cd "$stage_root" && COPYFILE_DISABLE=1 tar -czf \
  "$dist_root/StoneAge-Revival-Server-linux-amd64.tar.gz" \
  "$(basename "$linux_stage")")

(cd "$dist_root" && shasum -a 256 \
  StoneAge-Revival-macOS-arm64.zip \
  StoneAge-Revival-Win11-x64.zip \
  StoneAge-Revival-Server-linux-amd64.tar.gz >SHA256SUMS)

rm -rf "$stage_root"
echo "Release $version is ready under $dist_root"
