#!/usr/bin/env bash
set -euo pipefail

source_root="${1:-vendor/upstream/2.5}"
image="${STONEAGE_BUILD_IMAGE:-alpine:3.22}"

if [[ ! -f "$source_root/gmsv/main.c" || ! -f "$source_root/saac/main.c" ]]; then
  echo "Legacy server sources are missing under $source_root." >&2
  echo "Run scripts/import-legacy.sh first." >&2
  exit 1
fi

source_root="$(cd "$source_root" && pwd)"
compat_root="$(cd "$(dirname "$0")/../server/legacy/modern" && pwd)"
quiz_rewriter="$(cd "$(dirname "$0")" && pwd)/modernize-quiz-state.py"
password_redactor="$(cd "$(dirname "$0")" && pwd)/redact-legacy-password-logs.py"
character_reader_rewriter="$(cd "$(dirname "$0")" && pwd)/modernize-character-file-read.py"
login_announcement_rewriter="$(cd "$(dirname "$0")" && pwd)/modernize-login-announcement.py"
nu_flow_control_rewriter="$(cd "$(dirname "$0")" && pwd)/modernize-nu-flow-control.py"

docker_args=(
  run --rm
  --volume "$source_root:/src"
  --volume "$compat_root:/modern:ro"
  --volume "$quiz_rewriter:/modernize-quiz-state.py:ro"
  --volume "$password_redactor:/redact-legacy-password-logs.py:ro"
  --volume "$character_reader_rewriter:/modernize-character-file-read.py:ro"
  --volume "$login_announcement_rewriter:/modernize-login-announcement.py:ro"
  --volume "$nu_flow_control_rewriter:/modernize-nu-flow-control.py:ro"
  --workdir /src
)

if [[ -n "${STONEAGE_SERVER_PLATFORM:-}" ]]; then
  docker_args+=(--platform "$STONEAGE_SERVER_PLATFORM")
fi

docker "${docker_args[@]}" "$image" sh /modern/build.sh
