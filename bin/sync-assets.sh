#!/usr/bin/env bash
set -Eeuo pipefail

# Publish browser resources with the platform-native uploader. This path never
# contacts Docker or GHCR.
project_root="$(cd "$(dirname "$0")/.." && pwd)"
env_file="$(printenv STONEAGE_ENV_FILE 2>/dev/null || true)"
if [[ -z "$env_file" ]]; then
    env_file="$project_root/.env"
fi
web_args=()
dry_run=0
workers=""
asset_sync_bin=""

usage()
{
    cat <<'EOF'
Usage: bin/sync-assets.sh [options]

Publish sprites, maps, BGM and SE to the OSS/R2 origin configured in
config/web/web.toml. The host-native bin/stoneage-assets-sync uploader is
required; set STONEAGE_ASSET_SYNC_BIN to override its path.

Options:
  --sactl-packages DIR Publish released client archives and installers.
  --sactl-version TAG Stable release tag for the client archives.
  --web-only      Publish CDN modules and compressed indexes only.
  --resource-packs DIR Publish complete game resource ZIP and download catalog.
  --map-packs DIR Publish matching map packages (with --web-only).
  --dry-run       Validate the source trees and print the object count only.
  --workers N     Number of parallel uploads (1..64; default from .env/8).
  --env FILE      Read FILE instead of .env.
  -h, --help      Show this help.

Credentials use the existing 0600 secret files or provider-compatible
AWS_*, CLOUDFLARE_R2_* and ALIBABA_CLOUD_* variables. Docker and GHCR are not
used by this command.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --sactl-packages|--sactl-version)
            [[ $# -ge 2 ]] || { echo "$1 requires a value" >&2; exit 2; }
            web_args+=("-${1#--}" "$2"); shift ;;
        --web-only) web_args+=(-web-only) ;;
        --resource-packs)
            [[ $# -ge 2 ]] || { echo "--resource-packs requires a directory" >&2; exit 2; }
            web_args+=(-resource-packs "$2"); shift ;;
        --map-packs)
            [[ $# -ge 2 ]] || { echo "--map-packs requires a directory" >&2; exit 2; }
            web_args+=(-map-packs "$2"); shift ;;
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
    case "$env_file" in
        ./*) env_file="$project_root/$(printf '%s' "$env_file" | sed 's#^\./##')" ;;
        *) env_file="$project_root/$env_file" ;;
    esac
fi
if [[ ! -f "$env_file" ]]; then
    echo "Missing $env_file. Run bin/deploy.sh --init first." >&2
    exit 1
fi

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
            sub(/\r$/, "", value)
            if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) {
                value=substr(value, 2, length(value)-2)
            }
            print value
            exit
        }
    ' "$env_file"
}

first_nonempty()
{
    local value
    for value in "$@"; do
        if [[ -n "$value" ]]; then
            printf '%s\n' "$value"
            return 0
        fi
    done
    return 0
}

env_or_file_value()
{
    local env_name="$1"
    local file_key="$2"
    local value
    value="$(printenv "$env_name" 2>/dev/null || true)"
    if [[ -z "$value" ]]; then
        value="$(env_value "$file_key" || true)"
    fi
    printf '%s\n' "$value"
}

resolve_path()
{
    local value="$1"
    case "$value" in
        /*) printf '%s\n' "$value" ;;
        ./*) printf '%s/%s\n' "$project_root" "$(printf '%s' "$value" | sed 's#^\./##')" ;;
        *) printf '%s/%s\n' "$project_root" "$value" ;;
    esac
}

asset_sync_bin="$(first_nonempty \
    "$(env_or_file_value STONEAGE_ASSET_SYNC_BIN STONEAGE_ASSET_SYNC_BIN)" \
    "$project_root/bin/stoneage-assets-sync")"
asset_sync_bin="$(resolve_path "$asset_sync_bin")"

web_config_file="$(first_nonempty \
    "$(env_or_file_value STONEAGE_WEB_CONFIG_FILE STONEAGE_WEB_CONFIG_FILE)" \
    "./config/web/web.toml")"
web_config_file="$(resolve_path "$web_config_file")"
if [[ ! -f "$web_config_file" ]]; then
    echo "Web configuration file not found: $web_config_file" >&2
    exit 2
fi


sprites_root="$(first_nonempty \
    "$(env_or_file_value STONEAGE_SPRITES_ROOT STONEAGE_SPRITES_ROOT)" \
    "./assets/sprites")"
sprites_root="$(resolve_path "$sprites_root")"
for sprite_manifest in manifest.json sprites.json; do
    if [[ ! -f "$sprites_root/$sprite_manifest" || ! -s "$sprites_root/$sprite_manifest" ]]; then
        echo "Sprite resource file is missing or empty: $sprites_root/$sprite_manifest" >&2
        echo "Extract the standalone sprite archive into $sprites_root before deploying or syncing." >&2
        exit 2
    fi
done

client_root="$(first_nonempty \
    "$(env_or_file_value STONEAGE_CLIENT_DATA_ROOT STONEAGE_CLIENT_DATA_ROOT)" \
    "./assets/client")"
client_root="$(resolve_path "$client_root")"
if [[ ! -d "$client_root" ]]; then
    echo "Client data directory not found: $client_root" >&2
    exit 2
fi

if [[ -z "$workers" ]]; then
    workers="$(first_nonempty \
        "$(env_or_file_value STONEAGE_ASSET_SYNC_WORKERS STONEAGE_ASSET_SYNC_WORKERS)" \
        "8")"
fi
if [[ ! "$workers" =~ ^[0-9]+$ ]] || (( workers < 1 || workers > 64 )); then
    echo "--workers must be between 1 and 64" >&2
    exit 2
fi

if [[ ! -x "$asset_sync_bin" ]]; then
    echo "Native asset uploader not found or not executable: $asset_sync_bin" >&2
    echo "Install the platform-matched stoneage-assets-sync binary (Linux, macOS, or WSL), or set STONEAGE_ASSET_SYNC_BIN. Docker/GHCR is not used." >&2
    exit 1
fi

# Keep the existing provider-compatible credential precedence. Secret values
# are written to files and only the file paths are exported to the uploader;
# no secret value is ever included in its argument vector.
if [[ "$dry_run" != 1 ]]; then
    access_key="$(first_nonempty \
        "$(printenv STONEAGE_ASSET_SYNC_ACCESS_KEY 2>/dev/null || true)" \
        "$(printenv CLOUDFLARE_R2_ACCESS_KEY_ID 2>/dev/null || true)" \
        "$(printenv AWS_ACCESS_KEY_ID 2>/dev/null || true)" \
        "$(printenv ALIBABA_CLOUD_ACCESS_KEY_ID 2>/dev/null || true)" \
        "$(env_value STONEAGE_ASSET_SYNC_ACCESS_KEY || true)" \
        "$(env_value CLOUDFLARE_R2_ACCESS_KEY_ID || true)" \
        "$(env_value AWS_ACCESS_KEY_ID || true)" \
        "$(env_value ALIBABA_CLOUD_ACCESS_KEY_ID || true)")"
    access_secret="$(first_nonempty \
        "$(printenv STONEAGE_ASSET_SYNC_ACCESS_SECRET 2>/dev/null || true)" \
        "$(printenv CLOUDFLARE_R2_SECRET_ACCESS_KEY 2>/dev/null || true)" \
        "$(printenv AWS_SECRET_ACCESS_KEY 2>/dev/null || true)" \
        "$(printenv ALIBABA_CLOUD_ACCESS_KEY_SECRET 2>/dev/null || true)" \
        "$(env_value STONEAGE_ASSET_SYNC_ACCESS_SECRET || true)" \
        "$(env_value CLOUDFLARE_R2_SECRET_ACCESS_KEY || true)" \
        "$(env_value AWS_SECRET_ACCESS_KEY || true)" \
        "$(env_value ALIBABA_CLOUD_ACCESS_KEY_SECRET || true)")"
fi

access_key_file="$(first_nonempty \
    "$(env_or_file_value STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE)" \
    "$(env_or_file_value CLOUDFLARE_R2_ACCESS_KEY_ID_FILE CLOUDFLARE_R2_ACCESS_KEY_ID_FILE)" \
    "$(env_or_file_value AWS_ACCESS_KEY_ID_FILE AWS_ACCESS_KEY_ID_FILE)" \
    "$(env_or_file_value ALIBABA_CLOUD_ACCESS_KEY_ID_FILE ALIBABA_CLOUD_ACCESS_KEY_ID_FILE)" \
    "./config/secrets/oss-access-key-id")"
access_secret_file="$(first_nonempty \
    "$(env_or_file_value STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE)" \
    "$(env_or_file_value CLOUDFLARE_R2_SECRET_ACCESS_KEY_FILE CLOUDFLARE_R2_SECRET_ACCESS_KEY_FILE)" \
    "$(env_or_file_value AWS_SECRET_ACCESS_KEY_FILE AWS_SECRET_ACCESS_KEY_FILE)" \
    "$(env_or_file_value ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE)" \
    "./config/secrets/oss-access-key-secret")"
access_key_file="$(resolve_path "$access_key_file")"
access_secret_file="$(resolve_path "$access_secret_file")"

if [[ "$dry_run" != 1 ]]; then
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
    if [[ ( -e "$access_key_file" && ! -f "$access_key_file" ) || ( -e "$access_secret_file" && ! -f "$access_secret_file" ) ]]; then
        echo "object-storage credential paths must be regular files." >&2
        exit 2
    fi
    if [[ ! -s "$access_key_file" || ! -s "$access_secret_file" ]]; then
        echo "object-storage credentials are missing; provide the two secret files or provider-compatible access-key variables." >&2
        exit 2
    fi
    chmod 600 "$access_key_file" "$access_secret_file"
else
    # The native uploader skips credential reads for --dry-run, but retain the
    # old default-file behavior so callers can use the same env configuration
    # for a subsequent real upload. Custom missing paths remain an error.
    for secret_file in "$access_key_file" "$access_secret_file"; do
        if [[ ! -e "$secret_file" ]]; then
            case "$secret_file" in
                "$project_root/config/secrets/"*) ;;
                *)
                    echo "secret file is missing: $secret_file" >&2
                    exit 2
                    ;;
            esac
            umask 077
            mkdir -p "$(dirname "$secret_file")"
            : >"$secret_file"
        elif [[ ! -f "$secret_file" ]]; then
            echo "secret file is not a regular file: $secret_file" >&2
            exit 2
        fi
        chmod 600 "$secret_file"
    done
fi


export STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE="$access_key_file"
export STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE="$access_secret_file"
# Values are no longer needed after materializing the files. Prevent inherited
# provider aliases from becoming a second credential channel for the child.
unset STONEAGE_ASSET_SYNC_ACCESS_KEY STONEAGE_ASSET_SYNC_ACCESS_SECRET \
    CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY \
    AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY \
    ALIBABA_CLOUD_ACCESS_KEY_ID ALIBABA_CLOUD_ACCESS_KEY_SECRET

run_args=(
    -config "$web_config_file"
    -assets "$sprites_root"
    -client-data "$client_root"
    -workers "$workers"
)
if [[ "$dry_run" == 1 ]]; then
    run_args+=(-dry-run)
fi

echo "Publishing the complete client asset tree (assets, maps, audio)…"
"$asset_sync_bin" "${run_args[@]}" ${web_args[@]+"${web_args[@]}"}
if [[ "$dry_run" == 1 ]]; then
    echo "Client asset dry-run completed; no objects uploaded."
else
    echo "Client assets published."
fi
