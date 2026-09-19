#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
test_root="$(mktemp -d "$repo_root/.tmp-clean-images-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

fail()
{
    echo "clean-images test failed: $*" >&2
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
    if [[ -s "$file" ]] && grep -F -- "$needle" "$file" >/dev/null; then
        fail "$file contains unexpected text: $needle"
    fi
}

assert_empty_file()
{
    local file="$1"
    [[ ! -s "$file" ]] || fail "$file is not empty"
}

mock_docker="$test_root/mock-docker"
cat >"$mock_docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail

printf '%s\n' "$*" >>"$MOCK_CALLS_FILE"

if [[ "${1:-}" == image && "${2:-}" == inspect ]]; then
    reference="${!#}"
    while IFS=$'\t' read -r image_reference image_id; do
        [[ -n "$image_reference" ]] || continue
        if [[ "$image_reference" == "$reference" ]]; then
            printf '%s\n' "$image_id"
            exit 0
        fi
    done <"$MOCK_CURRENT_IMAGES_FILE"
    exit 1
fi

if [[ "${1:-}" == container && "${2:-}" == ls ]]; then
    cat "$MOCK_CONTAINER_IDS_FILE"
    exit 0
fi

if [[ "${1:-}" == container && "${2:-}" == inspect ]]; then
    container_id="${!#}"
    while IFS=$'\t' read -r listed_container image_id; do
        [[ -n "$listed_container" ]] || continue
        if [[ "$listed_container" == "$container_id" ]]; then
            printf '%s\n' "$image_id"
            exit 0
        fi
    done <"$MOCK_CONTAINER_IMAGES_FILE"
    exit 1
fi

if [[ "${1:-}" == image && "${2:-}" == ls ]]; then
    cat "$MOCK_IMAGE_LIST_FILE"
    exit 0
fi

if [[ "${1:-}" == image && "${2:-}" == rm ]]; then
    for argument in "$@"; do
        case "$argument" in
            --force|-f)
                echo "mock rejects forced image removal" >&2
                exit 2
                ;;
        esac
    done
    printf '%s\n' "${!#}" >>"$MOCK_REMOVED_FILE"
    exit 0
fi

echo "unexpected docker invocation: $*" >&2
exit 2
MOCK
chmod 700 "$mock_docker"

write_env()
{
    local case_root="$1"
    cat >"$case_root/.env" <<EOF
STONEAGE_VERSION=v0.1.16
STONEAGE_CONTROL_IMAGE=registry.example/stoneage/control-plane
STONEAGE_LEGACY_IMAGE=registry.example/stoneage/legacy-runtime
EOF
}

setup_case()
{
    local name="$1"
    case_root="$test_root/$name"
    mkdir -p "$case_root"
    write_env "$case_root"

    current_file="$case_root/current-images"
    container_ids_file="$case_root/container-ids"
    container_images_file="$case_root/container-images"
    image_list_file="$case_root/image-list"
    removed_file="$case_root/removed"
    calls_file="$case_root/calls"
    : >"$current_file"
    : >"$container_ids_file"
    : >"$container_images_file"
    : >"$image_list_file"
    : >"$removed_file"
    : >"$calls_file"

    export MOCK_CURRENT_IMAGES_FILE="$current_file"
    export MOCK_CONTAINER_IDS_FILE="$container_ids_file"
    export MOCK_CONTAINER_IMAGES_FILE="$container_images_file"
    export MOCK_IMAGE_LIST_FILE="$image_list_file"
    export MOCK_REMOVED_FILE="$removed_file"
    export MOCK_CALLS_FILE="$calls_file"
    export STONEAGE_DOCKER_BIN="$mock_docker"
}

