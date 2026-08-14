# macOS 开发与运行

## 已验证环境

- macOS 26.5.2，Apple Silicon
- Docker Desktop 29.4
- Go 1.26.2
- Wine 11（WoW64）
- cnc-ddraw 7.1.0.0（仓库内固定版本）

所有路径都以仓库根目录为基准。日常构建和运行只读取当前目录下的
`vendor/`、`runtime/`，不依赖 `/Volumes/JElements`。

## 一键运行

```bash
./scripts/start-local.sh
```

它会按需准备 2.5 服务端运行目录、启动容器、构建并启动 Go 协议网关、重新
生成校验过的本机客户端，然后用 CP936 Wine prefix 启动游戏。测试账号：

```text
账号：probe
密码：local
人物：ProbeHero
```

客户端中选择“本機”→“本機一線”。状态和停止命令：

```bash
./scripts/status-local.sh
./scripts/stop-local.sh
```

日志位于 `runtime/logs/gateway.log`、`runtime/legacy-server/logs/` 和
`runtime/logs/wine-client.log`。

## 从归档重建

只有 `vendor/upstream/` 丢失时才需要重新导入：

```bash
./scripts/import-legacy.sh
./scripts/build-legacy-server.sh
./scripts/prepare-legacy-server.sh
```

归档已复制到 `vendor/archives/`。服务端构建在 Alpine 容器中完成；Apple
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

客户端补丁与启动：

```bash
./scripts/patch-legacy-client.py \
  --host 127.0.0.1 --port 9065 --bypass-wgs
./scripts/run-legacy-client-wine.sh
```

服务端的移动包 NU 流控由 `runtime/legacy-server/gmsv/setup.cf` 中的
`enable_nu_flow_control` 控制：`0`（默认）关闭，`1` 开启。修改后需重启
GMSV 才会生效。

管理后台的“GMSV 配置”页只编辑这份 `setup.cf`；SAAC 的 `acserv.cf` 仍由部署
文件维护。在线通知通过受限 operator 写入固定通知队列，再由 GMSV 主循环广播
给在线玩家。

## 本地认证与管理后台

开发脚本默认不加 `-auth-required`，方便复现旧 SAAC 的协议问题。需要测试发布
账号层时，先创建临时 SQLite 库和管理员：

```bash
go run -mod=mod ./cmd/stoneage-admin create-admin \
  -db runtime/dev-auth.db -username admin -password-stdin
go run -mod=mod ./cmd/stoneage-admin serve \
  -db runtime/dev-auth.db \
  -listen 127.0.0.1:8080 \
  -config runtime/legacy-server/gmsv/setup.cf
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
