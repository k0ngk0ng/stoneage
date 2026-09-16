# Docker Compose 部署

服务包含 SAAC、GMSV、网关、Web 客户端、管理后台及服务控制器。需要 Linux amd64、Docker Engine 和 Compose v2+。服务器只拉取 GitHub 构建的镜像，不需要源码、Git、Go 或编译工具。

AI 玩家资金策略使用 Compose 的私有 `ai-funding` 卷。管理端将启用的策略写入该卷，
GMSV 从 `/run/stoneage/ai-funding/policies` 读取同一数据并把扣费账本写入该卷；
该卷不会挂载到 Codex AI runtime。删除或停用策略后，服务端下一次扣费检查即恢复
普通玩家的实际石币校验。

AI 运行时随发布流程单独构建为 `ai-runtime` 镜像，支持 Linux amd64 和 arm64，
内置固定版本的官方 Codex CLI `0.154.0`、`stoneage-game-mcp` 和仓库 Skills。
`control-plane` 与 `legacy-runtime` 的职责和默认 Compose 服务保持不变；其中
`legacy-runtime` 仍只有 amd64 版本。默认 Compose 不加载 AI overlay，也不会给
`admin` 增加 Docker 权限；启用容器模式时，`bin/stoneage` 会自动加载随部署包提供的
`docker-compose.ai.yml`。

启用容器模式前，在 `.env` 中设置 `STONEAGE_AI_CONTAINER_MODE=true`，并依次执行：

```bash
./bin/stoneage check
./bin/stoneage pull
./bin/stoneage deploy --no-image-update
```

`STONEAGE_AI_RUNTIME_IMAGE` 是完整的镜像引用，默认带有当前
`STONEAGE_VERSION` 标签；也可以填写已发布镜像的 `@sha256:...` 摘要。`check` 会拒绝
漂移到其他 tag 的引用，`pull` 先拉取该固定 AI 镜像，broker 启动 profile 容器时始终
使用 `--pull never`。未启用容器模式时，以上 overlay 不会被加载。

容器模式的 AI 服务使用独立的非 root 运行身份，并为每个 profile 分配唯一 named
volume，统一挂载到 `/var/lib/stoneage-ai`。broker 通过固定入口
`/usr/local/bin/stoneage-ai-runner -profile <profile-id>` 启动一次执行，随后在 stdin
写入一份 JSON 请求；镜像环境中的 `STONEAGE_AI_CODEX_BINARY`、
`STONEAGE_AI_MCP_BINARY`、`STONEAGE_AI_GIT_BINARY`、`STONEAGE_AI_SKILL_ROOT` 和
`STONEAGE_AI_STATE_ROOT` 是固定路径。不要设置或挂载镜像级 `CODEX_HOME`，runner
会在该 profile 卷内生成隔离的 Codex home、workspace、状态和能力令牌文件。
admin 进程只在容器模式 overlay 中挂载 `/var/run/docker.sock`；模型子容器不挂载
Docker socket、admin/auth 数据库、operator socket、GMSV/SAAC 存档或资金策略目录，
只接入固定的 backend 网络和该 profile 的 named volume。AI game session 连接
`gateway:9065` 的 named LSSPROTO 入口；容器内 MCP 访问 admin 的私有
`http://admin:8081/v1/game`，该端口不发布到宿主机。地图和知识读取现有
`/game/gmsv/data` 的只读挂载，资金策略卷仍与 GMSV 使用同一个 `ai-funding` 卷。
Web automation 的启动参数由 overlay 注入并与 admin 共用持久化 automation/receipt
卷。项目生成的 Codex 配置保留 `approval_policy = "never"` 与
`sandbox_mode = "danger-full-access"`；这不会改变 admin/web/operator 的权限边界。

## 1. 安装部署包

从 GitHub Release 下载 `stoneage-deploy-vX.Y.Z.tar.gz`（不要下载 Source code）并上传到服务器。程序 Release 不再附带精灵资源包；首次部署还要从已有部署或备份准备 `assets/sprites/` 和 `assets/client/`。如果需要离线精灵包，可在有仓库和资源的机器上运行 `python3 scripts/package-sprites.py vX.Y.Z --output dist` 后自行传到服务器：

```bash
mkdir -p /opt/stoneage
tar -xzf stoneage-deploy-vX.Y.Z.tar.gz -C /opt/stoneage
cd /opt/stoneage
./bin/stoneage init
```

