# StoneAge Revival

让经典《石器时代》重新成为一套可维护、可联网、可在现代 macOS 与 Windows 上运行的游戏。

当前可玩基线已经固定为 **StoneAge 2.5**：保存下来的 32 位 Windows
客户端经兼容补丁后，在 Apple Silicon macOS 的 Wine 11 中可连接容器内的
Linux 2.5 服务端。旧档案只作为只读输入；日常运行不再依赖移动硬盘。

浏览器 Web 后端是 Go（`client/web`），不是 Node.js；页面前端是随 Go 二进制嵌入的
HTML/CSS/JavaScript。Web 进程只负责游戏协议和公开静态资源 URL，对象存储上传由独立的
`stoneage-assets-sync` 控制面命令完成。

## 已验证状态（2026-08-13）

- macOS 26.5.2 / Apple Silicon / Wine 11：登录、人物、地图、移动、聊天、
  多人、宠物、物品及完整战斗均已通过实机验证。
- 两个独立 Wine prefix 的真实 2.5 客户端已同时进入同一 GMSV；第二人物移动后
  坐标从 `(15,22)` 保存为 `(16,25)`，并发连接与断线存档通过。
- Linux 2.5 GMSV 的战斗 `BC` 每角色 13 字段已由 Go 网关转换为旧客户端的
  8 字段；攻击、回合结算和返回地图均已验证。
- 登录公告 `WN` 窗口与确认包已验证；2026-09-01 又用独立静音无头 Web 会话和
  `probe` 账号在 2.5 本机地图完成了 NPC 选择、靠近、位置确认、`TK(P|hi)` 往返
  及 NPC 回复闭环（未使用 `player_demo`）。
- Win11：客户端仍是原生 x86 PE，配套 cnc-ddraw；需在 Win11 实机完成最终
  回归，步骤见 [`docs/windows.md`](docs/windows.md)。
- 界面用词虽偏繁体，二进制文本实际是 **CP936/GBK**，不是 CP950。Wine
  prefix 必须使用 ACP 936。
- 发布服务端使用独立 SQLite 认证库（Argon2id 密码哈希、管理员会话、审计日志），
  SAAC 仍保存人物、邮件和家族平面文件。SQLite 不替代角色目录，也不需要 MySQL。

## macOS 一键运行

首次需要 Docker Desktop、Go、Wine 11，以及已经保存在本目录中的
`vendor/`、`assets/client/` 资产。随后执行：

```bash
./scripts/start-local.sh
```

测试账号为 `probe` / `local`，人物为 `ProbeHero`。在客户端中依次选择
“本機”和“本機一線”。服务器/线路列表默认只有本机一线；需要自定义多个
服务器或线路时，复制 `config/client-servers.toml.example` 为
`runtime/client-servers.toml` 后再启动，脚本会自动嵌入旧客户端。也可以设置
`STONEAGE_CLIENT_SERVERS_URL` 从 HTTPS 接口动态获取。启动器只生成
`sa_2903-local.exe` 副本，原始 `sa_2903.exe` 保持不变。查看状态或停止：

```bash
./scripts/status-local.sh
./scripts/stop-local.sh
```

本地 `start-local.sh` 默认保留无认证开发模式。部署发布包时使用 Linux 目录下的
`start-server.sh` 和 `start-admin.sh`；游戏网关会强制校验 SQLite 账号，后台提供
账号创建、禁用、重置密码、审计、独立通知页面、GMSV/SAAC 配置编辑，以及游戏
网关/GMSV/SAAC/全部服务的受限重启。配置保存不会自动重启，首次启动后台前创建一次
管理员：

```bash
printf '%s\n' '强密码' | ./bin/stoneage-admin create-admin \
  -db runtime/stoneage-auth.db -username admin -password-stdin
```

后台默认位于 `http://127.0.0.1:8080/`，生产环境请放在 HTTPS 反向代理后。

### Linux Docker Compose

完整服务端 + Web 客户端的部署步骤见 [Docker Compose 部署文档](docs/docker-compose.md)，
包含 GMSV/SAAC 初始化、网关、后台、浏览器访问、HTTPS、升级与备份恢复。

下载 Release 中的 `stoneage-deploy-vX.Y.Z.tar.gz` 到 `/opt/stoneage`，使用
`./bin/stoneage init` 生成配置，再运行 `./bin/stoneage deploy`。服务器只拉取
预构建镜像，不需要源码或编译工具。配置统一放在 `config/<服务>/`，运行数据在
`data/`，匹配的 2.5 客户端公开地图和音频在 `assets/client/`；精灵图片使用独立资源包，放在 `assets/sprites/`。

