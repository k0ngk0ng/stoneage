#!/usr/bin/env bash
# Run on Linux with an already installed, matching-architecture training image.
# Exercises the actual distribution; never builds or downloads an image.
set -euo pipefail
cd "$(dirname "$0")/.."
image="${1:?installed ai-training image required}"
expected_arch="${2:-}"
expected_version="${3:-}"
[[ "$(uname -s)" == Linux ]] || { echo 'Training image validation requires a native Linux runner' >&2; exit 2; }
case "$(uname -m)" in
  x86_64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *) echo 'Unsupported training validation runner architecture' >&2; exit 2 ;;
esac
expected_arch="${expected_arch:-$host_arch}"
[[ "$expected_arch" == "$host_arch" ]] || { echo 'Training validation must run on the requested native architecture' >&2; exit 2; }
platform="linux/$expected_arch"
image_id="$(docker image inspect --format '{{.Id}}' "$image")"
[[ "$(docker image inspect --format '{{index .Config.Labels "org.stoneage.training.schema"}}' "$image_id")" == 1 ]]
[[ "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image_id")" == "$platform" ]] || { echo 'Installed training image does not match the native validation runner' >&2; exit 2; }
if [[ -n "$expected_version" ]]; then
  [[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image_id")" == "$expected_version" ]] || { echo 'Training image release label differs from the requested version' >&2; exit 2; }
fi
mkdir -p build
stage="$(mktemp -d "$PWD/build/training-image.XXXXXX")"
volume="stoneage-${stage##*/}"
container="${volume}-export"
cleanup() {
  local result=$?
  docker rm -f "$container" >/dev/null 2>&1 || true
  if [[ $result == 0 ]]; then
    docker volume rm "$volume" >/dev/null
  else
    echo "Training image check failed; evidence: $stage; volume: $volume" >&2
  fi
}
trap cleanup EXIT
docker image inspect --format '{"image_id":{{json .Id}},"os":{{json .Os}},"architecture":{{json .Architecture}}}' "$image_id" > "$stage/image.json"

# Copy the actual shipped CLI and skill. The container is never started.
docker create --pull never --platform "$platform" --network none --read-only --name "$container" "$image_id" >/dev/null
docker cp "$container:/opt/stoneage/bin/sactl" "$stage/sactl"
docker cp "$container:/opt/stoneage/skills/sactl" "$stage/skill"
docker rm "$container" >/dev/null
diff -qr .agents/skills/sactl "$stage/skill"
"$stage/sactl" version > "$stage/client-version.txt"
if [[ -n "$expected_version" ]]; then
  [[ "$(cat "$stage/client-version.txt")" == "sactl $expected_version" ]] || { echo 'Shipped training CLI version differs from the requested release' >&2; exit 2; }
fi

# Host CLI creates a pinned Docker worker configuration and checks real metadata.
"$stage/sactl" ai environment init --image "$image_id" --directory "$stage/runtime" > "$stage/environment-created.json"
"$stage/sactl" ai environment check --environment "$stage/runtime/environment.json" > "$stage/environment-checked.json" 2> "$stage/environment-check.log"
python3 scripts/test-training-worker-lifecycle.py "$stage/sactl" "$image_id" "$stage/worker-lifecycle"

docker volume create "$volume" >/dev/null
run_training() {
  docker run --rm -i --pull never --platform "$platform" --network none --read-only --cpus 2 --memory 2g \
    --tmpfs /tmp:rw,size=64m --mount "type=volume,src=$volume,dst=/data" "$image_id" "$@"
}
run_training experiment --output experiment.json --train-groups 4 --validation-groups 2 --test-groups 2 --seed 751 --pet-skills 1,2,20 > "$stage/experiment.log"
run_training train --experiment experiment.json --data-dir training --warmup-matches 0 --batch-matches 2 --batches 1 --seed 757 > "$stage/train.log"
run_training train --data-dir training --resume --batches 1 > "$stage/resume.log"
run_training export-model --data-dir training --output exported-model.json > "$stage/export.log"

# Persisted volume contents must be usable outside the training container.
docker create --pull never --platform "$platform" --network none --read-only --name "$container" \
  --mount "type=volume,src=$volume,dst=/data,readonly" "$image_id" >/dev/null
docker cp "$container:/data/exported-model.json" "$stage/model.json"
docker cp "$container:/data/experiment.json" "$stage/experiment.json"
docker rm "$container" >/dev/null
"$stage/sactl" ai evaluate --environment "$stage/runtime/environment.json" \
  --experiment "$stage/experiment.json" --model "$stage/model.json" --split validation \
  --opponent basic --output "$stage/validation.json" > "$stage/evaluate.log"
"$stage/sactl" ai verify-evaluation --report "$stage/validation.json" > "$stage/verified.json"
printf 'Training image CLI, skill, initialization, volume resume and exported-model evaluation passed: %s\n' "$stage"
