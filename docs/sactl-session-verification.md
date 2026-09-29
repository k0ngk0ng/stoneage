# v0.2.3 会话验证记录

## 故障与修复

隔离的真实 GMSV / SAAC / gateway 经 Web HTTP Handler 登录后，旧客户端在收到包含 `ladder` 的事件时退出，终止错误为 `websession: invalid private API response`。Web 已支持该字段，CLI 的严格 JSON 结构没有同步。

事件信封现由 `internal/sessionwire` 定义，Web 与 CLI 共用；CLI 使用事件序号确认与有界读取重试。游戏写操作不自动重试，避免重复提交。

客户端补齐与 Web 同周期的 20 秒 Echo 保活，包含选角阶段。隔离服务在只修复事件字段后也能空闲 85 秒，因此该环境未证明缺少心跳是此次断线的直接原因。

交互登录不再自动使用旧角色配置；登录失败可直接重试。登录前检查已有会话，保护已登录账号，自动替换闲置旧版本进程。普通连接不要求配置文件，命名会话支持默认选择及显式覆盖。

## 已执行验证

- `go test -mod=readonly ./...`。
- 公共游戏协议、HTTP 会话、CLI、Web Handler 的竞态检查。
- Web 登录凭据、线路选择、事件确认与重试、竞技场前端测试。
- `TestNativeSactlIdleLifecycle`：真实原生服务、两个独立账号、Web HTTP Handler、实际 sactl 后台及一次性命令。错误密码重试、无效角色后继续、选角空闲 85 秒、世界空闲 85 秒、角色/战斗日志/竞技场观察、logout 后 status 不重连、再次 login 均通过。
- macOS 实际进程：无配置启动独立 profile、sessions/use 路由、显式 profile 覆盖、v0.2.2 闲置后台自动更新、失败后再次交互登录无需 stop。
- 安装脚本、配置保留与下载校验测试；配套 skill 校验。

原生测试为显式启用：在隔离 fixture 环境设置 `STONEAGE_TEST_NATIVE_GATEWAY`、`STONEAGE_TEST_NATIVE_WORK`（含合成账号文件）、`STONEAGE_TEST_SACTL_BIN`，执行 `go test ./client/web -run '^TestNativeSactlIdleLifecycle$' -v -timeout 5m`。使用仓库原有模拟器的合成账号与角色，不使用生产账号。

## 验证边界

本轮没有用玩家的生产密码登录，也未重新验收全部游戏玩法或浏览器视觉交互。HTTP 链路和前端回归通过不等于所有 Web / CLI 功能已完全对等。

在线地图下载尚未实现；高级寻路仍依赖可选的本地 2.5 数据。CLI 自动任务、自动练级的高层入口仍存在已记录的能力差距。生产 Web 服务重启会关闭其持有的连接，发布后需要重新登录。

## v0.2.7 登出与两端入口核查

- Web 的回记录点与原地登出分别对应 `sactl logout --record-point`（默认）和 `sactl logout --in-place`。原地登出使用 EOF 而非 `CharLogout`，HTTP 与 Web 共用 `DELETE ...?wait=1` 的确认路径。
- 真实 GMSV / SAAC / 网关上的两个 CLI 会话已验证两种登出、立即重登、原地坐标保持；协议测试确认原地登出不发送回记录点包，并等待对端关闭。
- 旧后台曾忽略 logout 参数。新 CLI 对带参数的 logout 使用独立 RPC，旧后台只能拒绝，不会误执行回记录点；对旧后台的兼容测试覆盖两种模式、拼错参数和冲突参数。
- Web 背包和宠物界面保持原样；CLI 的物品模板编号与宠物稳定编号保留。战斗面板可按观察需要展示相关字段。

本次按已存在的入口核对了以下范围；“已有入口”不等于该类操作全部完成端到端验收。

| 功能 | Web | sactl | 状态 |
| --- | --- | --- | --- |
| 登录、选角、两种登出 | 登录/系统菜单 | login / chars / enter / logout | 本次登出流程已验收 |
| 角色、背包、宠物和战斗观察 | 对应面板 | observe / battle-state / battle-log | 已有结构化入口，编号展示按终端用途区分 |
| 物品、宠物、交易、邮件、组队、骑乘 | 对应菜单 | item / pet / trade / mail / party / ride | 已有入口，仍需逐操作核对行为与结果 |
| 竞技场 | 竞技场面板 | arena | 已有入口，完整验收边界见 arena-player-flow.md |
| 自动战斗 | 自动战斗 | auto-battle | 已有入口，不代表所有配置完全一致 |
| 自动任务、自动练级 | automation.js 的 quest / leveling | 缺少对应高层命令 | 待补齐，不得用原始 send 或本地 AI 指挥冒充 |
| 在线地图获取与寻路 | 在线资源加载 | goto / warp 等仍依赖本地数据 | 在线数据获取待补齐 |

两端功能新增或变更须同时检查入口、参数、权限、结构化结果及旧版本兼容性；不得将协议存在或单端测试通过作为完整对等的依据。