图片、地图、音效和音乐可以直接由阿里云 OSS、Cloudflare R2/CDN 提供，避免所有玩家从 8088
重复下载大文件。资源使用一个固定根目录，版本发布时做增量同步，不按 tag 重复
保存整套原版数据；根目录下保持下面的目录名：

```text
stoneage/
├── assets/   # client/web/assets/original 的内容
├── maps/     # 2.5 客户端 map/ 的内容
└── audio/    # 公开客户端数据（data/auto.dat、data/bgm/、data/se/、data/pal/）
```

不要分别手工上传这三个目录：这样容易让 CDN 在发布过程中看到半套
客户端。使用仓库内的同步器一次发布完整客户端；它会先校验并计算三棵目录的
SHA-256，只上传变化文件，最后才更新发布清单：

```bash
./bin/sync-assets.sh --dry-run  # 检查来源和对象数量
./bin/sync-assets.sh             # 发布 assets、maps、audio
```

该命令只读取客户端公开资源（精灵图、地图、`auto.dat`、BGM、SE 和调色板），
不会上传存档、聊天记录、服务端数据或 Admin。若由 CI 发布，也可以直接运行
`stoneage-assets-sync`；Admin 页面上的“开始同步”只是请求同一个固定任务，不能
指定本地路径、bucket 或任意命令。

网页端已注册同源 `/sw.js`：`assets/`、`maps/`、`audio/` 的 GET 请求使用
Service Worker + Cache Storage 的 cache-first 策略，网络不可用时仍可读取已缓存文件；
API、会话轮询和 HTML 不会进入缓存。发布器在全部对象上传完成后最后写入轻量
`stoneage/_client-version.json`（revision、对象数、总字节数），网页只读取这个小文件
来切换缓存命名空间，再按需下载登录/UI/当前地图资源。进入世界后会在空闲时预热少量常用
BGM/SE，后续地图和大精灵表仍然按需加载并显示下载进度。

目前没有把 1.2GB 资源强制打成单个 `.pack` 或写入 OPFS：这会让 2.5 客户端兼容、
CDN 增量发布和首次边玩边下变差。待资源包确实达到数 GB 时，再按地图/音频/精灵分片
生成带索引的 pack，并以 OPFS 作为可选加速层；Cache Storage 始终保留为回退路径。
revision 切换会建立新的 Service Worker Cache Storage 命名空间；发布器根据完整
`_client-manifest.json` 生成变更/删除路径，并附带 delta 的基准 revision。Worker 只有在
浏览器当前缓存正好是这个基准版本时，才会安全复用上一个命名空间中未变化的对象；跳过版本、变化或删除对象都会走网络，避免误用旧内容。旧版 marker 没有 delta 信息时按全量更新处理。

Web 后端启动时读取 [`config/web/web.toml`](config/web/web.toml)。在
`static.oss` 中填写对象存储的 `provider`、`endpoint`、`region`、`bucket` 和固定
`prefix`，在 `static.cdn.base_url` 中填写 CDN 公开根地址。阿里云 OSS 例如：

```toml
[static.oss]
provider = "aliyun-oss"
endpoint = "https://oss-cn-hangzhou.aliyuncs.com"
region = "cn-hangzhou"
bucket = "my-stoneage-assets"
prefix = "stoneage"

[static.cdn]
base_url = "https://cdn.example.com/stoneage"
```

Cloudflare R2 使用相同配置格式：`provider = "cloudflare-r2"`、endpoint 为
`https://<account-id>.r2.cloudflarestorage.com`、region 为 `auto`，CDN 填写
Cloudflare 自定义域名（完整模板见 [`config/web/web.r2.toml.example`](config/web/web.r2.toml.example)）。
R2 的 S3 API 默认不是公开下载端点，因此生产环境应配置 CDN；Web 不会把带签名的
R2 API 地址暴露给浏览器。

