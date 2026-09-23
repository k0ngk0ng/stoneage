#!/usr/bin/env bash
set -Eeuo pipefail
project_root="$(cd "$(dirname "$0")/.." && pwd)"
docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
output="$project_root/data/battle-training"
if [[ "${1:-}" == --output ]]; then
  [[ $# -ge 2 && -n "$2" ]] || { echo '--output needs a directory' >&2; exit 2; }
  output="$2"
  shift 2
fi
[[ "$output" == /* ]] || output="$PWD/$output"
install -d -m 700 "$output"
output="$(cd "$output" && pwd -P)"
image="${STONEAGE_BATTLE_DATASET_IMAGE:?use bin/stoneage battle-dataset}"
# It is impossible for this container to connect to production or save live
# characters: no network and only the dataset output is mounted writable.
exec "$docker_bin" run --rm --pull never --network none --read-only \
  --cap-drop ALL --security-opt no-new-privileges --cpus 2 --memory 1g \
  --tmpfs /tmp:rw,nosuid,nodev,size=64m \
  --mount "type=bind,src=$output,dst=/dataset" \
  -e STONEAGE_BATTLE_RECORD_DIR=/dataset \
  -e "STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES=${STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES:-1073741824}" \
  --entrypoint /opt/stoneage/bin/run-battle-dataset.sh "$image" "$@"
