#!/usr/bin/env bash
set -Eeuo pipefail

project_root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
env_file="${STONEAGE_ENV_FILE:-$project_root/.env}"
docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
dry_run=0
keep_versions=()
release_tag_re='^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9._-]+)?$'
while (( $# )); do
    case "$1" in
        --dry-run) dry_run=1 ;;
        --env)
            [[ $# -ge 2 ]] || { echo '--env requires a file' >&2; exit 2; }
            env_file="$2"; shift ;;
        --keep-version)
            [[ $# -ge 2 ]] || { echo '--keep-version requires a release tag' >&2; exit 2; }
            [[ "$2" =~ $release_tag_re ]] || {
                echo "Invalid --keep-version tag: $2" >&2
                exit 2
            }
            keep_versions+=("$2"); shift ;;
        --keep-version=*)
            keep_version="${1#*=}"
            [[ "$keep_version" =~ $release_tag_re ]] || {
                echo "Invalid --keep-version tag: $keep_version" >&2
                exit 2
            }
            keep_versions+=("$keep_version") ;;
        -h|--help)
            echo 'Usage: bin/stoneage clean [--dry-run] [--env FILE] [--keep-version TAG]...'
            echo 'Remove unused old StoneAge release images; preserve the configured version, requested release tags, and all container images.'
            exit 0 ;;
        *) echo "Unknown clean option: $1" >&2; exit 2 ;;
    esac
    shift
done
[[ -f "$env_file" ]] || { echo "Environment file not found: $env_file" >&2; exit 2; }

# Read literal deployment values without executing shell code or loading secrets.
# Duplicate assignments follow dotenv's last-value rule. Interpolation is
# deliberately rejected below: deletion must have an unambiguous keep target.
env_value() {
    awk -v key="$1" '
        {
            sub(/\r$/, "")
            line=$0
            sub(/^[[:space:]]*(export[[:space:]]+)?/, "", line)
            pos=index(line, "=")
            if (!pos) next
            name=substr(line, 1, pos-1)
            sub(/[[:space:]]+$/, "", name)
            if (name != key) next
            value=substr(line, pos+1)
            sub(/^[[:space:]]+/, "", value)
            sub(/[[:space:]]+#.*$/, "", value)
            sub(/[[:space:]]+$/, "", value)
            if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) value=substr(value, 2, length(value)-2)
            result=value
        }
        END { print result }
    ' "$env_file"
}

version="$(env_value STONEAGE_VERSION)"
[[ "$version" =~ $release_tag_re ]] || {
    echo 'STONEAGE_VERSION must be an explicit release tag in the env file (for example v0.1.17).' >&2
    exit 2
}
control_repo="$(env_value STONEAGE_CONTROL_IMAGE)"
legacy_repo="$(env_value STONEAGE_LEGACY_IMAGE)"
control_repo="${control_repo:-ghcr.io/k0ngk0ng/stoneage/control-plane}"
legacy_repo="${legacy_repo:-ghcr.io/k0ngk0ng/stoneage/legacy-runtime}"
for repo in "$control_repo" "$legacy_repo"; do
    [[ "$repo" =~ ^[a-z0-9][a-z0-9._:/-]*$ && "${repo##*/}" != *:* ]] || {
        echo 'Image repository settings must be literal repository names without tags or digests.' >&2
        exit 2
    }
done

configured_ids=""
for repo in "$control_repo" "$legacy_repo"; do
    ref="$repo:$version"
    if ! id="$("$docker_bin" image inspect --format '{{.Id}}' "$ref")" || [[ -z "$id" ]]; then
        echo "Configured image is missing: $ref. Deploy the configured version before cleaning." >&2
        exit 1
    fi
    configured_ids+="$id"$'\n'
    echo "Keep configured image: $ref"
done

container_image_ids() {
    local containers container
    containers="$("$docker_bin" container ls -aq)" || return 1
    for container in $containers; do
        "$docker_bin" container inspect --format '{{.Image}}' "$container" || return 1
    done
}
contains_id() { [[ $'\n'"$1"$'\n' == *$'\n'"$2"$'\n'* ]]; }
is_kept_version() {
    local candidate
    (( ${#keep_versions[@]} > 0 )) || return 1
    for candidate in "${keep_versions[@]}"; do
        [[ "$candidate" == "$1" ]] && return 0
    done
    return 1
}
used_ids="$(container_image_ids)"
images="$("$docker_bin" image ls --no-trunc --format '{{.Repository}} {{.Tag}} {{.ID}}')"
count=0
failed=0
while read -r repo tag id; do
    [[ "$repo" == "$control_repo" || "$repo" == "$legacy_repo" ]] || continue
    [[ "$tag" =~ $release_tag_re ]] || continue
    [[ "$tag" != "$version" ]] || continue
    ref="$repo:$tag"
    if is_kept_version "$tag"; then
        echo "Keep requested image: $ref"
        continue
    fi
    if contains_id "$configured_ids" "$id" || contains_id "$used_ids" "$id"; then
        echo "Keep referenced image: $ref"
        continue
    fi
    if (( dry_run )); then
        echo "Would remove: $ref"
    else
        # Recheck references before each deletion. Remove by tag, never by ID
        # or force, so unrelated tags, containers, volumes and build cache stay.
        used_ids="$(container_image_ids)"
        current_id="$("$docker_bin" image inspect --format '{{.Id}}' "$ref")"
        if [[ "$current_id" != "$id" ]] || contains_id "$used_ids" "$id"; then
            echo "Keep changed/referenced image: $ref"
            continue
        fi
        echo "Remove: $ref"
        if ! "$docker_bin" image rm "$ref"; then failed=1; continue; fi
    fi
    count=$((count+1))
done <<< "$images"
if (( dry_run )); then echo "Dry run: $count old release tag(s) eligible."
else echo "Removed $count old release tag(s)."; fi
exit "$failed"
