#!/usr/bin/env bash
# Render the Scoop manifest for a released sactl build and push it to the
# bucket, the Windows counterpart of the Homebrew tap.
#
# Usage:
#   scripts/publish-scoop-manifest.sh <tag>            # publish
#   scripts/publish-scoop-manifest.sh <tag> --dry-run  # print the manifest
set -euo pipefail

repo="k0ngk0ng/stoneage"
bucket="k0ngk0ng/scoop-bucket"
tag="${1:?release tag required, for example v0.1.49}"
mode="${2:-publish}"
version="${tag#v}"
archive="stoneage-sactl-${tag}-windows-amd64"
zip_name="${archive}.zip"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

if [[ -n "${TAP_GITHUB_TOKEN:-}" ]]; then
  GH_TOKEN="$TAP_GITHUB_TOKEN" gh release download "$tag" -R "$repo" -p SHA256SUMS --clobber -O "$workdir/SHA256SUMS"
else
  gh release download "$tag" -R "$repo" -p SHA256SUMS --clobber -O "$workdir/SHA256SUMS"
fi
# Anchor on the archive name so the server bundles in SHA256SUMS cannot match.
hash="$(awk -v pattern="$zip_name" '$2 ~ pattern {print $1}' "$workdir/SHA256SUMS" | head -1)"
[[ -n "$hash" ]] || { echo "publish-scoop-manifest: no checksum for $zip_name" >&2; exit 1; }

manifest="$workdir/sactl.json"
cat > "$manifest" <<EOF
{
  "version": "${version}",
  "description": "Headless StoneAge game client for terminals and agents",
  "homepage": "https://github.com/${repo}",
  "license": "Unknown",
  "url": "https://github.com/${repo}/releases/download/${tag}/${zip_name}",
  "hash": "${hash}",
  "extract_dir": "${archive}",
  "bin": "sactl.exe",
  "checkver": {
    "github": "https://github.com/${repo}"
  },
  "autoupdate": {
    "url": "https://github.com/${repo}/releases/download/v\$version/stoneage-sactl-v\$version-windows-amd64.zip",
    "extract_dir": "stoneage-sactl-v\$version-windows-amd64"
  }
}
EOF

if [[ "$mode" == "--dry-run" ]]; then
  cat "$manifest"
  exit 0
fi

if [[ -n "${TAP_GITHUB_TOKEN:-}" ]]; then
  clone_url="https://x-access-token:${TAP_GITHUB_TOKEN}@github.com/${bucket}.git"
else
  clone_url="https://github.com/${bucket}.git"
fi

git clone --depth 1 "$clone_url" "$workdir/bucket" >/dev/null 2>&1 ||
  { echo "publish-scoop-manifest: cannot clone $bucket (create it and add TAP_GITHUB_TOKEN)" >&2; exit 1; }
mkdir -p "$workdir/bucket/bucket"
cp "$manifest" "$workdir/bucket/bucket/sactl.json"

cd "$workdir/bucket"
git add bucket/sactl.json
if git diff --cached --quiet; then
  echo "manifest already at ${version}; nothing to publish"
  exit 0
fi
git -c user.name="sactl release" -c user.email="noreply@github.com" \
  commit -q -m "sactl ${version}"
git push -q origin HEAD
echo "published bucket/sactl.json ${version} to ${bucket}"