`prefix` 和 `base_url` 都是跨版本复用的固定根目录，不追加 `v*` tag。Compose
通过 `.env` 的 `STONEAGE_WEB_CONFIG_FILE` 把这个 TOML 配置只读挂载到 Web 容器；AccessKey
明文只应放在权限为 0600 的 secret 文件中，不应直接写进 TOML 或提交到 Git。CDN 有值时优先
使用 CDN；CDN 留空但阿里云 OSS endpoint/bucket 已配置时，后端会直接生成公开 OSS 根地址。R2
不会自动暴露 S3 endpoint。网页会把
`/assets/`、`/maps/`、`/audio/` 直接改写到该地址；登录、NPC 和游戏协议 API
仍只访问 8088。当前资源模式是公开只读 OSS/R2/CDN；AK/SK 建议分别写入权限为 0600 的
`config/secrets/oss-access-key-id` 和 `config/secrets/oss-access-key-secret`（或由 CI 注入），由一次性
批量资源同步工具读取，Web 游戏进程和 admin HTTP 进程不会读取或上传。admin 页面如果启用
同步，只能请求 service-control 的固定整包任务；service-control 只挂载这两个 Docker secret
文件，并在任务启动时传给上传子进程，任务结束后不保留密钥。OSS/R2/CDN 必须允许网页正式域名进行跨域 `GET`/`HEAD`，并正确返回
JSON、PNG、WAV 和二进制文件的 MIME 类型；建议允许 `Range`，暴露
`Content-Length`、`Content-Range`、`Accept-Ranges`、`ETag`。生产环境必须使用
HTTPS，并为精确下载进度返回 `Timing-Allow-Origin`，否则 HTTPS 网页会拦截混合
内容。`assets/*.json` 等清单应使用短缓存或 `no-cache`；PNG、地图和音频可以长期
缓存。同名二进制确实发生变化时，只刷新对应 CDN URL，不需要复制整套资源目录。

客户端资源是一个整体发布单元，生产发布时建议由部署脚本或 CI 执行。Admin 是独立
的运维应用，不会被打进客户端资源包，也不会拿到 OSS/R2 AK/SK：

```bash
./bin/sync-assets.sh --dry-run   # 先检查目录和对象数量
./bin/sync-assets.sh              # 一次发布 assets、maps、audio
```

同步器会在固定根目录写入 `stoneage/_client-manifest.json` 和轻量的
`stoneage/_client-version.json`。完整清单记录每个公开对象的大小和 SHA-256；版本文件只记录
revision、对象数和总字节数。后续发布只上传变化的文件，全部成功后才更新清单，失败重试不会把
客户端清单提前切到半套资源；上传器还会在写入清单前确认源文件没有在上传过程中被替换。资源 URL 仍然是不带 tag 的固定
`stoneage/{assets,maps,audio}/`，因此不会按版本复制整套客户端。每次发布先上传图片、
地图和音频，再上传浏览器索引（`*.json` 与 `audio/auto.dat`），最后才写发布清单，
避免 CDN 在发布中途拿到新索引却找不到对应资源。

首次配置 OSS 或 R2 时，把凭据分别写入部署机上的两个 secret 文件（不要把值提交到
`.env` 或 Git）：

```bash
install -d -m 700 .secrets
read -r -s -p 'OSS AccessKey ID: ' oss_id; echo
read -r -s -p 'OSS AccessKey Secret: ' oss_secret; echo
printf '%s\n' "$oss_id" > config/secrets/oss-access-key-id
printf '%s\n' "$oss_secret" > config/secrets/oss-access-key-secret
unset oss_id oss_secret
chmod 600 config/secrets/oss-access-key-id config/secrets/oss-access-key-secret
```

R2 这里填写 Cloudflare 控制台创建的 R2 API Token（Access Key ID/Secret
Access Key），不是 Cloudflare Global API Key；该 Token 只需 `Object Read & Write`
到目标 bucket。若使用 CI 临时环境，也可用 `AWS_ACCESS_KEY_ID`/
`AWS_SECRET_ACCESS_KEY` 或 `CLOUDFLARE_R2_ACCESS_KEY_ID`/
`CLOUDFLARE_R2_SECRET_ACCESS_KEY`，同步脚本会优先使用 0600 secret 文件。

脚本启动 Compose 的一次性 `assets-sync` profile；任务结束后容器即被删除，Web、网关和
admin HTTP 进程永远不会拿到 AK/SK，也不会因为上传而重启。目标仍是固定的
`stoneage/{assets,maps,audio}/` 根目录，不包含 release tag。同步器只上传公开的
`client/web/assets/original`、`map/`、`data/auto.dat`、`data/bgm/`、`data/se/` 和 `data/pal/`；不会把
`savedata.dat`、聊天记录、PE 支持文件等 `data/` 私有内容上传。CI 也可以直接调用同一个
`stoneage-assets-sync` 二进制或等价的 OSS/R2 同步步骤。admin 的资源按钮（如启用）只会
请求 service-control 的固定整包任务，不接受路径、bucket 或命令参数；service-control
只读挂载两个 Docker secret，不会把它持久化进镜像或传给 admin。

