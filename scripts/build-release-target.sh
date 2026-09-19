#!/usr/bin/env bash
# Build one release platform. CI fans these out onto separate runners.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${RELEASE_TAG:?RELEASE_TAG is required}"
[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release tag' >&2; exit 2; }
goos="${1:?target OS required}"
goarch="${2:?target architecture required}"
suffix=''
case "$goos/$goarch" in
  linux/amd64|darwin/amd64|darwin/arm64) ;;
  windows/amd64) suffix='.exe' ;;
  *) echo "Unsupported release target: $goos/$goarch" >&2; exit 2 ;;
esac
stage="${RELEASE_STAGE:-$PWD/build/release-stage}"
dist="${RELEASE_DIST:-$PWD/dist}"
mkdir -p "$stage" "$dist"

build_target() {
  local goos="$1"
  local goarch="$2"
  local suffix="$3"
  local package_root="$stage/stoneage-control-plane-${RELEASE_TAG}-${goos}-${goarch}"
  local bin_root="$package_root/bin"

  mkdir -p "$bin_root"
  commands=(stoneage-gateway stoneage-admin stoneage-operator stoneage-assets-sync)
  if [[ "$goos" == linux ]]; then
    # The embedded Web server is part of the Linux deployment.
    commands+=(stoneage-web)
  fi
  for command in "${commands[@]}"; do
    local output="$bin_root/$command$suffix"
    local package="./cmd/$command"
    local ldflags="-s -w"
    if [[ "$command" == stoneage-web ]]; then
      package="./client/web"
      ldflags+=" -X main.releaseVersion=$RELEASE_TAG"
    fi
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="$ldflags" -o "$output" "$package"
  done

  tar -czf "$dist/stoneage-assets-sync-${RELEASE_TAG}-${goos}-${goarch}.tar.gz" -C "$package_root" "bin/stoneage-assets-sync$suffix"

  cp README.md "$package_root/README.md"
  mkdir -p "$package_root/docs"
  cp docs/database.md docs/networking.md docs/development.md "$package_root/docs/"

  if [[ "$goos" == linux ]]; then
    cp release/linux/start-admin.sh \
      release/linux/stop-admin.sh \
      release/linux/status-admin.sh \
      release/linux/start-game.sh \
      release/linux/stop-game.sh \
      release/linux/start-gateway.sh \
      release/linux/stop-gateway.sh \
      release/linux/restart-server.sh \
      release/linux/restart-game.sh \
      release/linux/restart-gmsv.sh \
      release/linux/restart-saac.sh \
      release/linux/stop-gmsv.sh \
      release/linux/stop-saac.sh \
      release/linux/restart-gateway.sh \
      release/linux/send-notification.sh \
      "$package_root/"
  fi

  (cd "$stage" && tar -czf "$dist/stoneage-control-plane-${RELEASE_TAG}-${goos}-${goarch}.tar.gz" \
    "$(basename "$package_root")")
}

build_target "$goos" "$goarch" "$suffix"

# sactl is the headless game client an operator (or an AI player's executor)
# runs on their own machine, so it ships as its own archive: the server
# deployment bundle is not part of that workflow.
sactl_root="$stage/stoneage-sactl-${RELEASE_TAG}-${goos}-${goarch}"
mkdir -p "$sactl_root"
CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
  go build -trimpath -ldflags="-s -w -X main.version=${RELEASE_TAG}" -o "$sactl_root/sactl$suffix" ./cmd/sactl
cp config/sactl/sactl.toml.example "$sactl_root/sactl.toml.example"
cp docs/sactl.md "$sactl_root/sactl.md"
cp scripts/install-sactl.sh "$sactl_root/install-sactl.sh"
if [[ "$goos" == windows ]]; then
  (cd "$stage" && zip -q -r -9 "$dist/stoneage-sactl-${RELEASE_TAG}-windows-amd64.zip" \
    "stoneage-sactl-${RELEASE_TAG}-windows-amd64")
else
  (cd "$stage" && tar -czf "$dist/stoneage-sactl-${RELEASE_TAG}-${goos}-${goarch}.tar.gz" \
    "stoneage-sactl-${RELEASE_TAG}-${goos}-${goarch}")
fi
if [[ "$goos" == windows ]]; then
  (cd "$stage" && zip -q -r -9 "$dist/stoneage-control-plane-${RELEASE_TAG}-windows-amd64.zip" \
    "stoneage-control-plane-${RELEASE_TAG}-windows-amd64")
  rm -f "$dist/stoneage-control-plane-${RELEASE_TAG}-windows-amd64.tar.gz"
fi
if [[ "$goos" == linux ]]; then
  python3 scripts/package-deployment.py "$RELEASE_TAG" --output "$dist" \
    --uploader "$stage/stoneage-control-plane-${RELEASE_TAG}-linux-amd64/bin/stoneage-assets-sync"
fi
