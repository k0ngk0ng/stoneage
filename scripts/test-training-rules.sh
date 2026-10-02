#!/usr/bin/env bash
# Compare installed distribution images. Caller explicitly installs them first.
set -euo pipefail
game_image="${1:?installed game runtime image required}"
training_image="${2:?installed training image required}"
evidence="${3:?new evidence directory required}"
mkdir "$evidence"
rules() {
  docker run --rm --pull never --platform linux/amd64 --network none --read-only \
    --tmpfs /tmp:rw,size=64m --workdir /opt/stoneage/defaults/gmsv --entrypoint sh "$1" \
    /opt/stoneage/bin/prepare-battle-rules.sh setup.cf --digest-only
}
rules "$game_image" > "$evidence/game.txt" 2> "$evidence/game.log"
rules "$training_image" > "$evidence/training.txt" 2> "$evidence/training.log"
[[ "$(cat "$evidence/game.txt")" =~ ^[0-9a-f]{64}$ ]]
cmp "$evidence/game.txt" "$evidence/training.txt"
printf 'Actual game/training rule digests agree: %s\n' "$(cat "$evidence/game.txt")"
