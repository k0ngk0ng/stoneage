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
bucket = "stoneage-assets"
prefix = "stoneage"
```

保留模板其余配置项。`endpoint` 填地域地址，不含 bucket；`stoneage-assets.oss-cn-shanghai.aliyuncs.com` 是 bucket 的访问域名。网页与 CDN 可以用不同域名，CDN/OSS 配置允许网页域名跨域读取（GET、HEAD）；CDN 须提供 HTTPS。默认不启用 CDN，可先由 Web 提供资源。

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

## 5. 升级和备份

普通镜像升级：修改 `.env` 中的 `STONEAGE_VERSION` 为已发布 tag，再执行 `./bin/stoneage deploy`。部署工具本身升级时，先解压新包到临时目录，对比并更新 `bin/`、Compose 和配置模板；不要直接覆盖现有 `.env`、`config/`。

备份 `.env`、`config/`、`data/`、`assets/` 和 Docker 命名卷 `stoneage-auth`（账号数据库）；备份数据前先 `./bin/stoneage stop`，完成后再部署启动。若自定义卷名，以 `.env` 为准。不要执行 `docker compose down -v`，它会删除账号数据库卷。
