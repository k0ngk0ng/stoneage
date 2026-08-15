# macOS 开发与运行

## 已验证环境

- macOS 26.5.2，Apple Silicon
- Docker Desktop 29.4
- Go 1.26.2
- Wine 11（WoW64）
- cnc-ddraw 7.1.0.0（仓库内固定版本）

所有路径都以仓库根目录为基准。日常构建和运行读取当前目录下的
`server/legacy/source/`、`vendor/` 和 `runtime/`，不依赖外接硬盘。

## 一键运行

```bash
./scripts/start-local.sh
```

它会按需准备 2.5 服务端运行目录、启动独立的 SAAC/GMSV 容器、构建并启动 Go
协议网关、重新
生成校验过的本机客户端，然后用 CP936 Wine prefix 启动游戏。测试账号：

```text
账号：probe
密码：local
人物：ProbeHero
```

客户端中选择“本機”→“本機一線”。服务器/线路列表默认由本地启动脚本生成；如果
需要多个服务器或线路，可复制 [`config/client-servers.toml.example`](../config/client-servers.toml.example)
为 `runtime/client-servers.toml`。TOML 中的 `gateways` 统一保存网关地址和端口，
每条线路只引用一个 `gateway` ID；启动脚本会在启动客户端前读取该 UTF-8 TOML，
把网关和线路列表写入旧客户端的 CP936 数据结构：

```bash
cp config/client-servers.toml.example runtime/client-servers.toml
./scripts/start-local.sh
```

也可以用 `STONEAGE_CLIENT_SERVERS_FILE=/path/to/servers.toml` 指定其他文件，或用
`STONEAGE_CLIENT_SERVERS_URL=https://example.com/servers.toml` 在启动时从 HTTPS
接口获取。URL 和本地文件可以同时设置，接口失败时回退到本地文件。
旧客户端本身不具备读取外部配置的能力，因此启动器会把配置嵌入一个临时生成的
客户端副本；原始 `sa_2903.exe` 不会被修改。地址目前限定为 IPv4，名称会按
CP936 编码，最多 8 个服务器组、32 条线路，实际还受旧客户端代码空间限制。

状态和停止命令：

```bash
./scripts/status-local.sh
./scripts/stop-local.sh
```

日志位于 `runtime/logs/gateway.log`、`runtime/legacy-server/logs/` 和
`runtime/logs/wine-client.log`。

本地开发和生产 Compose 示例都只启动一条线路。客户端服务器列表由启动器配置
（TOML/HTTP）注入；它不负责在同一台机器上编排额外的 GMSV。需要多个入口时，
应等待后续的多节点部署方案，不要向当前单线路脚本追加路由。

## 从归档重建

只有 `server/legacy/source/` 丢失时才需要重新导入：

```bash
./scripts/import-legacy.sh
./scripts/build-legacy-server.sh
./scripts/prepare-legacy-server.sh
```

归档已复制到 `vendor/archives/`。服务端源码本身已纳入 Git，构建在 Alpine 容器中完成；Apple
Silicon 默认产出 Linux arm64 ELF，也可设
`STONEAGE_SERVER_PLATFORM=linux/amd64`。

## 独立测试

```bash
go test -mod=mod ./...
go build -mod=mod -o build/stoneage-gateway ./cmd/stoneage-gateway
go build -mod=mod -o build/stoneage-probe ./cmd/stoneage-probe
```

数字 GMSV 端到端探针（服务器已启动时）：

```bash
./build/stoneage-probe \
  -address 127.0.0.1:19065 \
  -account probe -password local \
  -character ProbeHero -enter
```

真实客户端诊断工具：

```bash
./scripts/drive-legacy-client-wine.sh state
./scripts/drive-legacy-client-wine.sh peek ADDRESS [SIZE]
```

终端不得直接把客户端窗口标题的 CP936 字节当 UTF-8 打印；驱动因此输出
`title_cp936_hex`。游戏画面中的中文才是编码是否正确的判断依据。

## 编码不可变条件

Wine prefix 必须在 `zh_CN.GBK` 下创建，并保持 ACP 936。CP950 会把实际
GBK 字节解错。不要复用曾在英文 locale 下创建的 prefix；脚本会检查注册表
ACP，并安装 CJK 字体回退。

## 单独组件

服务端和网关：

```bash
STONEAGE_UPSTREAM_PORT=19065 ./scripts/run-legacy-server.sh
./build/stoneage-gateway \
  -listen 127.0.0.1:9065 \
  -upstream 127.0.0.1:19065 -trace
```

客户端补丁与启动（网关地址已在 TOML 中，不再放在命令行）：

```bash
./scripts/patch-legacy-client.py \
  --servers-file runtime/client-servers.toml --bypass-wgs
./scripts/run-legacy-client-wine.sh
```

服务端的移动包 NU 流控由 `runtime/legacy-server/gmsv/setup.cf` 中的
`enable_nu_flow_control` 控制：`0`（默认）关闭，`1` 开启。修改后需重启
GMSV 才会生效。

管理后台的“服务”页展示“游戏网关”和“游戏服务”两个部署服务；游戏服务内部
仍可分别链接到 GMSV 的 `setup.cf` 与 SAAC 的 `acserv.cf` 配置页面。保存配置不会
自动重启，需手动重启对应进程。在线通知位于独立的“通知”页，
通过受限 operator 将 UTF-8 文本转换为 CP936 后写入固定通知队列，再由 GMSV
主循环广播给在线玩家。

## 本地认证与管理后台

开发脚本默认不加 `-auth-required`，方便复现旧 SAAC 的协议问题。需要测试发布
账号层时，先创建临时 SQLite 库和管理员：

```bash
go run -mod=mod ./cmd/stoneage-admin create-admin \
  -db runtime/dev-auth.db -username admin -password-stdin
go run -mod=mod ./cmd/stoneage-admin serve \
  -db runtime/dev-auth.db \
  -listen 127.0.0.1:8080 \
  -config runtime/legacy-server/gmsv/setup.cf \
  -saac-config runtime/legacy-server/saac/acserv.cf
```

在后台创建游戏账号后，以相同数据库启动网关：

```bash
go run -mod=mod ./cmd/stoneage-gateway \
  -listen 127.0.0.1:9065 -upstream 127.0.0.1:19065 \
  -auth-required -auth-db runtime/dev-auth.db
```

生产部署不要把 `-auth-required` 关闭，也不要把管理后台直接绑定到公网地址。

## 构建发布包

```bash
./scripts/build-release.sh
```

脚本会先跑全部 Go 测试，分别交叉编译 macOS arm64、Win11 amd64 和 Linux
amd64 网关；服务端会在 Docker 中构建一次 Linux amd64 发布版，再恢复当前
Apple Silicon 开发目录所需的 Linux arm64 二进制。最终输出和总清单位于
`dist/`。打包完成后不会启动任何 StoneAge 服务。
