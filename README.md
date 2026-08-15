# StoneAge Revival

让经典《石器时代》重新成为一套可维护、可联网、可在现代 macOS 与 Windows 上运行的游戏。

当前可玩基线已经固定为 **StoneAge 2.5**：保存下来的 32 位 Windows
客户端经兼容补丁后，在 Apple Silicon macOS 的 Wine 11 中可连接容器内的
Linux 2.5 服务端。旧档案只作为只读输入；日常运行不再依赖移动硬盘。

## 已验证状态（2026-08-13）

- macOS 26.5.2 / Apple Silicon / Wine 11：登录、人物、地图、移动、聊天、
  多人、宠物、物品及完整战斗均已通过实机验证。
- 两个独立 Wine prefix 的真实 2.5 客户端已同时进入同一 GMSV；第二人物移动后
  坐标从 `(15,22)` 保存为 `(16,25)`，并发连接与断线存档通过。
- Linux 2.5 GMSV 的战斗 `BC` 每角色 13 字段已由 Go 网关转换为旧客户端的
  8 字段；攻击、回合结算和返回地图均已验证。
- 登录公告 `WN` 窗口与确认包已验证；真正 NPC 对话按钮的完整闭环仍列为待
  回归项，不宣称完成。
- Win11：客户端仍是原生 x86 PE，配套 cnc-ddraw；需在 Win11 实机完成最终
  回归，步骤见 [`docs/windows.md`](docs/windows.md)。
- 界面用词虽偏繁体，二进制文本实际是 **CP936/GBK**，不是 CP950。Wine
  prefix 必须使用 ACP 936。
- 发布服务端使用独立 SQLite 认证库（Argon2id 密码哈希、管理员会话、审计日志），
  SAAC 仍保存人物、邮件和家族平面文件。SQLite 不替代角色目录，也不需要 MySQL。

## macOS 一键运行

首次需要 Docker Desktop、Go、Wine 11，以及已经保存在本目录中的
`vendor/`、`runtime/legacy-client/` 资产。随后执行：

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

Linux 也可以把旧版游戏服务、Go 网关、管理后台和受限 operator 一起交给
Compose 编排。复制 `.env.compose.example` 为 `.env` 并修改管理员密码；本地测试先
构建两个镜像，然后执行（这是单机单线路部署）：

```bash
cp .env.compose.example .env
chmod 600 .env
docker compose config --quiet
./scripts/build-local-images.sh
docker compose up -d
docker compose ps
docker compose logs -f saac gmsv gateway
```

Compose 默认只把游戏网关 9065 和后台 8080 绑定到 `127.0.0.1`；需要局域网或
 VPN 访问时，显式设置 `STONEAGE_GATEWAY_BIND`/`STONEAGE_ADMIN_BIND` 并配合防火墙。
如果玩家从公网连接，只将网关端口通过防火墙/VPN 暴露；后台应继续绑定回环地址，或
放在带 HTTPS 和访问控制的反向代理后，并把 `STONEAGE_ADMIN_COOKIE_SECURE` 设为
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

首次启动后访问 `http://127.0.0.1:8080/`。如果没有在 `.env` 设置管理员账号密码，
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
