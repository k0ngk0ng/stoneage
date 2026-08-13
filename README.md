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
- 服务端当前使用 SAAC 原生平面文件，不依赖数据库。未来账号层若改用 SQL，
  项目只接受 **MySQL 8.0 + utf8mb4**。

## macOS 一键运行

首次需要 Docker Desktop、Go、Wine 11，以及已经保存在本目录中的
`vendor/`、`runtime/legacy-client/` 资产。随后执行：

```bash
./scripts/start-local.sh
```

测试账号为 `probe` / `local`，人物为 `ProbeHero`。在客户端中依次选择
“本機”和“本機一線”。查看状态或停止：

```bash
./scripts/status-local.sh
./scripts/stop-local.sh
```

局域网和互联网部署见 [`docs/networking.md`](docs/networking.md)。当前本机服务
仅绑定 `127.0.0.1`，不要把历史 SAAC/GMSV 直接暴露到公网。

## 发布产物

执行下面的命令会在 `dist/` 生成 macOS Apple Silicon 应用、Win11 x64
联机包和 Linux amd64 服务端包：

```bash
./scripts/build-release.sh
```

现成包及 SHA-256 位于 [`dist/`](dist/)。macOS 应用仍需要外部 Wine 11 与
Docker Desktop/OrbStack；Win11 客户端本身不需要 Wine。发布说明见
[`docs/release-macos.md`](docs/release-macos.md)、
[`docs/windows.md`](docs/windows.md) 和
[`docs/verified-features.md`](docs/verified-features.md)。

## 当前目标

1. 完成真实 NPC 对话按钮与 Win11 实机回归。
2. 以现有 Go 协议网关逐步替换不安全的历史账号/网络边界。
3. 将原 VC6/DirectDraw 客户端迁移到可原生构建的 macOS arm64 / Win11
   客户端；Wine 版本继续作为完整功能基准。
4. 在具备认证、限流、备份和加密传输后开放互联网联机。

项目现状与决策记录见 [`docs/`](docs/)。
