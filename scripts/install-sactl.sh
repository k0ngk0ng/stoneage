#!/usr/bin/env bash
# Install sactl and its Codex/Claude Code skills on macOS or Linux.
# Usage: install-sactl.sh --download [vX.Y.Z] [--cdn-base https://cdn.example/game]
#        install-sactl.sh [--prefix DIR] [--force] [--no-skills]
# --force replaces the config template; existing config is otherwise preserved.
set -euo pipefail
script_dir=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
install_home="${SACTL_INSTALL_HOME:-$HOME}"
prefix="${PREFIX:-$install_home/.local/bin}"
config_dir="${XDG_CONFIG_HOME:-$install_home/.config}/sactl"
state_dir="${XDG_STATE_HOME:-$install_home/.local/state}/sactl"
force=0; download=0; skills=1; tag=""; cdn=""; bundle=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) prefix="${2:?--prefix requires a directory}"; shift 2 ;;
    --cdn-base) cdn="${2:?--cdn-base requires a URL}"; cdn="${cdn%/}"; shift 2 ;;
    --force) force=1; shift ;;
    --no-skills) skills=0; shift ;;
    --download) download=1; shift ;;
    -h|--help) echo 'Install sactl + Codex/Claude skills: --download [vX.Y.Z] [--cdn-base URL] [--prefix DIR] [--force] [--no-skills]'; exit 0 ;;
    v[0-9]*) tag="$1"; shift ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [[ -n "$cdn" && "$cdn" != https://* ]]; then echo 'CDN URL must use HTTPS' >&2; exit 2; fi
if [[ $download == 1 ]]; then
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"; arch="$(uname -m)"
  case "$arch" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo "Unsupported architecture: $arch" >&2; exit 2 ;; esac
  case "$os" in darwin|linux) ;; *) echo "Unsupported OS: $os" >&2; exit 2 ;; esac
  if [[ -z "$tag" ]]; then
    if [[ -n "$cdn" ]]; then
      tag="$(curl -fsSL "$cdn/downloads/sactl/latest.json" | sed -n 's/.*"version": *"\([^"]*\)".*/\1/p')"
    else
      tag="$(curl -fsSL https://api.github.com/repos/k0ngk0ng/stoneage/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
    fi
  fi
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release version' >&2; exit 2; }
  base="https://github.com/k0ngk0ng/stoneage/releases/download/$tag"
  [[ -z "$cdn" ]] || base="$cdn/downloads/sactl/$tag"
  archive="stoneage-sactl-${tag}-${os}-${arch}.tar.gz"
  workdir="$(mktemp -d)"; trap 'rm -rf "$workdir"' EXIT
  echo "Downloading $tag ($os/$arch)…"
  curl -fsSL "$base/$archive" -o "$workdir/$archive"
  curl -fsSL "$base/SHA256SUMS" -o "$workdir/SHA256SUMS"
  expected="$(awk -v name="$archive" '{f=$2; sub(/^\.\//,"",f); if(f==name)print $1}' "$workdir/SHA256SUMS")"
  [[ "$expected" =~ ^[a-f0-9]{64}$ ]] || { echo 'Missing or invalid SHA-256' >&2; exit 1; }
  if command -v shasum >/dev/null 2>&1; then actual="$(shasum -a 256 "$workdir/$archive" | awk '{print $1}')"
  else actual="$(sha256sum "$workdir/$archive" | awk '{print $1}')"; fi
  [[ "$actual" == "$expected" ]] || { echo 'Archive SHA-256 mismatch; nothing installed' >&2; exit 1; }
  tar -xzf "$workdir/$archive" -C "$workdir"
  bundle="$workdir/stoneage-sactl-${tag}-${os}-${arch}"
elif [[ -n "$script_dir" && -f "$script_dir/sactl" ]]; then
  bundle="$script_dir"
else
  [[ -n "$script_dir" ]] || { echo 'Use --download for a streamed installer' >&2; exit 2; }
  root="$(cd "$script_dir/.." && pwd)"
  command -v go >/dev/null || { echo 'Go is required for source builds; use --download instead' >&2; exit 1; }
  workdir="$(mktemp -d)"; trap 'rm -rf "$workdir"' EXIT
  bundle="$workdir/source"; mkdir -p "$bundle/skills"
  (cd "$root" && go build -mod=readonly -trimpath -o "$bundle/sactl" ./cmd/sactl)
  cp "$root/config/sactl/sactl.toml.example" "$bundle/sactl.toml.example"
  cp -R "$root/.agents/skills/sactl" "$bundle/skills/sactl"
fi
[[ -f "$bundle/sactl" && -f "$bundle/sactl.toml.example" ]] || { echo 'Incomplete client package' >&2; exit 1; }
if [[ $skills == 1 ]]; then
  [[ -f "$bundle/skills/sactl/SKILL.md" ]] || { echo 'This release has no bundled skill; select a newer release or use --no-skills' >&2; exit 1; }
  # Never follow an existing skill-directory symlink when updating files.
  for target in "$install_home/.agents/skills/sactl" "$install_home/.claude/skills/sactl"; do
    current="$target"
    while [[ "$current" != / ]]; do
      [[ ! -L "$current" ]] || { echo "Refusing symlink: $current" >&2; exit 1; }; current="$(dirname "$current")"
    done
    if [[ -d "$target" && -n "$(find "$target" -type l -print -quit)" ]]; then echo "Refusing skill containing symlinks: $target" >&2; exit 1; fi
  done
fi
mkdir -p "$prefix" "$config_dir" "$state_dir"; chmod 700 "$state_dir"
install -m 755 "$bundle/sactl" "$prefix/sactl"
if command -v xattr >/dev/null 2>&1; then xattr -d com.apple.quarantine "$prefix/sactl" 2>/dev/null || true; fi
config_path="$config_dir/sactl.toml"
if [[ ! -f "$config_path" || $force == 1 ]]; then
  # Escape paths for TOML and sed, including macOS home paths with spaces.
  socket="$(printf '%s' "$state_dir/sactl.sock" | sed 's/\\/\\\\/g; s/"/\\"/g; s/[&|]/\\&/g')"
  sed "s|^socket_path = .*|socket_path = \"$socket\"|" "$bundle/sactl.toml.example" > "$config_path"
  chmod 600 "$config_path"
fi
if [[ $skills == 1 ]]; then
  for target in "$install_home/.agents/skills/sactl" "$install_home/.claude/skills/sactl"; do
    mkdir -p "$target"
    cp -R "$bundle/skills/sactl/." "$target/"
    echo "Skill installed: $target"
  done
fi
printf '\nInstalled: %s\nConfig: %s\n' "$prefix/sactl" "$config_path"
echo 'Next: configure your game address/account/character, then run sactl serve.'
echo 'Skills are available in a new Codex/Claude Code session; no game login was started.'
case ":$PATH:" in *":$prefix:"*) ;; *) printf 'Add to your shell PATH: export PATH="%s:$PATH"\n' "$prefix" ;; esac
