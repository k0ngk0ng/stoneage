#!/usr/bin/env bash

# Authenticate Docker to the private GitHub Container Registry without putting
# a token in an argument, environment dump, or the repository. The function
# is deliberately defined without executing at source time so deployment
# entrypoints can source this file and call it when they pull images.
stoneage_registry_login()
{
    if (( $# != 0 )); then
        echo "usage: stoneage_registry_login" >&2
        return 2
    fi

    local helper_dir project_root_value docker_bin_value
    local username_file token_file docker_config
    local username token

    helper_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)" || return 1
    project_root_value="${project_root:-${STONEAGE_PROJECT_ROOT:-$(cd -- "$helper_dir/.." && pwd)}}"
    docker_bin_value="${docker_bin:-${STONEAGE_DOCKER_BIN:-docker}}"

    username_file="${STONEAGE_REGISTRY_USERNAME_FILE:-$project_root_value/config/registry/username}"
    token_file="${STONEAGE_REGISTRY_TOKEN_FILE:-$project_root_value/config/registry/token}"
    docker_config="$project_root_value/config/registry/docker"

    if [[ ! -e "$username_file" && ! -L "$username_file" &&
        ! -e "$token_file" && ! -L "$token_file" ]]; then
        echo "GHCR credentials are not configured; keeping the existing Docker authentication." >&2
        return 0
    fi

    if [[ ! -e "$username_file" || -L "$username_file" ]]; then
        if [[ -L "$username_file" ]]; then
            echo "GHCR username file must not be a symbolic link: $username_file" >&2
        else
            echo "GHCR username file is missing: $username_file" >&2
        fi
        return 2
    fi
    if [[ ! -e "$token_file" || -L "$token_file" ]]; then
        if [[ -L "$token_file" ]]; then
            echo "GHCR token file must not be a symbolic link: $token_file" >&2
        else
            echo "GHCR token file is missing: $token_file" >&2
        fi
        return 2
    fi
    if [[ ! -f "$username_file" ]]; then
        echo "GHCR username path is not a regular file: $username_file" >&2
        return 2
    fi
    if [[ ! -f "$token_file" ]]; then
        echo "GHCR token path is not a regular file: $token_file" >&2
        return 2
    fi

    # Credential files are operator-provided. Tighten their permissions before
    # reading them, and tighten each containing directory as well.
    if ! chmod 700 "$(dirname -- "$username_file")" "$(dirname -- "$token_file")" ||
        ! chmod 600 "$username_file" "$token_file"; then
        echo "cannot secure GHCR credential file permissions" >&2
        return 2
    fi

    if ! username="$(cat -- "$username_file")" || ! token="$(cat -- "$token_file")"; then
        echo "cannot read GHCR credential files" >&2
        return 2
    fi
    username="${username%$'\r'}"
    token="${token%$'\r'}"
    if [[ "$username" == *$'\n'* || "$username" == *$'\r'* ]]; then
        echo "GHCR username file must contain one line: $username_file" >&2
        return 2
    fi
    if [[ "$token" == *$'\n'* || "$token" == *$'\r'* ]]; then
        echo "GHCR token file must contain one line: $token_file" >&2
        return 2
    fi
    if [[ -z "${username//[[:space:]]/}" ]]; then
        echo "GHCR username file is empty: $username_file" >&2
        return 2
    fi
    if [[ -z "${token//[[:space:]]/}" ]]; then
        echo "GHCR token file is empty: $token_file" >&2
        return 2
    fi

    if [[ -e "$docker_config" || -L "$docker_config" ]]; then
        if [[ -L "$docker_config" || ! -d "$docker_config" ]]; then
            echo "DOCKER_CONFIG is not a directory: $docker_config" >&2
            return 2
        fi
    else
        if ! mkdir -p -- "$docker_config"; then
            echo "cannot create Docker config directory: $docker_config" >&2
            return 2
        fi
    fi
    if ! chmod 700 "$docker_config"; then
        echo "cannot secure Docker config directory: $docker_config" >&2
        return 2
    fi

    # Keep the project-local auth cache available to the caller as well as to
    # this login command. The deployment artifact owns this cache alongside
    # its credential files, so a caller's unrelated DOCKER_CONFIG cannot make
    # the subsequent Compose pull miss the newly stored login.
    export DOCKER_CONFIG="$docker_config"
    if ! "$docker_bin_value" login ghcr.io \
        --username "$username" --password-stdin <<<"$token"; then
        echo "GHCR Docker login failed for username from $username_file." >&2
        return 1
    fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    stoneage_registry_login "$@"
fi