`init` 生成 `.env`、随机后台密码及服务端配置，不覆盖已有配置。

```text
/opt/stoneage/
├── .env                       # 镜像版本、端口、挂载路径、后台初始账号
├── docker-compose.yml
├── VERSION                    # 部署包版本
├── README.md
├── bin/stoneage                # 运维统一入口
├── config/
│   ├── web/web.toml            # Web、OSS、CDN
│   ├── gateway/gateway.toml    # 网关
│   ├── gmsv/setup.cf           # 游戏服务端
│   ├── saac/acserv.cf          # 账号服务端
│   ├── registry/              # GHCR 用户名、token、登录缓存
│   ├── certificates/          # CDN 自动证书配置及专用密钥
│   └── secrets/               # OSS 密钥
├── assets/
│   ├── sprites/                # 独立资源包：精灵图片及索引
│   └── client/                 # 公开客户端地图、音频、调色板
├── data/{gmsv,saac}/           # 游戏运行数据
└── backups/                   # 配置与数据备份
```

## 2. 配置

通常只需检查 `.env` 中的后台账号、访问端口，以及 `config/web/web.toml` 中的 OSS/CDN；GMSV、SAAC 和网关默认值可直接使用。不要修改镜像内路径。

默认仅监听本机：Web `8088`、后台 `18080`、网关 `9065`。已有 Nginx 可将网页域名反代到 `127.0.0.1:8088`，后台域名反代到 `127.0.0.1:18080`。反代保留 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`；后台启用 HTTPS 后设置 `.env` 的 `STONEAGE_ADMIN_COOKIE_SECURE=true`。需要直接访问时再修改对应 `STONEAGE_*_BIND`。

审计来源 IP 只接受可信代理提供的 `X-Forwarded-For`（从右向左解析），没有该头时使用 `X-Real-IP`。宿主机 Nginx 经 Docker 端口转发时，在 `.env` 的 `STONEAGE_ADMIN_TRUSTED_PROXIES` 中加入管理容器看到的网关 IP；当前生产网关为 `192.0.2.1`，对应配置为 `127.0.0.1/32,::1/128,192.0.2.1/32`。不要信任整个内网网段；网关改变时同步更新该配置。此设置通过 Compose 传入管理容器，更新部署工具包时需带上新的 `docker-compose.yml`。旧审计记录保留原值。

游戏登录审计（`game_login_success` / `game_login_failed`）由网关写入。Compose 为 Web 启用客户端 IP 传递：Web 使用同一可信 Nginx 列表解析来源 IP（可用 `STONEAGE_WEB_TRUSTED_PROXIES` 单独覆盖），再通过 TCP 连接的 PROXY v1 首行传递；网关只接受 Docker DNS 名 `web` 对应的连接来源，容器 IP 变化后仍可识别。该首行由网关消费，不转发给 GMSV；原生 TCP 客户端仍记录实际 TCP 对端 IP。更新时需同时发布新的 Web/网关镜像和 `docker-compose.yml`，不需要修改 Nginx 或重新上传资源。

本次服务器的网页入口为 `https://sa.ichenj.com`，Nginx 配置在 `config/nginx/sa.ichenj.com.conf`，通过 `/etc/nginx/conf.d/` 的软链接加载，反代到 `127.0.0.1:8088`。Certbot 管理该域名证书，定时自动续期并重载 Nginx。维护命令：

```bash
nginx -t && systemctl reload nginx                  # 修改站点配置后
certbot renew --cert-name sa.ichenj.com --dry-run     # 检查自动续期
```

网页证书与 CDN 域名证书分别管理。CDN 自动证书配置见 `config/certificates/README.md`：服务器使用 Certbot DNS-01 续签，并调用阿里云 API 部署到 CDN，无需本地 CLI 或容器挂载。CDN 跨域响应头应允许 `https://sa.ichenj.com`。

将匹配的 2.5 客户端公开资源放入 `assets/client/`：

```text
map/
data/auto.dat
data/bgm/
data/se/
data/pal/
```

精灵图片在 `assets/sprites/`，应用镜像不包含游戏资源，也不需要复制 `client/` 源码。`.env` 中 `./` 开头的路径相对部署目录；TOML 的 `/game/...`、`/opt/stoneage/web-assets` 是容器内绝对路径。

