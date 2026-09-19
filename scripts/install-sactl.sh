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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) prefix="$2"; shift 2 ;;
    --force) force=1; shift ;;
    -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "install-sactl: unknown argument $1" >&2; exit 2 ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "install-sactl: Go is required to build sactl" >&2
  exit 1
fi

mkdir -p "$prefix" "$config_dir" "$state_dir"
chmod 700 "$state_dir"

echo "building sactl..."
(cd "$root" && go build -mod=mod -trimpath -o "$prefix/sactl" ./cmd/sactl)

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
  2. start the session holder:  sactl serve
  3. drive it from another terminal:  sactl status / observe / goto ...
EOF

case ":$PATH:" in
  *":$prefix:"*) ;;
  *) echo "note: $prefix is not on PATH; add it to your shell profile" ;;
esac