populate_normal_state()
{
    local case_root="$1"
    local current_file="$case_root/current-images"
    local container_ids_file="$case_root/container-ids"
    local container_images_file="$case_root/container-images"
    local image_list_file="$case_root/image-list"

    printf '%s\t%s\n' \
        'registry.example/stoneage/control-plane:v0.1.16' 'sha256:control-current' \
        'registry.example/stoneage/control-plane:v0.1.15' 'sha256:control-old' \
        'registry.example/stoneage/control-plane:v0.1.14' 'sha256:control-current' \
        'registry.example/stoneage/legacy-runtime:v0.1.16' 'sha256:legacy-current' \
        'registry.example/stoneage/legacy-runtime:v0.1.15' 'sha256:legacy-old' \
        'registry.example/stoneage/legacy-runtime:v0.1.14' 'sha256:legacy-stopped' \
        >"$current_file"
    printf '%s\n' 'stopped-container-id' >"$container_ids_file"
    printf '%s\t%s\n' 'stopped-container-id' 'sha256:legacy-stopped' >"$container_images_file"
    cat >"$image_list_file" <<'EOF'
registry.example/stoneage/control-plane v0.1.16 sha256:control-current
registry.example/stoneage/control-plane v0.1.15 sha256:control-old
registry.example/stoneage/control-plane v0.1.14 sha256:control-current
registry.example/stoneage/control-plane latest sha256:control-latest
registry.example/stoneage/legacy-runtime v0.1.16 sha256:legacy-current
registry.example/stoneage/legacy-runtime v0.1.15 sha256:legacy-old
registry.example/stoneage/legacy-runtime v0.1.14 sha256:legacy-stopped
registry.example/other unrelated sha256:unrelated
EOF
}

populate_keep_state()
{
    local case_root="$1"
    local current_file="$case_root/current-images"
    local image_list_file="$case_root/image-list"

    populate_normal_state "$case_root"
    printf '%s\t%s\n' \
        'registry.example/stoneage/control-plane:v0.1.24' 'sha256:control-kept' \
        'registry.example/stoneage/control-plane:v0.1.23' 'sha256:control-kept-2' \
        'registry.example/stoneage/legacy-runtime:v0.1.24' 'sha256:legacy-kept' \
        'registry.example/stoneage/legacy-runtime:v0.1.23' 'sha256:legacy-kept-2' \
        >>"$current_file"
    cat >>"$image_list_file" <<'EOF'
registry.example/stoneage/control-plane v0.1.24 sha256:control-kept
registry.example/stoneage/control-plane v0.1.23 sha256:control-kept-2
registry.example/stoneage/legacy-runtime v0.1.24 sha256:legacy-kept
registry.example/stoneage/legacy-runtime v0.1.23 sha256:legacy-kept-2
EOF
}

run_helper()
{
    local env_file="$1"
    shift
    bash "$repo_root/bin/stoneage" clean --env "$env_file" "$@"
}

# --dry-run previews exact eligible references and must not call image rm.
setup_case dry-run
populate_normal_state "$case_root"
run_helper "$case_root/.env" --dry-run >"$case_root/stdout" 2>"$case_root/stderr"
assert_file_contains "$case_root/stdout" 'registry.example/stoneage/control-plane:v0.1.15'
assert_file_contains "$case_root/stdout" 'registry.example/stoneage/legacy-runtime:v0.1.15'
assert_empty_file "$removed_file"
assert_file_not_contains "$calls_file" 'image rm'

# The default mode removes only v* tags from the two exact configured
# repositories. A tag that shares the current image's ID goes as a name --
# docker drops the tag and keeps the image, which the current version and the
# running container still reference. Stopped-container images, current tags,
# non-v* tags and unrelated repositories remain untouched.
setup_case apply
populate_normal_state "$case_root"
run_helper "$case_root/.env" >"$case_root/stdout" 2>"$case_root/stderr"
assert_file_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.15'
assert_file_contains "$removed_file" 'registry.example/stoneage/legacy-runtime:v0.1.15'
assert_file_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.14'
assert_file_contains "$case_root/stdout" 'Untag: registry.example/stoneage/control-plane:v0.1.14'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.16'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/control-plane:latest'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/legacy-runtime:v0.1.14'
assert_file_not_contains "$removed_file" 'registry.example/other:unrelated'
assert_file_not_contains "$calls_file" '--force'