后台账号详情中的“管理玩家资产”可选择角色，查看并修改随身和仓库的物品、宠物、技能、石币及属性。搜索目录来自当前 GMSV 数据，物品图标与宠物站立预览使用 Web 的同一套本地资源。更新此功能时须同时更新管理端、SAAC、GMSV 镜像和 Compose 文件；新增的 `player-admin` 私有卷只供这三个服务交换请求，不发布端口。已有资源无需重新上传。

在线修改由 GMSV 主循环执行并确认 SAAC 存档，离线修改由 SAAC 检查角色锁及原存档后写入。页面提示数据已变化时应重新加载；保存待确认时先刷新检查，避免重复赠送。管理端不直接写角色存档。非 Compose 启动可通过 `STONEAGE_PLAYER_ADMIN_ROOT`、`STONEAGE_PLAYER_CATALOG_ROOT`、`STONEAGE_PLAYER_ASSETS_ROOT` 指定请求目录、游戏数据和图片资源目录；SAAC/GMSV 的 `STONEAGE_PLAYER_ADMIN_DIR` 分别指向根目录下的 `saac`、`gmsv` 子目录。

使用阿里云 OSS 时，编辑 `config/web/web.toml` 中已有配置项：

```toml
[static.cdn]
base_url = "https://cdn.example.com/stoneage"

[static.oss]
provider = "aliyun-oss"
endpoint = "https://oss-cn-shanghai.aliyuncs.com"
region = "cn-shanghai"
bucket = "stoneage-assets"
prefix = "stoneage"
```

同地域阿里云 ECS 上传可将 `endpoint` 改成 `https://oss-cn-shanghai-internal.aliyuncs.com`；本地上传使用公网 endpoint。CDN 回源只选择 OSS bucket，不使用内网 endpoint。

保留模板其余配置项。`endpoint` 填地域地址，不含 bucket；`stoneage-assets.oss-cn-shanghai.aliyuncs.com` 是 bucket 的访问域名。网页与 CDN 可以用不同域名，CDN/OSS 配置允许网页域名跨域读取（GET、HEAD）；CDN 须提供 HTTPS。CDN 源站只选择 OSS bucket，不追加 `/stoneage` 回源目录、不改写路径；私有 bucket 启用 CDN 回源授权，缓存遵循 OSS 的 `Cache-Control`，不要强制长期缓存 JSON 清单。默认不启用 CDN，可先由 Web 提供资源。

## 3. 配置镜像拉取凭据

私有 GHCR 使用有两个镜像包读取权限的 GitHub 账号，可以是其他账号或专用部署账号。创建 **PAT classic**，仅勾选 `read:packages`，并确认账号拥有包的 Read 权限。SSH Deploy Key 只能拉 Git 仓库，不能用于 GHCR。

在服务器 Bash 中执行一次（token 不回显、不进入命令历史）：

```bash
cd /opt/stoneage
install -d -m 700 config/registry
read -r -p 'GitHub 用户名: ' registry_user
read -r -s -p 'GitHub token: ' registry_token; echo
(umask 077; printf '%s\n' "$registry_user" > config/registry/username
 printf '%s\n' "$registry_token" > config/registry/token)
unset registry_user registry_token
./bin/stoneage login
```

后续拉取、部署自动读取这两个文件；资源上传不使用 GHCR。更换账号或续期时更新文件即可，登录缓存保存在 `config/registry/docker/`。凭据未配置时使用当前 Docker 登录状态。

## 4. 启动和上传资源

```bash
./bin/stoneage check
./bin/stoneage deploy
./bin/stoneage status
./bin/stoneage logs --tail=100 -f
```

启动后用 `.env` 中的后台账号登录管理后台，再创建游戏账号。容器首次启动自动初始化服务端数据。

需要 OSS/CDN 时，将 AccessKey ID 和 Secret 分别写入以下文件（仅资源上传需要，内容不加引号）：

```bash
install -d -m 700 config/secrets
# 用编辑器分别填写：
vi config/secrets/oss-access-key-id
vi config/secrets/oss-access-key-secret
chmod 600 config/secrets/oss-access-key-*
./bin/stoneage sync-assets --dry-run
./bin/stoneage sync-assets
```

`--dry-run` 校验资源但不上传。上传工具已包含在 Linux 部署包内，直接运行，不使用 Docker、不拉取镜像。正式同步一次发布图片、地图和音频，只上传变化文件。CDN 的 URL 路径必须与 OSS `prefix` 一致（上例均为 `stoneage`），网页直接从 CDN 加载资源。

