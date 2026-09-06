# Docker Compose 部署（完整服务端 + Web）

要求：Linux x86-64、Docker Engine、Docker Compose v2 或更新版本、Git。
部署包含 GMSV、SAAC、认证网关、SQLite、管理后台和 Web 客户端。
以下命令在项目根目录执行。

## 1. 初始化配置

```bash
git clone https://github.com/k0ngk0ng/stoneage.git
cd stoneage
./scripts/deploy.sh --init
```

初始化生成 `.env` 和缺失的 GMSV/SAAC 配置，不启动服务，不覆盖已有配置。
组件配置统一放在 `config/`：

```text
.env                         # Compose 部署参数、首次管理员密码
config/
├── gateway.toml             # 网关监听、上游、日志开关
├── web.toml                 # Web、资源路径、CDN、对象存储
├── gmsv/setup.cf             # 游戏服务配置
└── saac/acserv.cf            # 角色服务配置
```

GMSV/SAAC 的 `.example` 是模板，编辑实际 `.cf` 文件。`.env` 和生成的 `.cf` 不提交 Git。

## 2. 填写实际配置

常规参数已有默认值，按需修改以下文件：

| 文件 | 默认配置 | 按环境填写 |
| --- | --- | --- |
| [`.env` 模板](../.env.compose.example) | GHCR 版本镜像；数据在 `runtime/`；Web 8088、后台 18080 绑定回环 | 镜像版本、数据/资源路径、宿主机入口、首次管理员账号 |
| [config/gateway.toml](../config/gateway.toml) | 监听 `0.0.0.0:9065`，上游 `gmsv:9065`，日志跟踪关闭 | 通常保持默认；排障时调整 trace 开关 |
| [GMSV 模板](../config/gmsv/setup.cf.example) → `config/gmsv/setup.cf` | 9065、`fdnum=128`、单条线路 | 线路名称、游戏参数、SAAC 连接密码 `acpasswd` |
| [SAAC 模板](../config/saac/acserv.cf.example) → `config/saac/acserv.cf` | 9300、文件存档 | 服务间密码 `pass`，必须与 GMSV `acpasswd` 一致 |
| [config/web.toml](../config/web.toml) | 上游 `gateway:9065`，本地资源 | CDN 域名；需要上传时再填写 OSS/R2 参数 |

单机部署保持容器内部地址、端口与存档相对路径不变。网关认证固定开启，网关和后台共用
`.env` 指定的 SQLite 卷与数据库文件，无需安装 MySQL。

**客户端资源**：将匹配的 2.5 原版资源放入以下目录，或修改 `.env` 的 `STONEAGE_CLIENT_DATA_ROOT`：

```text
runtime/legacy-client/
├── map/
└── data/
    ├── auto.dat
    ├── bgm/
    ├── se/
    └── pal/
```

服务端数据和网页精灵图随镜像准备；上述原版资源需自行提供。

**CDN（可选）**：编辑 `config/web.toml` 已有的配置段，填写自己的 HTTPS 资源根地址：

```toml
[static.cdn]
base_url = "https://cdn.example.com/stoneage"
```

