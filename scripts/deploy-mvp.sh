#!/usr/bin/env bash
set -Eeuo pipefail

# Bring up the single-host MVP described by docker-compose.yml.  This script
# deliberately does not run `down -v`: the auth volume contains administrator
# sessions and must survive an application image update.

project_root="$(cd "$(dirname "$0")/.." && pwd)"
docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
env_file="${STONEAGE_ENV_FILE:-$project_root/.env}"
compose_file="$project_root/docker-compose.yml"
mode="auto"
init_env=0
check_only=0
created_env=0

usage()
{
    cat <<'EOF'
Usage: scripts/deploy-mvp.sh [options]

Prepare and start the single-host StoneAge MVP with Docker Compose.

Options:
  --init             Create .env with a random administrator password.
  --build            Build the configured control-plane and legacy images.
  --pull             Pull the configured versioned images from a registry.
  --no-image-update  Do not build or pull images (use images already present).
  --check            Validate .env, bind paths and Compose without starting services.
  --env FILE         Read FILE instead of .env.
  -h, --help         Show this help.

With the default --init/auto flow, STONEAGE_VERSION=local builds images and a
version beginning with v pulls images.  The generated .env is mode 0600.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --init) init_env=1 ;;
        --build) mode=build ;;
        --pull) mode=pull ;;
        --no-image-update) mode=none ;;
        --check) check_only=1 ;;
        --env)
            if [[ $# -lt 2 ]]; then
                echo "--env requires a file" >&2
                exit 2
            fi
            env_file="$2"
            shift
            ;;
        -h|--help) usage ; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

if [[ ! -f "$compose_file" ]]; then
    echo "Compose file not found: $compose_file" >&2
    exit 1
fi

if [[ ! -f "$env_file" ]]; then
    if [[ "$init_env" != 1 ]]; then
        echo "Missing $env_file. Run '$0 --init' once, then review the values." >&2
        exit 1
    fi
    if [[ "$env_file" != /* ]]; then
        env_file="$project_root/$env_file"
    fi
    umask 077
    mkdir -p "$(dirname "$env_file")"
    cp "$project_root/.env.compose.example" "$env_file"
    random_password=""
    if command -v openssl >/dev/null 2>&1; then
        random_password="$(openssl rand -hex 24)"
    elif [[ -r /dev/urandom ]]; then
        random_password="$(od -An -N24 -tx1 /dev/urandom | tr -d '[:space:]')"
    fi
    if [[ -z "$random_password" ]]; then
        rm -f "$env_file"
        echo "Cannot generate a secure administrator password; install openssl and retry." >&2
        exit 1
    fi
    # The example contains a single STONEAGE_ADMIN_PASSWORD assignment.  Use
    # awk instead of sed so the generated value is not interpreted as syntax.
    temporary="${env_file}.tmp.$$"
    awk -v password="$random_password" '
        /^STONEAGE_ADMIN_PASSWORD=/ { print "STONEAGE_ADMIN_PASSWORD=" password; next }
        { print }
    ' "$env_file" >"$temporary"
    mv -f "$temporary" "$env_file"
    chmod 600 "$env_file"
    created_env=1
    echo "Created $env_file with a random administrator password. Keep it private." >&2
elif [[ "$init_env" == 1 ]]; then
    echo "$env_file already exists; refusing to overwrite it." >&2
    exit 1
fi

if [[ "$created_env" == 1 ]]; then
    echo "Review the generated values (especially STONEAGE_CLIENT_DATA_ROOT), then rerun without --init." >&2
    exit 0
fi

if [[ "$env_file" != /* ]]; then
    env_file="$(cd "$(dirname "$env_file")" && pwd)/$(basename "$env_file")"
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

compose_args=(--project-directory "$project_root" --env-file "$env_file" -f "$compose_file")
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

version="$(env_value STONEAGE_VERSION || true)"
control_image="$(env_value STONEAGE_CONTROL_IMAGE || true)"
legacy_image="$(env_value STONEAGE_LEGACY_IMAGE || true)"
version="${version:-local}"
control_image="${control_image:-stoneage-control-plane}"
legacy_image="${legacy_image:-stoneage-legacy-runtime}"
admin_password="$(env_value STONEAGE_ADMIN_PASSWORD || true)"
setup_token="$(env_value STONEAGE_ADMIN_SETUP_TOKEN || true)"
cdn_base="$(env_value STONEAGE_WEB_CDN_BASE_URL || true)"
web_config_file="$(env_value STONEAGE_WEB_CONFIG_FILE || true)"
web_config_file="${web_config_file:-./config/web.toml}"
if [[ "$check_only" != 1 && "$admin_password" == "replace-with-a-long-random-password" && -z "$setup_token" ]]; then
    echo "Set STONEAGE_ADMIN_PASSWORD (or a one-time STONEAGE_ADMIN_SETUP_TOKEN) in $env_file before deploying." >&2
    exit 2
fi
if [[ -n "$cdn_base" ]]; then
    if [[ "$cdn_base" != https://* && "$cdn_base" != http://* ]]; then
        echo "STONEAGE_WEB_CDN_BASE_URL must be an absolute HTTP(S) URL." >&2
        exit 2
    fi
    cdn_authority="${cdn_base#*://}"
    cdn_authority="${cdn_authority%%/*}"
    if [[ -z "$cdn_authority" || "$cdn_authority" == *@* || "$cdn_base" == *\?* || "$cdn_base" == *\#* ]]; then
        echo "STONEAGE_WEB_CDN_BASE_URL must not contain credentials, a query or a fragment." >&2
        exit 2
    fi
    if [[ "$cdn_base" == http://* ]]; then
        echo "WARNING: an HTTP CDN is blocked when the browser client is served over HTTPS; use an HTTPS CDN in production." >&2
    fi
fi
case "$mode" in
    auto)
        case "$version" in
            ""|local) mode=build ;;
            v[0-9]*) mode=pull ;;
            *) mode=none ;;
        esac
        ;;
esac

# Bind mounts are created by Docker as root when absent.  Create them here so
# the operator can back them up and so a typo in a relative path is visible in
# the host checkout before containers start.
for data_key in STONEAGE_GMSV_DATA_ROOT STONEAGE_SAAC_DATA_ROOT STONEAGE_CLIENT_DATA_ROOT; do
    data_root="$(env_value "$data_key" || true)"
    [[ -n "$data_root" ]] || continue
    case "$data_root" in
        /*) ;;
        *) data_root="$project_root/$data_root" ;;
    esac
    mkdir -p "$data_root"
done

case "$web_config_file" in
    /*) ;;
    *) web_config_file="$project_root/$web_config_file" ;;
esac
if [[ ! -f "$web_config_file" ]]; then
    echo "Web configuration file not found: $web_config_file" >&2
    echo "Copy or edit $project_root/config/web.toml before deploying." >&2
    exit 2
fi

client_data_root="$(env_value STONEAGE_CLIENT_DATA_ROOT || true)"
if [[ -n "$client_data_root" ]]; then
    case "$client_data_root" in
        /*) ;;
        *) client_data_root="$project_root/$client_data_root" ;;
    esac
    if [[ ! -d "$client_data_root/map" || ! -d "$client_data_root/data/bgm" || ! -d "$client_data_root/data/se" ]]; then
        echo "WARNING: $client_data_root lacks client map/data/bgm/data/se; web map/audio will be unavailable." >&2
    fi
fi

echo "Validating Compose configuration ($env_file)..."
compose config --quiet
if [[ "$check_only" == 1 ]]; then
    echo "Compose configuration is valid."
    echo "Web config: $web_config_file"
    if [[ -n "$cdn_base" ]]; then
        echo "Static CDN: ${cdn_base%/}/"
    fi
    exit 0
fi

case "$mode" in
    build)
        echo "Building control-plane image $control_image:$version..."
        "$docker_bin" build --pull \
            --file "$project_root/deploy/linux/Dockerfile" \
            --tag "$control_image:$version" \
            "$project_root"
        echo "Building legacy runtime image $legacy_image:$version..."
        "$docker_bin" build --pull \
            --file "$project_root/deploy/linux/legacy-runtime.Dockerfile" \
            --tag "$legacy_image:$version" \
            "$project_root"
        ;;
    pull)
        echo "Pulling configured release images..."
        compose pull
        ;;
    none)
        echo "Using images already present on this host."
        ;;
esac

echo "Starting StoneAge MVP services..."
compose up -d --remove-orphans

services=(saac gmsv gateway web service-control admin)
wait_seconds="${STONEAGE_DEPLOY_HEALTH_TIMEOUT:-180}"
if ! [[ "$wait_seconds" =~ ^[1-9][0-9]*$ ]]; then
    echo "STONEAGE_DEPLOY_HEALTH_TIMEOUT must be a positive integer" >&2
    exit 2
fi

wait_healthy()
{
    local service="$1"
    local deadline=$((SECONDS + wait_seconds))
    local container status
    while (( SECONDS < deadline )); do
        container="$(compose ps -q "$service" 2>/dev/null || true)"
        if [[ -n "$container" ]]; then
            status="$("$docker_bin" inspect -f '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$container" 2>/dev/null || true)"
            if [[ "$status" == "running healthy" ]]; then
                return 0
            fi
            if [[ "$status" == exited\ * || "$status" == dead\ * ]]; then
                break
            fi
        fi
        sleep 1
    done
    echo "$service did not become healthy within ${wait_seconds}s" >&2
    compose ps >&2 || true
    compose logs --tail=80 "$service" >&2 || true
    return 1
}

for service in "${services[@]}"; do
    wait_healthy "$service"
done

compose ps
web_bind="$(env_value STONEAGE_WEB_BIND || true)"
web_port="$(env_value STONEAGE_WEB_PORT || true)"
admin_bind="$(env_value STONEAGE_ADMIN_BIND || true)"
admin_port="$(env_value STONEAGE_ADMIN_PORT || true)"
echo
echo "MVP is healthy."
echo "Browser client: http://${web_bind:-127.0.0.1}:${web_port:-8088}/"
echo "Admin console:  http://${admin_bind:-127.0.0.1}:${admin_port:-8080}/"
if [[ -n "$cdn_base" ]]; then
    echo "Static CDN:    ${cdn_base%/}/"
fi
echo "Use the admin console to create a game account before logging in."
echo "Do not run 'docker compose down -v'; it removes the persistent auth volume."