使用 GitHub Release 镜像时，先在服务器执行 `docker login ghcr.io`（若仓库为私有），
再把 `.env` 中的两个镜像仓库写成 `ghcr.io/<owner>/<repo>/control-plane` 和
`ghcr.io/<owner>/<repo>/legacy-runtime`，填入对应的 `v*` 版本并运行
`./bin/deploy.sh --pull`。

Compose 默认把浏览器端 8088、游戏网关 9065 和后台 18080 绑定到 `127.0.0.1`；
需要让朋友访问网页时，把 `STONEAGE_WEB_BIND` 设为服务器的 LAN/VPN 地址（或
`0.0.0.0`）并只在防火墙放行 8088。原生客户端才需要 9065；后台应继续绑定回环，
或放在带 HTTPS 和访问控制的反向代理后，并把 `STONEAGE_ADMIN_COOKIE_SECURE` 设为
`true`。不要执行 `docker compose down -v`，否则会删除 SQLite 认证卷。
GMSV 的 9065、SAAC 的 9300 只在 Compose 内网可见。`saac` 与 `gmsv` 是独立容器；
operator 通过固定脚本控制
Compose 容器，所以需要只给它挂载 `/var/run/docker.sock`；不要把该 socket 挂载到
后台容器，也不要把后台直接暴露到公网。账号、SQLite 数据和角色目录均由卷或绑定
目录持久化。网页中的网关、GMSV、SAAC 停止/重启操作分别对应各自容器；SAAC 停止
时 GMSV 不能正常登录，生产维护时应先停止 GMSV 再停止 SAAC。

生产环境把 `.env` 中的 `STONEAGE_CONTROL_IMAGE` 和 `STONEAGE_LEGACY_IMAGE` 设置为
GHCR 的镜像仓库（不带 Tag），`STONEAGE_VERSION` 设置为已发布的 `v*` Tag，并将
`STONEAGE_GMSV_DATA_ROOT`、`STONEAGE_SAAC_DATA_ROOT`、`STONEAGE_PROJECT_ROOT`
设置为宿主机绝对路径。这样后台“版本”页面才会启用下发；版本更新只替换镜像和
静态资源，不覆盖这些数据目录。不要执行 `docker compose down -v`，否则会删除
SQLite 认证卷。

当前 Compose 文件只覆盖单机单线路部署。客户端启动器可以列出分别部署在其他
服务器上的游戏入口，但不要在这份 Compose 中追加第二个 GMSV；多节点编排将在
后续单独设计。

首次启动后访问 `http://127.0.0.1:18080/`。如果没有在 `.env` 设置管理员账号密码，
可设置一次性的 `STONEAGE_ADMIN_SETUP_TOKEN` 后从 `/setup` 初始化管理员。

局域网和互联网部署见 [`docs/networking.md`](docs/networking.md)。当前本机服务
仅绑定 `127.0.0.1`，不要把历史 SAAC/GMSV 直接暴露到公网。

## 发布产物

执行下面的命令会在 `dist/` 生成 macOS Apple Silicon 应用、Win11 x64
联机包和 Linux amd64 服务端包：

```bash
./scripts/build-release.sh
```

构建输出和 SHA-256 清单会写入 [`dist/`](dist/)（该目录是本地生成目录，不随 Git
保存）。macOS 应用仍需要外部 Wine 11 与 Docker Desktop/OrbStack；Win11 客户端
本身不需要 Wine。发布说明见
[`docs/release-macos.md`](docs/release-macos.md)、
[`docs/windows.md`](docs/windows.md) 和
[`docs/verified-features.md`](docs/verified-features.md)。

推送 `v*` 标签会触发 [GitHub Actions release workflow](.github/workflows/release.yml)，
从仓库内受 Git 管理的 `server/legacy/source/2.5` 构建 amd64 GMSV/SAAC 镜像和 Go
控制面镜像，推送到 GHCR 并发布 digest。完整的服务端版本不再依赖开发者本机的
私有源码归档；运行中的角色、邮件、家族和账号数据仍只保存在部署机的持久化目录。

## 当前目标

1. 完成真实 NPC 对话按钮与 Win11 实机回归。
2. 以现有 Go 协议网关逐步替换不安全的历史账号/网络边界。
3. 将原 VC6/DirectDraw 客户端迁移到可原生构建的 macOS arm64 / Win11
   客户端；Wine 版本继续作为完整功能基准。
4. 在具备认证、限流、备份和加密传输后开放互联网联机。

项目现状与决策记录见 [`docs/`](docs/)。
