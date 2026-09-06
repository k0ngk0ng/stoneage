# Docker Compose 部署

服务包含 SAAC、GMSV、网关、Web 客户端、管理后台及服务控制器。需要 Linux amd64、Docker Engine 和 Compose v2+。服务器只拉取 GitHub 构建的镜像，不需要源码、Git、Go 或编译工具。

## 1. 安装部署包

从 GitHub Release 下载 `stoneage-deploy-vX.Y.Z.tar.gz`（不要下载 Source code），以及独立资源包 `stoneage-sprites-vX.Y.Z.tar.gz`，上传到服务器后：

```bash
mkdir -p /opt/stoneage
tar -xzf stoneage-deploy-vX.Y.Z.tar.gz -C /opt/stoneage
tar -xzf stoneage-sprites-vX.Y.Z.tar.gz -C /opt/stoneage
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

本地与服务器均支持以上上传命令。本地解压部署包和资源包，执行 `init`，准备 `assets/client/`，填写 Web/OSS 配置及 OSS 密钥即可；无需 Docker 或 GHCR 凭据。Mac 额外下载对应架构的 `stoneage-assets-sync-vX.Y.Z-darwin-arm64.tar.gz`（Intel 用 `darwin-amd64`），在部署目录解压覆盖 `bin/stoneage-assets-sync`。服务器保留完整 `assets/` 副本和 AK/SK，方便任选一端上传；本版本不实现 CDN 故障自动回退。

## 5. 下次更新

只使用 GitHub Release 中已发布成功的 tag。下面的 `vX.Y.Z` 替换成目标版本；服务器不拉源码、不编译，也不重新执行 `init`。

**只更新程序（通常这样操作）**

```bash
cd /opt/stoneage
vi .env                         # 将 STONEAGE_VERSION 改为目标 tag
./bin/stoneage check
./bin/stoneage deploy            # 自动认证、拉取镜像、更新容器并等待健康检查
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
./bin/stoneage deploy
./bin/stoneage status
```

这会同时更新独立上传工具，保留 `.env` 和整个 `config/`。如果发布说明新增了服务配置文件，单独提取该模板并按说明配置。`VERSION` 表示部署工具包版本，实际镜像版本以 `.env` 为准。

**资源有更新时**

下载新的 `stoneage-sprites-vX.Y.Z.tar.gz` 到 `/opt/stoneage`；确认没有上传任务正在运行后解压，再同步 OSS：

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
./bin/stoneage clean --dry-run   # 仅列出待清理的镜像
./bin/stoneage clean             # 执行清理
```

命令读取 `.env` 中的 `STONEAGE_VERSION`、`STONEAGE_CONTROL_IMAGE` 和 `STONEAGE_LEGACY_IMAGE`，只删除这两个仓库的其他版本标签。当前版本的两个镜像必须已存在，否则停止清理并提示先部署。运行中或已停止容器使用的镜像、与当前版本共享镜像 ID 的标签都会保留。不会清理其他项目镜像、无标签镜像、构建缓存、容器、数据卷或存档；被删除的旧版本需要重新拉取后才能回退。可用 `--env FILE` 指定其他环境文件。

仅镜像更新且发布说明确认数据格式兼容时，可将 `.env` 的 `STONEAGE_VERSION` 改回上一版本再部署。若本次同时改过 Compose、配置、资源或数据格式，应使用对应版本的工具和备份恢复。

更新前备份 `.env`、`config/`、`data/`、`assets/` 和 Docker 命名卷 `stoneage-auth`（账号数据库）；备份数据前先 `./bin/stoneage stop`，完成后再部署启动。若自定义卷名，以 `.env` 为准。不要执行 `docker compose down -v`，它会删除账号数据库卷。

GMSV 启动失败时查看 `data/gmsv/logs/gmsv.log`，SAAC 查看 `data/saac/logs/saac.log`。v0.1.16 修复首次初始化缺少 `data/gmsv/log/log.cf` 导致 GMSV 不健康的问题，并保留已有日志配置。

### 管理后台服务器清单与时间

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