# A repeated --keep-version preserves that tag in both configured repositories,
# while another older release remains eligible for removal.
setup_case keep-version
populate_keep_state "$case_root"
run_helper "$case_root/.env" --keep-version v0.1.24 --keep-version v0.1.23 >"$case_root/stdout" 2>"$case_root/stderr"
assert_file_contains "$case_root/stdout" 'Keep requested image: registry.example/stoneage/control-plane:v0.1.24'
assert_file_contains "$case_root/stdout" 'Keep requested image: registry.example/stoneage/control-plane:v0.1.23'
assert_file_contains "$case_root/stdout" 'Keep requested image: registry.example/stoneage/legacy-runtime:v0.1.24'
assert_file_contains "$case_root/stdout" 'Keep requested image: registry.example/stoneage/legacy-runtime:v0.1.23'
assert_file_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.15'
assert_file_contains "$removed_file" 'registry.example/stoneage/legacy-runtime:v0.1.15'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.24'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/control-plane:v0.1.23'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/legacy-runtime:v0.1.24'
assert_file_not_contains "$removed_file" 'registry.example/stoneage/legacy-runtime:v0.1.23'

# --keep-version accepts only explicit release tags and rejects values before
# touching Docker, including an empty argument and interpolation syntax.
for value in '' 'v0.1' '${RELEASE_TAG}' '$(touch should-not-exist)'; do
    setup_case invalid-keep-version
    populate_normal_state "$case_root"
    if run_helper "$case_root/.env" --keep-version "$value" >"$case_root/stdout" 2>"$case_root/stderr"; then
        fail "invalid keep version was accepted: $value"
    fi
    assert_empty_file "$removed_file"
    assert_empty_file "$calls_file"
done

# If either current release reference is unavailable, the helper must stop
# before listing or removing any image.
setup_case current-missing
populate_normal_state "$case_root"
awk '!/legacy-runtime:v0.1.16/' "$case_root/current-images" >"$case_root/current-images.tmp"
mv "$case_root/current-images.tmp" "$case_root/current-images"
if run_helper "$case_root/.env" >"$case_root/stdout" 2>"$case_root/stderr"; then
    fail "missing current image was accepted"
fi
assert_empty_file "$removed_file"
assert_file_not_contains "$calls_file" 'image rm'
assert_file_not_contains "$calls_file" 'image ls'

# A missing or interpolated version must not silently use Compose defaults.
for value in '' '${RELEASE_TAG}' '$(touch should-not-exist)'; do
    setup_case invalid-version
    printf 'STONEAGE_VERSION=%s\n' "$value" >"$case_root/.env"
    if run_helper "$case_root/.env" >"$case_root/stdout" 2>"$case_root/stderr"; then
        fail "invalid version was accepted"
    fi
    assert_empty_file "$calls_file"
done

# Quoted values, comments and the final duplicate assignment follow dotenv.
setup_case quoted-env
populate_normal_state "$case_root"
printf "STONEAGE_VERSION=v0.0.1\nexport STONEAGE_VERSION = 'v0.1.16' # deployed\n" >>"$case_root/.env"
: >"$container_ids_file"
run_helper "$case_root/.env" --dry-run >"$case_root/stdout" 2>"$case_root/stderr"
assert_file_contains "$case_root/stdout" 'Keep configured image: registry.example/stoneage/control-plane:v0.1.16'
assert_empty_file "$removed_file"

help_output="$(bash "$repo_root/bin/clean-images.sh" --help)"
[[ "$help_output" == *'--keep-version TAG'* ]] || fail 'help does not document --keep-version'

echo "clean-images tests passed"
