#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
runtime_root="${STONEAGE_RUNTIME_ROOT:-$project_root/runtime/legacy-server}"
line2_runtime_root="${STONEAGE_RUNTIME_ROOT_LINE2:-$project_root/runtime/legacy-server-line2}"
compose_project="${STONEAGE_COMPOSE_PROJECT:-stoneage-multiline-local}"
gateway_bind="${STONEAGE_GATEWAY_BIND:-127.0.0.1}"
gateway_port="${STONEAGE_GATEWAY_PORT:-9066}"
gateway_port_2="${STONEAGE_GATEWAY_PORT_2:-9067}"
admin_port="${STONEAGE_ADMIN_PORT:-8081}"
admin_user="${STONEAGE_ADMIN_USER:-admin}"
admin_password="${STONEAGE_ADMIN_PASSWORD:-stoneage-local-admin}"

if [[ ! -x "$runtime_root/saac/saacjt.exe" || ! -x "$runtime_root/gmsv/gmsvjt.exe" ]]; then
  "$project_root/scripts/prepare-legacy-server.sh" \
    "$project_root/vendor/upstream/2.5" "$runtime_root"
fi

if [[ ! -x "$line2_runtime_root/gmsv/gmsvjt.exe" ]]; then
  STONEAGE_GMSV_NAME=stoneage-line2 \
  STONEAGE_GMSV_ID=stoneage-line2 \
  STONEAGE_GMSV_SERVER_NUMBER=2 \
    "$project_root/scripts/prepare-legacy-server.sh" \
      "$project_root/vendor/upstream/2.5" "$line2_runtime_root"
fi

compose_env=(
  "STONEAGE_RUNTIME_ROOT=$runtime_root"
  "STONEAGE_RUNTIME_ROOT_LINE2=$line2_runtime_root"
  "STONEAGE_SAAC_CONTAINER=stoneage-multiline-saac"
  "STONEAGE_GMSV_CONTAINER=stoneage-multiline-gmsv1"
  "STONEAGE_GMSV2_CONTAINER=stoneage-multiline-gmsv2"
  "STONEAGE_GATEWAY_CONTAINER=stoneage-multiline-gateway"
  "STONEAGE_OPERATOR_CONTAINER=stoneage-multiline-operator"
  "STONEAGE_ADMIN_CONTAINER=stoneage-multiline-admin"
  "STONEAGE_AUTH_VOLUME=stoneage-multiline-auth"
  "STONEAGE_OPERATOR_RUN_VOLUME=stoneage-multiline-operator-run"
  "STONEAGE_GATEWAY_BIND=$gateway_bind"
  "STONEAGE_GATEWAY_PORT=$gateway_port"
  "STONEAGE_GATEWAY_PORT_2=$gateway_port_2"
  "STONEAGE_GATEWAY_ROUTES=0.0.0.0:9065=gmsv:9065;0.0.0.0:9066=gmsv2:9065"
  "STONEAGE_ADMIN_BIND=127.0.0.1"
  "STONEAGE_ADMIN_PORT=$admin_port"
  "STONEAGE_ADMIN_USER=$admin_user"
  "STONEAGE_ADMIN_PASSWORD=$admin_password"
)

cd "$project_root"
env "${compose_env[@]}" docker compose \
  -p "$compose_project" \
  -f docker-compose.yml \
  -f docker-compose.multiline.yml \
  up -d --build

cat <<EOF

多线路本地服务已启动：
  网关线路 1：${gateway_bind}:${gateway_port}
  网关线路 2：${gateway_bind}:${gateway_port_2}
  管理后台：http://127.0.0.1:${admin_port}/ （管理员：${admin_user}）
  管理员初始密码：${admin_password}

查看状态：
  env ${compose_env[*]} docker compose -p ${compose_project} -f docker-compose.yml -f docker-compose.multiline.yml ps
查看日志：
  env ${compose_env[*]} docker compose -p ${compose_project} -f docker-compose.yml -f docker-compose.multiline.yml logs -f saac gmsv gmsv2 gateway
EOF
