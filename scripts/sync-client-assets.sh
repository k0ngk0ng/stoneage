#!/usr/bin/env bash
set -Eeuo pipefail

# Publish the complete browser client static tree as one explicit operation.
# This script is intentionally outside the admin UI: an administrator can
# restart game services, while a deployment operator/CI pipeline owns public
# client asset publication.  Credentials are passed only to the one-shot
# Compose service and are never mounted into web or admin.

project_root="$(cd "$(dirname "$0")/.." && pwd)"
docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
env_file="${STONEAGE_ENV_FILE:-$project_root/.env}"
compose_file="$project_root/docker-compose.yml"
dry_run=0
workers=""

usage()
{
    cat <<'EOF'
Usage: scripts/sync-client-assets.sh [options]

Publish the complete public 2.5 browser client (sprites, maps, BGM and SE)
to the OSS origin configured in config/web.toml. The destination is the
stable <prefix>/{assets,maps,audio}/ root; no release tag is added to the URL.
The uploader keeps a SHA-256 client manifest at <prefix>/_client-manifest.json
and only uploads changed objects on later runs. The sync job deliberately
excludes private files from data/ such as saves, chat history and PE support
binaries.

Options:
  --dry-run       Validate the source trees and print the object count only.
  --workers N     Number of parallel uploads (1..64; default from .env/8).
  --env FILE      Read FILE instead of .env.
  -h, --help      Show this help.

Set STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE and
STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE to 0600 files containing the two OSS
credentials. For CI/direct use, ALIBABA_CLOUD_ACCESS_KEY_ID and
ALIBABA_CLOUD_ACCESS_KEY_SECRET are accepted and copied to those files for
the one-shot container. The admin UI is not involved.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) dry_run=1 ;;
        --workers)
            if [[ $# -lt 2 ]]; then
                echo "--workers requires a number" >&2
                exit 2
            fi
            workers="$2"
            shift
            ;;
        --env)
            if [[ $# -lt 2 ]]; then
                echo "--env requires a file" >&2
                exit 2
            fi
            env_file="$2"
            shift
            ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

if [[ "$env_file" != /* ]]; then
    env_file="$project_root/$env_file"
fi
if [[ ! -f "$env_file" ]]; then
    echo "Missing $env_file. Run scripts/deploy-mvp.sh --init first." >&2
    exit 1
fi
if [[ ! -f "$compose_file" ]]; then
    echo "Compose file not found: $compose_file" >&2
    exit 1
fi
if ! command -v "$docker_bin" >/dev/null 2>&1; then
    echo "Docker CLI not found: $docker_bin" >&2
    exit 1
fi
if ! "$docker_bin" info >/dev/null 2>&1; then
    echo "Docker daemon is not reachable; start Docker Engine and retry." >&2
    exit 1
fi
if ! "$docker_bin" compose version >/dev/null 2>&1; then
    echo "Docker Compose v2 plugin is required (try: docker compose version)." >&2
    exit 1
fi

# Compose performs .env interpolation.  Keep the explicit --env-file so the
# selected credentials/configuration are used even when the caller's cwd is
# not the project root.
compose_args=(--project-directory "$project_root" --env-file "$env_file" -f "$compose_file" --profile assets-sync)
compose()
{
    "$docker_bin" compose "${compose_args[@]}" "$@"
}

env_value()
{
    local key="$1"
    awk -v key="$key" '
        /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
        {
            line=$0
            sub(/^[[:space:]]*/, "", line)
            split(line, fields, "=")
            if (fields[1] != key) next
            value=substr(line, length(fields[1])+2)
            sub(/^[[:space:]]*/, "", value)
            sub(/[[:space:]]+#.*$/, "", value)
            if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) {
                value=substr(value, 2, length(value)-2)
            }
            print value
            exit
        }
    ' "$env_file"
}

compose config --quiet

run_args=(run --rm --no-deps assets-sync)
if [[ "$dry_run" == 1 ]]; then
    run_args+=(--dry-run)
fi
if [[ -n "$workers" ]]; then
    if [[ ! "$workers" =~ ^[0-9]+$ ]] || (( workers < 1 || workers > 64 )); then
        echo "--workers must be between 1 and 64" >&2
        exit 2
    fi
    run_args+=(-workers "$workers")
fi

if [[ "$dry_run" != 1 ]]; then
    # Do not silently start an upload with empty credentials. Prefer secret
    # files so the values never enter Compose's rendered environment.
    access_key="${ALIBABA_CLOUD_ACCESS_KEY_ID:-}"
    access_secret="${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}"
    access_key="${access_key:-$(env_value ALIBABA_CLOUD_ACCESS_KEY_ID || true)}"
    access_secret="${access_secret:-$(env_value ALIBABA_CLOUD_ACCESS_KEY_SECRET || true)}"
    access_key_file="${STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE:-$(env_value STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE || true)}"
    access_secret_file="${STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE:-$(env_value STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE || true)}"
    access_key_file="${access_key_file:-.secrets/oss-access-key-id}"
    access_secret_file="${access_secret_file:-.secrets/oss-access-key-secret}"
    case "$access_key_file" in /*) ;; *) access_key_file="$project_root/${access_key_file#./}" ;; esac
    case "$access_secret_file" in /*) ;; *) access_secret_file="$project_root/${access_secret_file#./}" ;; esac
    if [[ -n "$access_key" && ! -s "$access_key_file" ]]; then
        umask 077
        mkdir -p "$(dirname "$access_key_file")"
        printf '%s\n' "$access_key" >"$access_key_file"
    fi
    if [[ -n "$access_secret" && ! -s "$access_secret_file" ]]; then
        umask 077
        mkdir -p "$(dirname "$access_secret_file")"
        printf '%s\n' "$access_secret" >"$access_secret_file"
    fi
    chmod 600 "$access_key_file" "$access_secret_file"
    if [[ ! -s "$access_key_file" || ! -s "$access_secret_file" ]]; then
        echo "OSS credentials are missing; provide the two secret files or ALIBABA_CLOUD_ACCESS_KEY_ID(_FILE) and ALIBABA_CLOUD_ACCESS_KEY_SECRET(_FILE)." >&2
        exit 2
    fi
    # Compose resolves secret file paths against the project directory. Pass
    # the normalized paths explicitly so --env FILE and an external cwd agree.
    export STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE="$access_key_file"
    export STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE="$access_secret_file"
else
    # Compose still needs a source file for a file-backed secret even though
    # the uploader will not read it in --dry-run mode. Create only the ignored
    # local defaults; custom external paths are an operator error.
    access_key_file="${STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE:-$(env_value STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE || true)}"
    access_secret_file="${STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE:-$(env_value STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE || true)}"
    access_key_file="${access_key_file:-.secrets/oss-access-key-id}"
    access_secret_file="${access_secret_file:-.secrets/oss-access-key-secret}"
    case "$access_key_file" in /*) ;; *) access_key_file="$project_root/${access_key_file#./}" ;; esac
    case "$access_secret_file" in /*) ;; *) access_secret_file="$project_root/${access_secret_file#./}" ;; esac
    for secret_file in "$access_key_file" "$access_secret_file"; do
        if [[ ! -e "$secret_file" ]]; then
            if [[ "$secret_file" != "$project_root/.secrets/"* ]]; then
                echo "secret file is missing: $secret_file" >&2
                exit 2
            fi
            umask 077
            mkdir -p "$(dirname "$secret_file")"
            : >"$secret_file"
        fi
        chmod 600 "$secret_file"
    done
    export STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE="$access_key_file"
    export STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE="$access_secret_file"
fi

echo "Publishing the complete client asset tree (assets, maps, audio)…"
compose "${run_args[@]}"
echo "Client assets published."
