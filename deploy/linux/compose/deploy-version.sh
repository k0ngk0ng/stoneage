#!/usr/bin/env bash
set -Eeuo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid release version" >&2
  exit 2
fi

docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
project_root="${STONEAGE_PROJECT_CONTAINER_ROOT:-/host-project}"
project_host_root="${STONEAGE_PROJECT_HOST_ROOT:-${STONEAGE_PROJECT_ROOT:-}}"
compose_file="${STONEAGE_COMPOSE_FILE:-$project_root/docker-compose.yml}"
control_image="${STONEAGE_CONTROL_IMAGE:?STONEAGE_CONTROL_IMAGE is required}"
legacy_image="${STONEAGE_LEGACY_IMAGE:?STONEAGE_LEGACY_IMAGE is required}"
state_file="${STONEAGE_DEPLOY_STATE:-/state/deploy.status}"
state_volume="${STONEAGE_OPERATOR_STATE_VOLUME:-stoneage-operator-state}"
auth_volume="${STONEAGE_AUTH_VOLUME:-stoneage-auth}"
gmsv_data_root="${STONEAGE_GMSV_DATA_ROOT:?STONEAGE_GMSV_DATA_ROOT is required}"
saac_data_root="${STONEAGE_SAAC_DATA_ROOT:?STONEAGE_SAAC_DATA_ROOT is required}"

if [[ ! -f "$compose_file" ]]; then
  echo "compose file is unavailable: $compose_file" >&2
  exit 1
fi
if [[ -z "$project_host_root" || ! "$project_host_root" = /* ]]; then
  echo "project host root must be an absolute host path" >&2
  exit 1
fi
if [[ ! "$gmsv_data_root" = /* || ! "$saac_data_root" = /* ]]; then
  echo "data roots must be absolute host paths" >&2
  exit 1
fi

mkdir -p "$(dirname "$state_file")"
timestamp() { date -u '+%Y-%m-%dT%H:%M:%SZ'; }
write_state()
{
  phase="$1"
  message="$2"
  backup="${3:-}"
  temporary="$state_file.$$"
  umask 077
  {
    printf 'version=%s\n' "$version"
    printf 'phase=%s\n' "$phase"
    printf 'message=%s\n' "$message"
    printf 'updated_at=%s\n' "$(timestamp)"
    printf 'backup=%s\n' "$backup"
  } >"$temporary"
  mv -f "$temporary" "$state_file"
}

compose()
{
  compose_version="$1"
  compose_control_image="$2"
  compose_legacy_image="$3"
  shift 3
  env \
    STONEAGE_VERSION="$compose_version" \
    STONEAGE_CONTROL_IMAGE="$compose_control_image" \
    STONEAGE_LEGACY_IMAGE="$compose_legacy_image" \
    STONEAGE_PROJECT_ROOT="$project_host_root" \
    STONEAGE_PROJECT_HOST_ROOT="$project_host_root" \
    "$docker_bin" compose --project-directory "$project_root" -f "$compose_file" "$@"
}

previous_image()
{
  container="$1"
  "$docker_bin" inspect -f '{{.Config.Image}}' "$container" 2>/dev/null || true
}

split_image()
{
  image="$1"
  last_component="${image##*/}"
  if [[ "$last_component" != *:* ]]; then
    printf '%s\n%s\n' "$image" latest
    return
  fi
  printf '%s\n%s\n' "${image%:*}" "${image##*:}"
}

control_current="$(previous_image "${STONEAGE_CONTROL_CONTAINER:-stoneage-service-control}")"
legacy_current="$(previous_image "${STONEAGE_GMSV_CONTAINER:-stoneage-gmsv}")"
previous_control_repo="$(split_image "$control_current" | sed -n '1p')"
previous_version="$(split_image "$control_current" | sed -n '2p')"
previous_legacy_repo="$(split_image "$legacy_current" | sed -n '1p')"

backup_dir="/state/backups/${version}-$(date -u '+%Y%m%dT%H%M%SZ')"
mkdir -p "$backup_dir"

on_failure()
{
  code=$?
  trap - ERR
  write_state rolling_back "发布失败，正在回滚" "$backup_dir"
  if [[ "$previous_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] &&
     [[ -n "$previous_control_repo" && -n "$previous_legacy_repo" ]]; then
    if compose "$previous_version" "$previous_control_repo" "$previous_legacy_repo" up -d --no-build; then
      write_state rolled_back "新版本健康检查失败，已回滚到 $previous_version" "$backup_dir"
    else
      write_state failed "回滚失败，请立即检查 Docker 服务" "$backup_dir"
    fi
  else
    write_state failed "发布失败且找不到可回滚版本" "$backup_dir"
  fi
  exit "$code"
}
trap on_failure ERR

write_state preparing "准备发布 $version" "$backup_dir"

write_state stopping "停止旧服务以生成一致备份" "$backup_dir"
compose "${previous_version:-local}" "${previous_control_repo:-$control_image}" "${previous_legacy_repo:-$legacy_image}" stop gateway gmsv saac admin || true

write_state backup "备份账号库和 GMSV/SAAC 数据" "$backup_dir"

# Copy the SQLite database through the named volume so the host never needs to
# know Docker's internal volume path. Character/mail/family stores are tarred
# from their explicit host bind paths, read-only from this detached job.
backup_name="$(basename "$backup_dir")"
"$docker_bin" run --rm \
  --volume "$auth_volume:/data:ro" \
  --volume "$state_volume:/backup" \
  alpine:3.22 sh -c "mkdir -p /backup/backups/$backup_name/auth && cp -a /data/. /backup/backups/$backup_name/auth/"
tar -czf "$backup_dir/gmsv-data.tgz" -C "$gmsv_data_root" .
tar -czf "$backup_dir/saac-data.tgz" -C "$saac_data_root" .

write_state pulling "拉取 $version 镜像" "$backup_dir"
"$docker_bin" pull "$control_image:$version"
"$docker_bin" pull "$legacy_image:$version"

write_state updating "切换到 $version" "$backup_dir"
compose "$version" "$control_image" "$legacy_image" up -d --no-build

write_state verifying "等待服务健康" "$backup_dir"
for service in saac gmsv gateway service-control admin; do
  ready=0
  for _ in $(seq 1 180); do
    container="$($docker_bin compose --project-directory "$project_root" -f "$compose_file" ps -q "$service" 2>/dev/null || true)"
    if [[ -n "$container" ]]; then
      status="$($docker_bin inspect -f '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$container" 2>/dev/null || true)"
      if [[ "$status" == "running healthy" ]]; then
        ready=1
        break
      fi
    fi
    sleep 1
  done
  if [[ "$ready" != 1 ]]; then
    echo "$service did not become healthy" >&2
    exit 1
  fi
done

write_state succeeded "已发布 $version" "$backup_dir"
trap - ERR
