# Windows 11 开发与运行

当前 `sa_2903-local.exe` 是原生 32 位 x86 Windows 程序，Win11 无需 Wine。
cnc-ddraw 负责旧 DirectDraw/调色板兼容。`dist/StoneAge-Revival-Win11-x64.zip`
已经包含 x64 网关、客户端、PowerShell 启停器和服务器地址配置器。macOS
链路已实机通过，Win11 仍需在真实机器完成最终回归。

## 需要复制的内容

复制整个仓库，尤其是 Git 忽略但运行必需的目录：

```text
runtime/legacy-client/
runtime/legacy-server/        （可重建）
vendor/archives/
vendor/upstream/              （可从 archives 重建）
```

不需要移动硬盘，也没有硬编码 macOS 绝对路径。

## 推荐结构

最稳妥的第一版是在 Win11 上安装 Docker Desktop（WSL2 backend）和 Go：

1. Docker 运行 Linux 2.5 SAAC/GMSV，宿主只映射 `127.0.0.1:19065`。
2. Windows x64 Go 构建 `stoneage-gateway.exe`，监听 `127.0.0.1:9065`。
3. `runtime/legacy-client/sa_2903-local.exe` 与 `ddraw.dll`、`ddraw.ini`、
   `data/`、`map/` 保持同目录，直接启动。

PowerShell 构建网关：

```powershell
go test ./...
go build -o build\stoneage-gateway.exe .\cmd\stoneage-gateway
.\build\stoneage-gateway.exe -listen 127.0.0.1:9065 -upstream 127.0.0.1:19065
```

## 直接使用发布包

解压后，本地 GMSV 已在 `127.0.0.1:19065` 时执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -LocalGateway
```

连接朋友的 LAN/VPN IPv4 时先生成对应客户端（需 Python 3）：

```powershell
powershell -ExecutionPolicy Bypass -File .\Configure-Server.ps1 -IPv4 192.168.1.50
powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1
```

服务器地址写在客户端二进制中，因此不能只给 `Start-StoneAge.ps1` 传一个新
地址。停止客户端与包内网关使用 `Stop-StoneAge.ps1`。

服务端脚本目前是 POSIX shell，推荐从 WSL 执行：

```bash
STONEAGE_UPSTREAM_PORT=19065 ./scripts/run-legacy-server.sh
```

客户端本机补丁可在 macOS 生成后直接复制；或在 Windows 的 Python 3 中运行：

```powershell
python .\scripts\patch-legacy-client.py --host 127.0.0.1 --port 9065 --bypass-wgs
```

## Win11 回归清单

- 窗口模式启动，无 256 色/DirectDraw 报错
- 登录页中文可读（程序资源是 CP936）
- `probe/local` 登录并显示 `ProbeHero`
- 进入地图 1006，不在人物登入后退出
- 鼠标移动、键盘输入、声音和窗口缩放
- 两台机器同时登录、互相可见、移动和聊天
- 宠物、物品、战斗遭遇、攻击和结算

不要开启 Windows 的“使用 Unicode UTF-8 提供全球语言支持”测试选项作为修复
手段；这个客户端依赖 CP936。若非中文系统出现字体问题，可在“非 Unicode
程序的语言”选择简体中文，或由后续启动器显式提供 CP936 环境。