该地址下应有 `assets/`、`maps/`、`audio/`，允许游戏域名跨域 GET/HEAD。
CDN 域名没有通用默认值；留空且未配置 OSS 公开地址时使用本地资源。
填写域名不会自动上传文件。通常保持 `.env` 的 `STONEAGE_WEB_CDN_BASE_URL` 为空，避免覆盖 TOML。
OSS/R2 上传配置见 [README](../README.md#linux-docker-compose) 和 [R2 模板](../config/web.r2.toml.example)。

## 3. 部署

```bash
./scripts/deploy.sh --check
./scripts/deploy.sh
docker compose --env-file .env ps
```

默认拉取 `.env` 指定的 GHCR 版本镜像并等待六个常驻服务健康。
镜像由 GitHub Actions 在推送 `v*` tag 后构建，部署服务器不需要编译。`--check` 检查路径和 Compose，
不启动服务，但可能创建数据目录；资源完整性仍需登录游戏验收。

切换发布版本时，修改 `.env` 后执行 `./scripts/deploy.sh --pull`：

```dotenv
STONEAGE_VERSION=v实际发布版本
STONEAGE_CONTROL_IMAGE=ghcr.io/<owner>/<repo>/control-plane
STONEAGE_LEGACY_IMAGE=ghcr.io/<owner>/<repo>/legacy-runtime
```

代码、Compose 和镜像使用匹配版本；私有镜像先执行 `docker login ghcr.io`。
当前布局需要 `v0.1.11` 或更新的兼容镜像。源码开发时可将镜像仓库改成本地名称、版本设为 `local`，再用 `--build`。

## 4. 访问

默认端口只绑定服务器回环地址。在自己的电脑建立 SSH 隧道：

```bash
ssh -N -L 8088:127.0.0.1:8088 -L 18080:127.0.0.1:18080 user@服务器地址
```

1. 打开后台 `http://127.0.0.1:18080/`，用 `.env` 中的管理员账号和随机密码登录，创建游戏账号。
2. 打开游戏 `http://127.0.0.1:8088/`，用游戏账号登录、创建角色，检查地图、NPC 和音频。
3. 退出重登，确认角色和背包仍在。已有管理员的密码不会随 `.env` 初始密码变化。

正式域名通过宿主机 Caddy/Nginx HTTPS 代理到 `127.0.0.1:8088`。Caddy 示例：

```caddyfile
game.example.com {
    reverse_proxy 127.0.0.1:8088
}
```

域名需解析到服务器并开放 80/443。容器内反向代理加入项目网络后使用 `web:8088`。
后台若使用 HTTPS，设置 `STONEAGE_ADMIN_COOKIE_SECURE=true` 并限制访问来源；
HTTP 隧道访问保留 `false`。Web 玩家无需开放 9065、9300。

## 5. 维护与旧配置迁移

| 修改内容 | 应用方式 |
| --- | --- |
| `.env`、配置文件挂载路径 | `./scripts/deploy.sh --no-image-update` |
| `config/gateway.toml` | `docker compose --env-file .env restart gateway` |
| `config/web.toml` | `docker compose --env-file .env restart web` |
| `config/gmsv/setup.cf` | `docker compose --env-file .env restart gmsv` |
| SAAC 配置或两端连接密码 | 维护窗口停服，修改后用部署脚本启动 |
| 源码/镜像版本 | `./scripts/deploy.sh --build` 或 `--pull` |

后台可以编辑 GMSV/SAAC 的部分参数，保存后需重启。配置挂载整个目录，保证后台原子保存正常。
SAAC 运行目录中的 `acserv.cf` 是启动时刷新的兼容副本，只编辑 `config/` 中的文件。

旧部署先运行以下命令：目标缺失时，从 `.env` 指定的旧运行目录复制配置到 `config/`；
没有旧配置则使用模板，原文件和已有目标均保留。自定义配置目录通过
`STONEAGE_GMSV_CONFIG_DIR` / `STONEAGE_SAAC_CONFIG_DIR` 指定。

```bash
./scripts/deploy.sh --prepare-config
```

旧 `.env` 的 `STONEAGE_GATEWAY_LISTEN_HOST`、`STONEAGE_GATEWAY_UPSTREAM` 和两个 trace 变量
不再配置网关进程；将自定义值迁入 `config/gateway.toml` 对应字段。宿主机映射和认证库仍由 `.env` 配置。

常用排障与停服命令：

```bash
docker compose --env-file .env logs --tail=100 gateway web admin service-control
docker compose --env-file .env exec gmsv tail -n 100 /game/gmsv/logs/gmsv.log
docker compose --env-file .env exec saac tail -n 100 /game/saac/logs/saac.log

docker compose --env-file .env stop web gateway admin service-control
docker compose --env-file .env stop gmsv
docker compose --env-file .env stop saac
# 恢复服务
./scripts/deploy.sh --no-image-update
```

**升级前备份**：停服后同时保存 `.env`、完整 `config/`、`.secrets/`、两个服务端数据目录及
认证命名卷 `stoneage-auth`（包含可能存在的 SQLite WAL/SHM）；运维状态在 `stoneage-operator-state`。
恢复时使用同一时间点的配置、账号与角色数据，并修正路径。不要执行 `docker compose down -v`，
否则会删除认证库等命名卷。数据库说明见 [database.md](database.md)。