本地与服务器均支持以上上传命令。本地解压部署包、准备资源副本和 `assets/client/`，执行 `init`，填写 Web/OSS 配置及 OSS 密钥即可；无需 Docker 或 GHCR 凭据。Mac 额外下载对应架构的 `stoneage-assets-sync-vX.Y.Z-darwin-arm64.tar.gz`（Intel 用 `darwin-amd64`），在部署目录解压覆盖 `bin/stoneage-assets-sync`。服务器保留完整 `assets/` 副本和 AK/SK，方便任选一端上传；本版本不实现 CDN 故障自动回退。

## 5. 下次更新

只使用 GitHub Release 中已发布成功的 tag。下面的 `vX.Y.Z` 替换成目标版本；服务器不拉源码、不编译，也不重新执行 `init`。

**只更新程序（通常这样操作）**

```bash
cd /opt/stoneage
vi .env                         # 将 STONEAGE_VERSION 改为目标 tag
./bin/stoneage check
./bin/stoneage pull              # 保持现有服务运行，先完成镜像下载
./bin/stoneage deploy --no-image-update  # 镜像就绪后切换容器并等待健康检查
./bin/stoneage status
```

配置、密钥、存档和资源会保留。GHCR 凭据配置一次即可，只有 token 到期或更换账号时才需更新。未变更资源时不需要重新上传 OSS。

v0.1.17 只需更新程序镜像，无需更新部署工具或重新上传 OSS。该版本修复资源缓存失败引起的重复下载、人物清单无限重试、鱼骨鼠标热点偏移，以及战斗目标切换、治疗 NPC 隔柜交互和行走画面对齐。部署后关闭旧游戏标签页并重新打开，以加载新的页面与 Service Worker。

该版本同时将游戏密码统一限制为 1–12 位半角可打印字符（不含空格）。已有 13–15 位密码的账号需在管理后台重置为 12 位以内；账号和存档不会被删除。

**发布说明要求更新部署工具或 Compose 时**

下载目标版本的 `stoneage-deploy-vX.Y.Z.tar.gz` 并上传到 `/opt/stoneage`。确认没有资源上传任务正在运行，然后仅更新部署程序和模板：

```bash
cd /opt/stoneage
tar -xzf stoneage-deploy-vX.Y.Z.tar.gz \
  bin docker-compose.yml .env.compose.example README.md VERSION
rm stoneage-deploy-vX.Y.Z.tar.gz
# 按发布说明对照 .env.compose.example 补充新增配置项；不要覆盖现有 .env/config
vi .env                         # 将 STONEAGE_VERSION 改为同一个 tag
./bin/stoneage check
./bin/stoneage pull
./bin/stoneage deploy --no-image-update
./bin/stoneage status
```

这会同时更新独立上传工具，保留 `.env` 和整个 `config/`。如果发布说明新增了服务配置文件，单独提取该模板并按说明配置。`VERSION` 表示部署工具包版本，实际镜像版本以 `.env` 为准。

**资源有更新时**

准备新的精灵资源包或资源副本（可在仓库工作树运行 `python3 scripts/package-sprites.py vX.Y.Z --output dist` 生成），复制到 `/opt/stoneage`；确认没有上传任务正在运行后解压，再同步 OSS：

```bash
cd /opt/stoneage
tar -xzf stoneage-sprites-vX.Y.Z.tar.gz
rm stoneage-sprites-vX.Y.Z.tar.gz
# 地图/音乐有变化时，将对应公开文件更新到 assets/client/
./bin/stoneage sync-assets --dry-run
./bin/stoneage sync-assets
```

上传成功后才更新 OSS 发布清单和版本标记。若发布说明列出已删除的资源文件，按清单清理本地对应文件；直接解压只会新增和覆盖文件。

**回退与备份**

更新完成后，可清理未被任何容器使用的其他版本 StoneAge 镜像：

```bash
cd /opt/stoneage
# 发版后保留上一版用于回退（将 vX.Y.Z 替换为实际上一版）：
./bin/stoneage clean --dry-run --keep-version vX.Y.Z
./bin/stoneage clean --keep-version vX.Y.Z
```

