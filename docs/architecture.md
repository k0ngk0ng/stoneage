# 架构与迁移路线

## 当前可玩链路

```text
StoneAge 2.5 Windows 客户端（named LSSPROTO，CP936）
  │  TCP 9065
  ▼
Go stoneage-gateway（SQLite ClientLogin 校验 + 函数名协议 ↔ 数字协议）
  │  TCP 19065，仅本机
  ▼
Linux 2.5 GMSV 进程 ── SAAC RPC ── Linux 2.5 SAAC 进程
                                    └─ 平面文件角色/账号数据

管理员浏览器 ── HTTPS 反向代理 ── stoneage-admin（SQLite 会话/审计）
                                      │ Unix socket：status/固定重启动作/通知
                                      ▼
                               stoneage-operator ── 固定脚本 + 通知队列
SQLite stoneage-auth.db ────────┘
```

Linux 服务器也提供根目录 `docker-compose.yml`：`saac` 和 `gmsv` 是两个独立的
Compose 容器，`gateway`、`admin` 和 `operator` 作为独立 Compose 服务运行。
operator 才挂载 Docker socket，并且只调用 `deploy/linux/compose/` 下的固定脚本；
后台通过共享的 Unix socket 请求状态、通知和服务控制。SQLite 认证库、配置和角色
目录保持持久化。

GMSV 与 SAAC 是独立的旧版服务，分别读取 `setup.cf` 和 `acserv.cf`。当前 Compose
只部署一条游戏线路：一个 GMSV、一个 SAAC 和一个网关。GMSV 在拆分容器时通过
服务名 `saac:9300` 连接 SAAC，用户编辑的 `setup.cf` 不会被改写。未来多节点部署
可以让其他 GMSV 连接同一 SAAC，但不属于当前单线路 Compose 的编排范围。

保存的 `sa_2903.exe` 与现有 Linux GMSV 都属于 2.5 时代，但使用两种生成版
LSSPROTO 方言：客户端用函数名，GMSV 用数字函数号、校验和及会话密钥。Go
网关转换封包而不改变游戏语义，已覆盖当前源码中已知的登录和游戏消息。

客户端补丁只做三件事：注入本机服务器列表、绕过已经消失的商业 WGS 会员
网关，以及恢复 WGS 原本负责设置的“游戏协议阶段”。原始客户端保留不动，
补丁脚本会校验其 SHA-256 和每个修改点的原始字节。

本地回环比 2000 年代公网快得多。客户端在三个界面状态切换中存在竞态，网关
因此仅在 `ClientLogin` 回复前、`CharLogin` 回复后的初始化数据前，以及
`EN` 后进入战斗循环前各保留 100 ms；移动、聊天和普通战斗包不增加人为
延迟。Linux 2.5 的 `BC` 在每个角色后附加五个骑宠字段，旧客户端只消费八个
字段；网关会删除这五个扩展字段，其余战斗命令保持字节透明。

## 编码边界

- UI 文案使用“伺服器”等繁体风格用词，但可执行文件中字节是 CP936/GBK。
- 客户端、资源及旧服务端内部保持字节透明，协议网关不能擅自转 UTF-8。
- 新管理界面和未来数据库统一使用 UTF-8；在明确边界处与 CP936 转换。

## 迁移策略

短期以 Wine + cnc-ddraw 保存完整 2.5 功能并作为黑盒回归基准。长期客户端
迁移以 1.82 源码提供的平台与资源格式知识为参考，但协议和行为必须对照 2.5
二进制，不会把游戏降级到 1.82。

1. 固化网络协议、资源解码和关键流程的自动化测试。
2. 扩展 Go 边界：SQLite 账号、管理员会话、审计和受限部署控制。
3. 建立 SDL3/CMake 客户端壳，逐步替换 DirectDraw、输入、音频和 UI。
4. macOS arm64 与 Win11 x64 共享核心；原 2.5 客户端持续作兼容基准。

## 里程碑

- M0：旧资产完整复制进当前目录，不依赖移动硬盘 — 完成
- M1：现代 Linux 容器构建并运行 2.5 SAAC/GMSV — 完成
- M2：数字协议探针通过登录、建角、地图 1006 — 完成
- M3：真实 2.5 客户端经 Go 网关完成登录、选人、地图和移动 — 完成
- M4：两个真实客户端同时登录、移动、聊天与断线存档 — 完成；宠物、物品和
  完整战斗已通过。登录公告 WN 已通过，真正 NPC 对话按钮仍待稳定闭环
- M5：SQLite 账号边界、管理员后台和可安全部署的朋友联机方案 — 基线完成；
  仍需真实 Linux/VPN 网络演练
- M6：原生 macOS/Win11 客户端逐模块替代 Wine 基线
