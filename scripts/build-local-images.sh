#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
control_image="${STONEAGE_CONTROL_IMAGE:-stoneage-control-plane}:local"
legacy_image="${STONEAGE_LEGACY_IMAGE:-stoneage-legacy-runtime}:local"

docker build \
  --file "$project_root/deploy/linux/Dockerfile" \
  --tag "$control_image" \
  "$project_root"

docker build \
  --file "$project_root/deploy/linux/legacy-runtime.Dockerfile" \
  --tag "$legacy_image" \
  "$project_root"

echo "Built $control_image"
echo "Built $legacy_image"
