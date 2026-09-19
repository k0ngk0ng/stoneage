#!/usr/bin/env bash
# Build and install the sactl game client for the operator running it.
#
# The binary goes on PATH, and its configuration lives under the standard
# user paths so it works from any working directory:
#
#   binary   ${PREFIX:-$HOME/.local/bin}/sactl
#   config   ${XDG_CONFIG_HOME:-$HOME/.config}/sactl/sactl.toml
#   state    ${XDG_STATE_HOME:-$HOME/.local/state}/sactl/   (socket and logs)
#
# Usage:
#   scripts/install-sactl.sh [--prefix DIR] [--force]
#   scripts/install-sactl.sh --download [tag]     # fetch a published release
#
# A browser download carries com.apple.quarantine, and macOS kills a
# quarantined binary that is only ad-hoc signed (a plain "Killed: 9" with no
# message). Installing from this script clears that attribute, and
# --download fetches with curl, which never sets it in the first place.
#
# Builds with -mod=mod: this repository keeps its data under vendor/, which
# collides with Go's vendor convention, so the default module mode cannot be
# used.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prefix="${PREFIX:-$HOME/.local/bin}"
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/sactl"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/sactl"
force=0
download=0
tag=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) prefix="$2"; shift 2 ;;
    --force) force=1; shift ;;
    --download) download=1; shift ;;
    -h|--help) sed -n '2,22p' "${BASH_SOURCE[0]}"; exit 0 ;;
    v[0-9]*) tag="$1"; shift ;;
    *) echo "install-sactl: unknown argument $1" >&2; exit 2 ;;
  esac
done

mkdir -p "$prefix" "$config_dir" "$state_dir"
chmod 700 "$state_dir"

if [[ $download -eq 1 ]]; then
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; esac
  case "$os" in darwin|linux) ;; *) echo "install-sactl: unsupported OS $os" >&2; exit 2 ;; esac
  if [[ -z "$tag" ]]; then
    tag="$(curl -fsSL https://api.github.com/repos/k0ngk0ng/stoneage/releases/latest |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  fi
  [[ -n "$tag" ]] || { echo "install-sactl: cannot determine the latest release tag" >&2; exit 1; }
  archive="stoneage-sactl-${tag}-${os}-${arch}.tar.gz"
  url="https://github.com/k0ngk0ng/stoneage/releases/download/${tag}/${archive}"
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT
  echo "downloading ${tag} (${os}/${arch})..."
  # curl does not set com.apple.quarantine, unlike a browser download.
  curl -fsSL "$url" -o "$workdir/$archive"
  tar -xzf "$workdir/$archive" -C "$workdir"
  install -m 755 "$workdir/stoneage-sactl-${tag}-${os}-${arch}/sactl" "$prefix/sactl"
else
  if ! command -v go >/dev/null 2>&1; then
    echo "install-sactl: Go is required to build sactl (or use --download)" >&2
    exit 1
  fi
  echo "building sactl..."
  (cd "$root" && go build -mod=mod -trimpath -o "$prefix/sactl" ./cmd/sactl)
fi

# Gatekeeper: a quarantined ad-hoc signed binary is killed on launch with no
# message. Clearing the attribute keeps the installed client runnable.
if command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$prefix/sactl" 2>/dev/null || true
fi

config_path="$config_dir/sactl.toml"
if [[ -f "$config_path" && $force -ne 1 ]]; then
  echo "keeping existing config $config_path"
else
  sed -e "s|^socket_path = .*|socket_path = \"$state_dir/sactl.sock\"|" \
      "$root/config/sactl/sactl.toml.example" > "$config_path"
  chmod 600 "$config_path"
  echo "wrote $config_path"
fi

cat <<EOF

installed: $prefix/sactl
config:    $config_path
state:     $state_dir

next:
  1. edit the config: gateway address, account, password, character, and
     map_directory (a copy of the 2.5 server data directory; routing, warps
     and encounter lookups read it locally)
  2. check the client:  sactl version
  3. start the session holder:  sactl serve
  4. drive it from another terminal:  sactl status / observe / goto ...
EOF

case ":$PATH:" in
  *":$prefix:"*) ;;
  *) echo "note: $prefix is not on PATH; add it to your shell profile" ;;
esac
