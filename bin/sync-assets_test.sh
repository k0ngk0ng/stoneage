#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
test_root="$(mktemp -d "$repo_root/.tmp-sync-assets-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

fail()
{
    echo "sync-assets test failed: $*" >&2
    exit 1
}

assert_file_contains()
{
    local file="$1"
    local needle="$2"
    grep -F -- "$needle" "$file" >/dev/null || fail "$file does not contain expected text: $needle"
}

assert_file_not_contains()
{
    local file="$1"
    local needle="$2"
    if grep -F -- "$needle" "$file" >/dev/null; then
        fail "$file contains unexpected text: $needle"
    fi
}

assert_arg()
{
    local needle="$1"
    grep -Fx -- "$needle" "$MOCK_ARGS_FILE" >/dev/null || fail "missing uploader argument: $needle"
}

mkdir -p "$test_root/sprites" "$test_root/client" "$test_root/config"
printf '%s\n' '{}' >"$test_root/sprites/manifest.json"
printf '%s\n' '{}' >"$test_root/sprites/sprites.json"
printf '%s\n' '[static]' 'assets_directory = "/unused"' >"$test_root/config/web.toml"
printf '%s\n' 'native-access-key' >"$test_root/access-key"
printf '%s\n' 'native-access-secret' >"$test_root/access-secret"
chmod 600 "$test_root/access-key" "$test_root/access-secret"

env_file="$test_root/.env"
cat >"$env_file" <<EOF
STONEAGE_SPRITES_ROOT=$test_root/sprites
STONEAGE_CLIENT_DATA_ROOT=$test_root/client
STONEAGE_WEB_CONFIG_FILE=$test_root/config/web.toml
STONEAGE_ASSET_SYNC_WORKERS=7
STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE=$test_root/access-key
STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE=$test_root/access-secret
EOF

mock_uploader="$test_root/stoneage-assets-sync"
cat >"$mock_uploader" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$@" >"$MOCK_ARGS_FILE"
printf '%s\n' "$(printenv STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE || true)" >"$MOCK_KEY_FILE"
printf '%s\n' "$(printenv STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE || true)" >"$MOCK_SECRET_FILE"
printf '%s\n' "$(printenv AWS_ACCESS_KEY_ID || true)" >"$MOCK_AWS_ENV_FILE"
MOCK
chmod 700 "$mock_uploader"

mock_docker="$test_root/docker"
cat >"$mock_docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' invoked >"$MOCK_DOCKER_CALLED"
exit 99
MOCK
chmod 700 "$mock_docker"

export STONEAGE_ENV_FILE="$env_file"
export STONEAGE_ASSET_SYNC_BIN="$mock_uploader"
export STONEAGE_DOCKER_BIN="$mock_docker"
export MOCK_ARGS_FILE="$test_root/args"
export MOCK_KEY_FILE="$test_root/key-path"
export MOCK_SECRET_FILE="$test_root/secret-path"
export MOCK_AWS_ENV_FILE="$test_root/aws-env"
export MOCK_DOCKER_CALLED="$test_root/docker-called"

"$repo_root/bin/sync-assets.sh" --workers 11 >"$test_root/normal.stdout" 2>"$test_root/normal.stderr"
assert_arg "-config"
assert_arg "$test_root/config/web.toml"
assert_arg "-assets"
assert_arg "$test_root/sprites"
assert_arg "-client-data"
assert_arg "$test_root/client"
assert_arg "-workers"
assert_arg "11"
assert_file_not_contains "$MOCK_ARGS_FILE" "native-access-key"
assert_file_not_contains "$MOCK_ARGS_FILE" "native-access-secret"
assert_file_contains "$MOCK_KEY_FILE" "$test_root/access-key"
assert_file_contains "$MOCK_SECRET_FILE" "$test_root/access-secret"
assert_file_not_contains "$MOCK_AWS_ENV_FILE" "native-access"
assert_file_not_contains "$test_root/normal.stdout" "native-access"
[[ ! -e "$MOCK_DOCKER_CALLED" ]] || fail "Docker was invoked"
assert_file_contains "$test_root/normal.stdout" "Client assets published."

: >"$MOCK_ARGS_FILE"
"$repo_root/bin/sync-assets.sh" --dry-run >"$test_root/dry.stdout" 2>"$test_root/dry.stderr"
assert_arg "-dry-run"
assert_arg "-workers"
assert_arg "7"
assert_file_not_contains "$MOCK_ARGS_FILE" "native-access-key"
assert_file_not_contains "$MOCK_ARGS_FILE" "native-access-secret"
[[ ! -e "$MOCK_DOCKER_CALLED" ]] || fail "Docker was invoked during dry-run"
assert_file_contains "$test_root/dry.stdout" "Client asset dry-run completed; no objects uploaded."
assert_file_not_contains "$test_root/dry.stdout" "Client assets published."

missing_bin="$test_root/missing-uploader"
if STONEAGE_ASSET_SYNC_BIN="$missing_bin" "$repo_root/bin/sync-assets.sh" --dry-run >"$test_root/missing.stdout" 2>"$test_root/missing.stderr"; then
    fail "missing native uploader was accepted"
fi
assert_file_contains "$test_root/missing.stderr" "Native asset uploader not found"
assert_file_contains "$test_root/missing.stderr" "STONEAGE_ASSET_SYNC_BIN"
assert_file_contains "$test_root/missing.stderr" "Docker/GHCR is not used"

echo "sync-assets tests passed"

