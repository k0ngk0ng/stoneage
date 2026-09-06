#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
entrypoint="$repo_root/server/legacy/modern/runtime-entrypoint.sh"
test_root="$(mktemp -d "$repo_root/.tmp-runtime-entrypoint-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

fail()
{
    echo "runtime-entrypoint test failed: $*" >&2
    exit 1
}

assert_file_equals()
{
    local expected="$1"
    local actual="$2"
    cmp -s "$expected" "$actual" || fail "$actual does not match $expected"
}

assert_file_contains()
{
    local file="$1"
    local expected="$2"
    grep -Fx -- "$expected" "$file" >/dev/null || fail "$file does not contain expected content"
}

defaults="$test_root/defaults"
mkdir -p "$defaults/gmsv/data"
printf '%s\n' 'gmsv-binary' >"$defaults/gmsv/gmsvjt.exe"
printf '%s\n' 'image-default-log-config' >"$defaults/gmsv/log.cf"
printf '%s\n' 'badpet-default' >"$defaults/gmsv/badpetstring.txt"
printf '%s\n' 'image-default-setup' >"$defaults/gmsv/setup.cf"

run_gmsv_init()
{
    local game_root="$1"
    STONEAGE_GAME_ROOT="$game_root" \
        STONEAGE_RUNTIME_ROLE=gmsv \
        STONEAGE_GMSV_CONFIG= \
        STONEAGE_SAAC_CONFIG= \
        STONEAGE_LOG_ROOT= \
        STONEAGE_DEFAULTS_ROOT="$defaults" \
        "$entrypoint" true
}

# A fresh bind-mounted runtime gets both the legacy root file and the nested
# path consumed by setup.cf, seeded from the image default.
empty_root="$test_root/empty"
run_gmsv_init "$empty_root"
assert_file_contains "$empty_root/gmsv/log.cf" 'image-default-log-config'
assert_file_equals "$empty_root/gmsv/log.cf" "$empty_root/gmsv/log/log.cf"

# A user-edited root-level file wins over the image default and becomes the
# initial nested config.
root_custom="$test_root/root-custom"
mkdir -p "$root_custom/gmsv"
printf '%s\n' 'operator-root-log-config' >"$root_custom/gmsv/log.cf"
run_gmsv_init "$root_custom"
assert_file_contains "$root_custom/gmsv/log.cf" 'operator-root-log-config'
assert_file_contains "$root_custom/gmsv/log/log.cf" 'operator-root-log-config'

# Once the nested file exists, it is authoritative and must survive even when
# the root-level compatibility file contains different operator content.
nested_custom="$test_root/nested-custom"
mkdir -p "$nested_custom/gmsv/log"
printf '%s\n' 'operator-root-log-config' >"$nested_custom/gmsv/log.cf"
printf '%s\n' 'operator-nested-log-config' >"$nested_custom/gmsv/log/log.cf"
run_gmsv_init "$nested_custom"
assert_file_contains "$nested_custom/gmsv/log.cf" 'operator-root-log-config'
assert_file_contains "$nested_custom/gmsv/log/log.cf" 'operator-nested-log-config'

echo "runtime-entrypoint tests passed"
