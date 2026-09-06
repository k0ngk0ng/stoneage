#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
test_root="$(mktemp -d "$repo_root/.tmp-registry-login-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

fail()
{
    echo "registry-login test failed: $*" >&2
    exit 1
}

assert_file_contains()
{
    local file="$1"
    local needle="$2"
    grep -F -- "$needle" "$file" >/dev/null || fail "$file does not contain expected text"
}

assert_file_not_contains()
{
    local file="$1"
    local needle="$2"
    if grep -F -- "$needle" "$file" >/dev/null; then
        fail "$file contains sensitive or unexpected text"
    fi
}

assert_mode()
{
    local path="$1"
    local expected="$2"
    local actual
    if actual="$(stat -c '%a' "$path" 2>/dev/null)"; then
        :
    else
        actual="$(stat -f '%Lp' "$path")"
    fi
    [[ "$actual" == "$expected" ]] || fail "$path mode=$actual, want $expected"
}

mock_docker="$test_root/mock-docker"
cat >"$mock_docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "${DOCKER_CONFIG:-}" >"$MOCK_DOCKER_CONFIG_FILE"
printf '%s\n' "$@" >"$MOCK_DOCKER_ARGS_FILE"
cat >"$MOCK_DOCKER_PASSWORD_FILE"
if [[ "${MOCK_DOCKER_STATUS:-0}" != 0 ]]; then
    echo "mock docker failure" >&2
    exit "$MOCK_DOCKER_STATUS"
fi
MOCK
chmod 700 "$mock_docker"

# Sourcing defines the function but must not invoke Docker or create files.
source "$repo_root/bin/registry-login.sh"
project_root="$test_root/no-login"
docker_bin="$mock_docker"
export MOCK_DOCKER_CONFIG_FILE="$test_root/no-login-config"
export MOCK_DOCKER_ARGS_FILE="$test_root/no-login-args"
export MOCK_DOCKER_PASSWORD_FILE="$test_root/no-login-password"
stoneage_registry_login 2>"$test_root/no-login.stderr" || fail "missing credentials should be a no-op"
[[ ! -e "$project_root/config/registry/docker" ]] || fail "source/no-login created Docker config"
[[ ! -e "$test_root/no-login-password" ]] || fail "source/no-login invoked Docker"

# The standalone entrypoint follows the same no-op path when no credentials
# are present, without requiring a Docker daemon.
standalone_root="$test_root/standalone"
STONEAGE_PROJECT_ROOT="$standalone_root" STONEAGE_DOCKER_BIN="$mock_docker" \
    bash "$repo_root/bin/registry-login.sh" 2>"$test_root/standalone.stderr" || fail "standalone no-op failed"
[[ ! -e "$standalone_root/config/registry/docker" ]] || fail "standalone invocation created Docker config"

# A single supplied file is an actionable configuration error.
mkdir -p "$test_root/partial/config/registry"
printf '%s\n' 'alice' >"$test_root/partial/config/registry/username"
project_root="$test_root/partial"
if stoneage_registry_login 2>"$test_root/partial.stderr"; then
    fail "partial credentials were accepted"
fi
assert_file_contains "$test_root/partial.stderr" "token file is missing"

# Empty credentials must fail without passing the empty secret to Docker.
mkdir -p "$test_root/empty/config/registry"
printf '%s\n' 'alice' >"$test_root/empty/config/registry/username"
: >"$test_root/empty/config/registry/token"
project_root="$test_root/empty"
if stoneage_registry_login 2>"$test_root/empty.stderr"; then
    fail "empty token was accepted"
fi
assert_file_contains "$test_root/empty.stderr" "token file is empty"

# A valid login uses the project-local Docker config by default, tightens all
# credential/config permissions, and sends the token only on stdin.
mkdir -p "$test_root/success/config/registry"
printf '%s\n' 'alice' >"$test_root/success/config/registry/username"
printf '%s\n' 'ghp-test-secret' >"$test_root/success/config/registry/token"
chmod 644 "$test_root/success/config/registry/username" "$test_root/success/config/registry/token"
project_root="$test_root/success"
export MOCK_DOCKER_CONFIG_FILE="$test_root/success-docker-config"
export MOCK_DOCKER_ARGS_FILE="$test_root/success-docker-args"
export MOCK_DOCKER_PASSWORD_FILE="$test_root/success-docker-password"
unset DOCKER_CONFIG
stoneage_registry_login >"$test_root/success.stdout" 2>"$test_root/success.stderr"
default_docker_config="$test_root/success/config/registry/docker"
[[ "$(cat "$test_root/success-docker-config")" == "$default_docker_config" ]] || fail "default DOCKER_CONFIG was not used"
assert_file_contains "$test_root/success-docker-args" "login"
assert_file_contains "$test_root/success-docker-args" "ghcr.io"
assert_file_contains "$test_root/success-docker-args" "--username"
assert_file_contains "$test_root/success-docker-args" "alice"
assert_file_contains "$test_root/success-docker-args" "--password-stdin"
[[ "$(cat "$test_root/success-docker-password")" == 'ghp-test-secret' ]] || fail "token was not sent on stdin"
assert_mode "$test_root/success/config/registry" 700
assert_mode "$test_root/success/config/registry/username" 600
assert_mode "$test_root/success/config/registry/token" 600
assert_mode "$default_docker_config" 700
assert_file_not_contains "$test_root/success.stdout" 'ghp-test-secret'
assert_file_not_contains "$test_root/success.stderr" 'ghp-test-secret'

# The project-local Docker config takes precedence when credential files are
# present, so the subsequent Compose pull uses the cache just populated by the
# helper even if the caller inherited an unrelated DOCKER_CONFIG.
mkdir -p "$test_root/custom/config/registry"
printf '%s\n' 'bob' >"$test_root/custom/config/registry/username"
printf '%s\n' 'custom-secret' >"$test_root/custom/config/registry/token"
project_root="$test_root/custom"
export DOCKER_CONFIG="$test_root/custom-docker"
export MOCK_DOCKER_CONFIG_FILE="$test_root/custom-docker-config"
export MOCK_DOCKER_ARGS_FILE="$test_root/custom-docker-args"
export MOCK_DOCKER_PASSWORD_FILE="$test_root/custom-docker-password"
stoneage_registry_login
custom_project_docker_config="$test_root/custom/config/registry/docker"
[[ "$(cat "$test_root/custom-docker-config")" == "$custom_project_docker_config" ]] || fail "project Docker config was not selected"
[[ "$DOCKER_CONFIG" == "$custom_project_docker_config" ]] || fail "project Docker config was not exported"
[[ ! -e "$test_root/custom-docker" ]] || fail "external Docker config was used"
assert_mode "$custom_project_docker_config" 700

# Docker failures are returned and never echo the token in helper diagnostics.
export MOCK_DOCKER_STATUS=7
if stoneage_registry_login >"$test_root/failure.stdout" 2>"$test_root/failure.stderr"; then
    fail "Docker failure was swallowed"
fi
assert_file_contains "$test_root/failure.stderr" "Docker login failed"
assert_file_not_contains "$test_root/failure.stderr" 'custom-secret'

echo "registry-login tests passed"