命令读取 `.env` 中的 `STONEAGE_VERSION`、`STONEAGE_CONTROL_IMAGE` 和 `STONEAGE_LEGACY_IMAGE`，只删除这两个仓库的其他版本标签。当前版本的两个镜像必须已存在，否则停止清理并提示先部署。运行中或已停止容器使用的镜像、与当前版本共享镜像 ID 的标签都会保留。不会清理其他项目镜像、无标签镜像、构建缓存、容器、数据卷或存档；被删除的旧版本需要重新拉取后才能回退。可用 `--env FILE` 指定其他环境文件。

`--keep-version TAG` 可重复指定，保留本机已有的对应版本镜像，不主动拉取镜像。生产发版清理时应指定上一版，保留回退能力。

仅镜像更新且发布说明确认数据格式兼容时，可将 `.env` 的 `STONEAGE_VERSION` 改回上一版本再部署。若本次同时改过 Compose、配置、资源或数据格式，应使用对应版本的工具和备份恢复。

只更新程序镜像、未改资源时不做备份，不提前手动停服。`bin/stoneage pull` 在现有服务运行时完成下载，随后 `bin/stoneage deploy --no-image-update` 负责停止旧容器、启动新容器，尽量缩短服务不可用时间。资源变更的备份需求另行明确，不能把全量备份作为普通发版的前置步骤。

部署、停服、回退必须使用 `bin/stoneage`；禁止临时编写 SSH/脚本编排。流程需要调整时，只在仓库的 `bin/stoneage` 入口修改，提交并发布后使用。不要执行 `docker compose down -v`，它会删除账号数据库卷。

GMSV 启动失败时查看 `data/gmsv/logs/gmsv.log`，SAAC 查看 `data/saac/logs/saac.log`。v0.1.16 修复首次初始化缺少 `data/gmsv/log/log.cf` 导致 GMSV 不健康的问题，并保留已有日志配置。

### 管理后台服务器清单与时间

两个运行时镜像均安装时区数据并默认使用 `Asia/Shanghai`（UTC+8）。游戏欢迎语中的服务器时间及服务本地时间使用该时区；管理后台带时区的时间戳仍按浏览器本地时区展示。

Web 选服列表和管理后台共用 `config/gateway/gateway.toml` 的服务器目录：

```toml
catalog_listen_address = "0.0.0.0:9080"

[[servers]]
id = "line-1"
name = "一线"
listen_address = "0.0.0.0:9065"
address = "gateway:9065"
upstream_address = "gmsv:9065"
disabled = false

[[servers]]
id = "line-2"
name = "二线"
listen_address = "0.0.0.0:9066"
address = "gateway:9066"
upstream_address = "gmsv2:9065"
disabled = false
```

`id` 应保持稳定；修改名称不影响重连线路。`listen_address` 是网关监听地址，
`address` 是 Web 后端可访问的网关 TCP 地址，`upstream_address` 指向对应 GMSV。
每条线路使用不同监听端口，设置 `disabled = true` 后该线路不再接受新连接。
配置仅连接已有 GMSV，不会自动创建额外游戏容器。原版 TCP 客户端需要在 Compose
中额外发布对应端口；Web 通过容器网络连接，不需要增加对外端口。

网关提供内部 `GET /api/servers` 接口，Web 的 `gateway_api_url` 配置为
`http://gateway:9080`；service-control 使用同一地址的 `STONEAGE_GATEWAY_API_URL`。
浏览器访问 Web 同源 `/api/servers`，只接收线路 ID、名称和开放状态；建立会话时
只提交 ID，不能指定任意 TCP 地址。网关内部目录端口不应发布到公网。
修改网关配置后重启 gateway，重新加载网页即可看到更新。若更改目录端口，需同时更新 Web 的 `gateway_api_url` 和 service-control 的 `STONEAGE_GATEWAY_API_URL`。

“服务”页通过目录中的 GMSV 地址查询原版 `PlayerNumGet`，展示已进入游戏的角色
人数，而非 TCP 连接数。查询失败显示“未知”，总人数仅累加已知线路；维护线路
不会发起探测。最多配置 32 条线路。没有启用目录的旧部署仍可使用单一 TCP 上游，
但要使用可配置选服列表，应同步更新 gateway 配置、Web 配置和 service-control。

管理后台的账号、审计、部署、同步和服务器查询时间统一按浏览器本地时区显示，
数据库和接口仍保存带时区的时间。资源页“开始同步”会实际调用对象存储上传器，
需要配置 `web.toml` 的存储参数并挂载对应凭据和资源目录；任务运行时按钮禁用，
刷新页面可查看执行状态。它不会把 CDN 上的资源下载到游戏客户端。
