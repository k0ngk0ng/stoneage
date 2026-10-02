# 连接与结果处理

## 本地前提

v0.2.3 起，用户在终端直接执行 `sactl login`，无需先 init 或编写配置，默认使用客户端内置的公开游戏网址。密码隐藏输入且只保留在后台内存；Agent 不要求用户在聊天或命令参数里传密码。登录后用 `chars`、`enter <名称|槽位>` 选择角色，选角失败不破坏账号登录。

`login --profile alt` 创建独立会话并选为当前默认；`sessions` 列出状态，`use <名称|default>` 切换。默认选择跨终端共享，Agent 应显式固定用户授权的 `--profile`。登录失败直接重试 login，不要求用户 stop；login 会更新闲置的旧后台进程，已登录的会话会保留。软件更新后仍在线的旧会话不会强行迁移，用户 logout 后再次 login 即使用新版。

需要已安装的 sactl 二进制，配置可选。可通过 Homebrew、Scoop 或下载区安装，安装行为遵循用户已有授权。

配置查找顺序：`--config` → `STONEAGE_SACTL_CONFIG` → 当前目录 `sactl.toml` → `~/.config/sactl/sactl.toml`。不要读取或输出整份配置来排障。查看 `sactl --help` 确认版本支持的参数。

`serve` 持有长连接；普通命令通过 socket 访问它。守护进程未运行且用户已授权登录时，在允许的工作目录中按现有环境启动：

```sh
sactl login --config /path/to/sactl.toml  # 用户在终端交互登录
# 后续每条命令使用相同配置
sactl --config /path/to/sactl.toml --json chars
sactl --config /path/to/sactl.toml --json enter '用户指定的角色'
sactl --config /path/to/sactl.toml --json observe
```

`serve` 默认后台运行，只启动本地进程，不代表已经登录；`login` 才负责交互认证。`serve --foreground` 供排障和外部进程管理器使用，不能无边界等待其退出。尚未登录时请用户在终端执行 login，不索取明文密码。目标角色不明确时询问用户；已指定地址时沿用它，否则使用客户端默认地址。连接覆盖参数仅用于 login / serve。

## JSON 与退出码

返回外层类似：

```json
{"ok":true,"data":{"Phase":"battle","Revision":42},"text":"供人阅读的摘要"}
```

这是外层格式示意，不是完整观察。`data` 的形状取决于命令；某些操作只有 `ok` 和 `text`，用后续 `observe` 核验。

| 结果 | 处理 |
| --- | --- |
| `ok=true` | 读取该命令的数据；核对预期游戏状态变化 |
| `kind=usage` / 退出码 2 | 修正命令或参数，不原样重试 |
| `kind=session` / 退出码 3 | 检查 daemon、配置或连接；未必仅是 daemon 未启动 |
| `kind=action` / 退出码 1 | 根据服务端原因及观察调整动作 |
| `kind=unknown` / 退出码 1 | 可能已提交；先观察核对，不能直接重发 |
| 退出码 4 | CLI 本地错误或 daemon 启动失败；可能没有 JSON，查看错误并核实状态 |
| `kind=server` / 退出码 1 | 报告服务端错误，核实状态后决定是否继续 |

即使没有合法 JSON（进程失败、连接断开），已提交动作也可能发生。不得把错误自动解释为“没有执行”。

`wait 30s` 等待下一条事件，并不指定等待某个业务结果；它也不能替代观察。`status` 只观察当前连接，不触发登录。其他游戏命令在有内存凭据或兼容旧配置时可能重连。`logout` 清除内存凭据，之后不会因查询而重新登录。

`auto-battle off` 停止自动战斗策略，`stop` 结束 sactl daemon，v0.2.8 起 `logout`（等同 `logout --in-place`）原地登出并关闭会话，三者含义不同。回记录点必须显式使用 `logout --record-point`。已发布的 v0.2.7 默认仍回记录点，须使用 `logout --in-place` 原地登出：要求先离开战斗，等待服务端断开确认；两种登出都会清除内存凭据。JSON `data.mode` 区分 `record-point` / `in-place`，`data.confirmed` 表示是否确认；`kind="unknown"` 时不要宣称位置保存成功，应重登核验。`stop` 不能替代这条正式登出命令。不要为了结束一次任务擅自关闭用户原有会话。

新版 CLI 将无参数 logout 明确发送为原地模式，兼容 v0.2.7 后台；更旧、不支持模式的后台会明确拒绝，不会退化成回记录点。被拒绝时不要改发回记录点来绕过。

新版 `status` 文本区分客户端与后台版本；JSON 顶层提供 `client_version` / `daemon_version`（旧后台可能不返回后者）。编号自动刷新不需要模型配置；手动 `query AI` 仅用于扩展状态诊断。 开发版服务端还返回当前出战宠物选择，公共观察使用 `Player.BattlePetSlot` / `BattlePetSlotKnown`（战斗观察位于 `Own`）；未知时不能从技能列表或第一个宠物槽推断。旧服务端仍可通过原生 KS 回执确认选择，查询本身不切换宠物。

## 手动状态查询

优先使用正常 `observe` / `status`；v0.2.8 起编号会自动获取。`query` 是只读 S 状态请求，不是大模型调用。v0.2.9 起命令帮助（无参数 `query`、`query --help`）会离线列出用途、范围及示例；先核对安装版本是否支持。

可用代码：`c` 位置、`i` 全部装备/背包、`k0..k4` 宠物属性、`w0..w4` 宠物技能、`j0..j4` 装备精灵魔法、`n0..n4` 队伍成员、`t` 称号、`AI` 扩展自身状态、`BTIME` 竞技场回合时钟、`BTRULES` 战斗规则摘要与引擎平台（需客户端、服务端均支持）。`t` 当前只能通过返回事件查看，尚未投影到 observe。槽位从 0 开始且类型不同；不要将宠物槽位当作装备槽位。

`BTRULES` 供本地 learned v2 模型核对兼容性，结果投影到战斗 `Clock.RulesDigest` / `Clock.EnginePlatform`；它与 `BTIME` 独立，不改变回合截止时间。没有元数据或与模型不匹配时，指挥程序在排队前拒绝，不把缺失信息当成兼容。摘要相同不代表模型已经通过胜率或完整竞技场认证。

服务端热重载配置或战斗表会清除该摘要，并通知已连接客户端清除缓存；learned 不能继续使用旧摘要。遇到这种情况保留回退/缺失元数据记录，不能手工修改模型摘要绕过校验；由运营人员按发布流程恢复经过核验的服务端资源。

`query` 成功只说明发出请求，即时快照可能尚未更新；用后续结构化观察核验。不要把 wait 任意一次唤醒当成该查询完成。需关联扩展状态时可使用 `AI:<16位小写十六进制请求ID>`，检查响应的 `AI.RequestID`。

开发版战斗中的 `observe` / `battle-state` 立即返回当前已知投影，不为可选编号刷新等待；缺失编号继续明确标记未知。后台仍以只读 `AI` 请求刷新自身状态，战斗中也支持手动 `AI` 及合法的关联请求；其他状态查询原有场景限制不变。世界中的普通观察仍会短暂等待待刷新编号。
