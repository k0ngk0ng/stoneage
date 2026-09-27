# 天梯玩家流程（v0.1.95 起）

天梯已在 v0.1.95 发布并上线，Web 与 sactl 均须使用支持该协议的版本。真实认证网络下的 sactl 1v1—5v5、
多人邀请及再次匹配、1v1 同场重连和 GMSV 崩溃恢复已通过专项联调；真实 Web–sactl 1v1
战斗、同场重连与再次匹配也已通过。Web 2v2—5v5 邀请至再次匹配，以及 5v5 中一名玩家
同场重连、退场地图恢复已通过；sactl 5v5 多人、整队和全场十人同时掉线并交错登录已通过。
同一角色 Web→sactl→Web 的 1v1/5v5 续战，以及 5v5 中 Web 玩家和一名 CLI 队友同时
离线后交错恢复已通过。
新增邀请身份参数、旧名片重建及槽位复用已通过真实网络专项检查。
完整验收及证据范围见 [实现跟踪](ladder-implementation.md)。Web 与 sactl 均可独立
完成整个过程，多人队伍的每位玩家分别操作自己的账号。

## sactl 操作

当前规则不限制重复对手；与相同玩家再次匹配仍按正常 Elo 规则计分，没有重复对手次数
上限、收益衰减或专用匹配冷却。

以下命令假定 daemon 已配置并登录角色。`--json` 的外层 `ok` 表示命令结果，天梯权威
回执和事件批次在 `data` 中；不要从 `text` 的中文或英文说明推断状态。

| 阶段 | 命令 | 观察/结果 |
| --- | --- | --- |
| 当前状态 | `sactl --json ladder status` | `data.snapshot`，含角色、五模式积分、队伍、邀请、比赛和待确认结算 |
| 创建队伍 | `sactl --json ladder create 3` | 创建 3v3 队伍；模式取 1–5 |
| 更改模式 | `sactl --json ladder mode 2` | 队长操作，重新准备 |
| 查看天梯名片 | `sactl --json ladder contacts` | `data.contacts` 同时给出 `slot`、`id`、`name`、`online`；取选中行的槽位和角色 ID |
| 发出邀请 | `sactl --json ladder invite 0 <character_id>` | 同时验证槽位和选中角色的身份；不是发送邮件 |
| 处理邀请 | `sactl --json ladder accept <invitation_id>` / `decline <invitation_id>` | ID 来自 `snapshot.invitations` |
| 成员管理 | `sactl --json ladder kick <player_id>` / `leader <player_id>` / `leave` | ID 来自 `snapshot.room.members`；移人/转让限队长 |
| 登记宠物 | `sactl --json ladder loadout 3` | 五个宠物槽位的位掩码：3 表示槽位 0 和 1；登记骑宠和备用宠物 |
| 准备 | `sactl --json ladder ready` / `unready` | 每位成员独立操作；人数和状态由服务器核查 |
| 开始/取消匹配 | `sactl --json ladder queue` / `cancel` | 队长操作；队伍人数必须恰好等于模式人数 |
| 等待事件 | `sactl --json ladder wait 0` | 初次订阅，读取当前快照及 `data.stream`、`data.cursor` |
| 继续等待 | `sactl --json ladder wait <cursor> 30s --stream <stream>` | 使用上次返回的两个值；匹配后获取双方名单和开战时间 |
| 策略列表 | `sactl --json ladder strategies` | 当前客户端安装的策略元数据，内置 `basic` 和 `manual` |
| 策略选择 | `sactl --json ladder strategy basic` / `manual` | 可在比赛中切换；选择按角色持久化，已接受的动作不撤回 |
| 战斗 | 原有 `observe`、`battle`、`battle-log`、`auto-battle` 命令 | 初次默认初级策略，准备时启动天梯专用循环；选择 `manual` 后可自行出招 |
| 读取结算 | `sactl --json ladder result` | 当前待确认结算；胜负、逐人统计和积分前后值 |
| 历史结算 | `sactl --json ladder result <match_id>` | 仅能查询自己参与的比赛；不会替换当前比赛状态 |
| 确认结算 | `sactl --json ladder ack` | 保留队伍、清除准备；之后逐人准备，由队长再次排队 |

人物和宠物的实际出战/骑乘状态沿用现有宠物操作，`loadout` 只登记允许参战的槽位。
结算的 `statistics.damage` 为人物及所属宠物的总伤害，`pet_damage` 是其中宠物造成的部分；
`damage_taken` 同样包含本人和宠物承伤，`pet_damage_taken`、`ride_damage_taken` 为其中
的宠物、骑宠部分，不能再次相加。Web 对应展示“总伤害”“总承伤”和各分项。
普通名片交换仍用 `social trade-card 1`、面对对方后 `mail add`。邀请前用 `ladder contacts`
读取服务端当前名片；不可只保存数字槽位。底层邀请参数为 `<slot>:<character_id>`，未知
结果重试须保留这个完整参数。槽位被替换返回 `contact_slot_changed`，刷新后重新选择；
原角色删除重建返回 `contact_identity_changed`，旧名片没有身份字段则返回
`contact_identity_required`，后两者须与对方重新交换名片。Web 使用同一查询及校验。

排队后不能改参战配置。天梯结束不用普通 `battle end`/EO 确认，使用 `ladder ack`。
发件人或收件人被天梯占用时，宠物邮件暂缓投递和回送；解除占用后继续，等待期间保留附件。
多人队伍须先由同队全部成员确认结算，再逐人准备、由队长排队；不必等待对方队伍确认。
倒计时内服务器会确认全员赛前角色存档已保存；保存失败、存档超过旧协议容量或超时则取消
开战，双方回到未准备的队伍，不扣积分或增加弃赛冷却。比赛内正常消耗的道具与装备损坏
在结束后恢复，掉线重连期间仍保留本场已经消耗的状态。
策略与参战配置分开：比赛中可切换策略，不能借此解锁装备或宠物登记。策略说明与本地
中高级扩展契约见 [天梯战斗策略](ladder-strategies.md)。

## 重连、事件与未知结果

重新登录同一角色后先 `ladder status`。比赛仍在重连期限内时，服务器应接回原席位和
当前回合；已结束则读取结算。预算为每人每场累计 60 秒，战斗不暂停；已接受动作保留，
回合超时人物防御、宠物待机。真实 TCP 网关和 sactl 的 1v1 重连已验证原比赛、席位、
回合及已提交动作恢复，旧事件 stream 返回缺口和完整快照。Web 1v1 另验证入场动画结束后
已提交动作仍锁定、下一回合解锁及原比赛结算；Web 5v5 中一名玩家断线也已验证这些行为，
并确认结算后地图恢复可用。另已验证 sactl 5v5 的双方各两人、整支队伍和全场十人同时
断线后反向逐个登录：原席位保留，同回合保留已提交动作，跨回合开放新指令；重复掉线
继续扣除各自累计额度，未掉线玩家不扣额度，恢复后完成同一场权威结算和资源恢复。
真实浏览器另验证同一角色 Web→sactl→Web 的 1v1 续战：先关闭原连接再登录另一端，
保留原比赛、席位、回合和已提交动作，下一回合两项指令重新开放，累计重连额度不重置，
最后完成原比赛并恢复地图。5v5 另验证 Web 玩家和一名 CLI 队友同时离线，由在线对手
确认两人离线；交错恢复并完成 Web→sactl→Web 后，十人取得一致结算并返回保留队伍。
此混合场景使用一个真实浏览器及九个独立 CLI，不等同于十个浏览器同时恢复的专项检查。
初级策略还通过了独立网络恢复检查：重新认证后不再设置策略，自动回答下一回合的人物和
宠物指令并完成原场结算。该场景用手动对手控制回合，确认新动作来自重连后的策略循环。

服务重启与玩家掉线分开处理：重启后不续接原战斗。已持久化终局的比赛继续完成原结算；
尚未持久化终局的比赛补发 `reason=server_restart`、不计分的技术中断结算。后一种结果带
`statistics_incomplete=true`，其回合、时长和逐人统计为未知，不能把 JSON 中的占位零值
当成实测数据。重启不保留临时队伍，确认结算后重新组队；弃赛冷却仍然保留。协调器数据库
恢复已通过子进程退出测试，赛前档案另通过真实 SAAC 保存、强制退出及同端口重启读取检查。
另已验证简单 1v1 的 GMSV 战斗中崩溃、重启及网关重新登录：人物 HP/MP/金币及宠物
状态恢复、积分不变、技术中断结算可确认。另已通过真实 5v5 的复杂资源检查：十名角色
消耗道具、损坏武器后强制退出 GMSV，重新登录恢复赛前物品、耐久和两只宠物及其持有物品，
十人取得一致的不计分结算；再次存档与赛前档案一致。真实 Web 也验证了重启后重新认证、
技术中断通知、移动端结果面板和确认后重新建队。具体证据见实现跟踪；这不代表覆盖所有
技能效果，也不代表游戏服与账号服同时崩溃时仍可续战。

`wait` 返回 `gap=true` 时，丢弃旧的本地天梯投影，以 `data.snapshot` 完整替换，再使用
返回的新 `stream` 和 `cursor` 等待。不能把缺失的事件当成“什么也没发生”。`timed_out=true`
表示本次等待时间内没有新的可返回事件；取消与断线是错误，并尽可能保留最后快照。
当前默认 CLI 总超时 60 秒；如等待超过 30 秒，按需要显式设置全局 `--timeout`。

操作返回 `data.code=outcome_unknown` 时，保留 `data.request` 中的全部内容，例如：

```sh
sactl --json ladder queue --request-id <原request_id> --revision <原revision>
```

重试必须携带相同操作、参数、ID 和版本。不要换 ID 盲目再次提交；服务器若返回
`stale_revision` 或 `request_conflict`，读取当前状态并处理这个明确结果。活动比赛及终局
已经持久化；服务端保存每个角色最近 32 条修改请求的持久回执，以及已发布版本的持久水位。
相同请求跨重启重试可返回原结果，回执淘汰后由旧版本拒绝再次执行。`server_boot` 表示当前
服务启动，`receipt_boot` 表示原操作所属启动；历史成功不代表重启前的临时队伍仍然存在，
应以当前 `snapshot` 为准。核心层已验证重启、时钟回拨、回执淘汰及数据库失败回滚；
提交前后强制退出的核心测试已验证策略和回执的原子持久化，原生回归通过；
sactl 已验证游戏服与客户端同时重启后的成功/拒绝回执恢复、历史建队不重建房间、请求冲突，
以及回执淘汰后的旧版本拒绝。Web 收到历史回执时显示“重启前的操作结果”，两端都不会因此
解除当前自动战斗的停用状态。Web 已用真实浏览器验证建队成功后丢失 HTTP 回包、游戏服
重启、页面重载和重新认证后的原请求恢复与历史回执重试；旧房间不会重建。真实拒绝回包丢失后重启也已验证原请求恢复和历史拒绝重放；
另由同角色 sactl 推进回执窗口后返回浏览器，验证旧请求得到 stale_revision、不重复执行。

## Web 与 sactl 核对

Web 右上角“天梯”面板已有与上表对应的组队、邀请、宠物登记、准备、匹配和结算入口；
战斗及战斗记录沿用现有界面。关闭面板只是隐藏面板；“确认结算，返回队伍”才发送 ACK。
不确定的操作在同一标签页的会话存储中保存，重新读取同一角色后提供“重试原请求”。

Web 战斗页面已接入共享的同场比赛及已提交动作投影；重连后已接受的人物/宠物指令不会
再次开放选择。天梯不走普通战斗退出确认；最后一回合动作播放结束后打开权威结算，
提前轮询到结果也不能跳过动画确认。冷资源、重放、回合切换、快速战斗和本地全灭的页面
状态机已通过专项检查；真实浏览器连接原生服务器的 1v1—5v5 已验证战斗、末回合动画后
展示结果，以及保留队伍再次排队。1v1 与 5v5 单人重新登录后可以完成原比赛并恢复地图。
倒计时和结果分别只主动展示一次；关闭倒计时不抑制结果，主动隐藏结果后重复包也不会强行
再弹出。邀请对象在名片刷新时按槽位与角色 ID 保留，槽位换人则清空选择。

两端的解析、状态投影、完整请求回执匹配和事件恢复均使用共享 Go 层。Web 还必须经过
控制权 generation 校验；自动任务或练级占用角色时应先接管。已有寻敌循环会在天梯准备、
匹配和结算期间停止走动；原地自动战斗可继续回答回合。

尚未达到完整对等验收：默认初级策略和扩展接口已有专项测试，完整配置冻结、
完整背包隔离审计、全技能统计仍有待办。Web 已实际验证 5v5 游戏服崩溃后重新登录
显示不计分和统计未恢复、十人结果一致、确认后重新建队。CLI 的复杂资源 GMSV
崩溃恢复已通过真实 5v5 专项，见下方入口及实现跟踪中的证据。
界面夹具、单元测试、原生战斗 smoke 和 sactl 网络联调分别验证各自边界，不能替代
完整 Web/sactl 对等验收。

## 隔离网络回归

`server/legacy/modern/tests/test-ladder-network.py` 启动专用 SAAC、GMSV、认证网关和十个
sactl daemon，创建新账号，测试 1v1、原请求重试、已提交动作后的同场重连、战斗中
GMSV 强制退出后的恢复，以及通过实际移动/名片交换完成的 2v2—5v5 邀请、准备、积分
结算和两队独立再排队。另覆盖保存并重新登录后同名角色删除重建、旧名片拒邀和重新交换，
以及名片槽位复用时拒绝原选择。`--scenario basic` 运行双人场景（含初级策略重连后自动
回答新回合），`reconnect` 单独运行该自动恢复场景，`multiplayer` 运行多人和名片场景，
`contacts` 单独运行实际名片交换与身份变化场景，`receipts` 单独运行服务与客户端重启后的
持久回执恢复、冲突及淘汰检查，`multi-reconnect` 单独运行十人 5v5 的多人、整队、
全员同时掉线及交错重连、累计额度和原场结算检查，
`contact-restart` 验证 SAAC/GMSV 一起冷重启后的原名片身份保留及明确删角通知，
`cross-map` 经正常传送将双人置于不同地图，验证普通决斗不会选择远端角色，而天梯
可以完整对战并在结束后保留双方原地图/坐标，
默认 `all` 包含以上场景。它只接受仓库 `build/` 内尚不存在的测试目录，结束时清理其
启动的进程；日志与 JSON 保留。它不读取玩家配置，也不访问生产。

先按项目原生构建流程生成 GMSV/SAAC，再为同一 Linux 架构构建：

```sh
go build -mod=mod -o build/ladder/network-bin/ ./cmd/sactl ./cmd/stoneage-gateway
go build -mod=mod -o build/ladder/network-bin/seed-network server/legacy/modern/tests/seed-ladder-network.go
```

交叉编译时显式设置目标 `GOOS=linux`、`GOARCH` 和 `CGO_ENABLED=0`，缓存及临时目录
均放在 `build/`。在隔离 Linux 环境中执行，例如使用现有缓存工具链容器和
`--pull=never --network=none --read-only`，将仓库只读挂载到 `/repo`，仅将 `build/`
另行挂载为可写：

```sh
python3 /repo/server/legacy/modern/tests/test-ladder-network.py \
  --work /repo/build/ladder/network-run-01 \
  --sactl /repo/build/ladder/network-bin/sactl \
  --gateway /repo/build/ladder/network-bin/stoneage-gateway \
  --seed /repo/build/ladder/network-bin/seed-network \
  --gmsv /repo/build/ladder/native/gmsv/gmsvjt.exe \
  --saac /repo/build/ladder/native/saac/saacjt.exe
```

此入口不构建或下载镜像，也不代表 Web、多人重连、全部资源类型已经验收。

复杂资源的 GMSV 崩溃专项入口为 `tests/test-ladder-resource-crash.py`，使用同样五个
程序参数和新的 `--work` 目录，另外提供 `--resource-archive <checkpoint.data>`，输入为
原生 smoke 生成的资源检查点。它仅在测试服务停止时准备独立账号资源，随后通过正常
名片邀请和 sactl 战斗消耗两份食物、损坏武器，强制结束游戏服和客户端后验证十人资源、
宠物持有物品、不计分结算以及退出后再次保存的存档。SAAC 在故障阶段保持运行。

### 真实浏览器回归

`serve-ladder-web-fixture.py` 复用上述原生服务夹具。额外构建同一 Linux 架构的
`./client/web` 到 `build/ladder/network-bin/web-native`，传入原有五项二进制参数及
`--web /repo/build/ladder/network-bin/web-native`，并使用新的 `--work` 目录。
默认 `--scenario basic` 创建两名测试角色；驱动使用 `--scenario cross-client` 可在此双人夹具
执行 Web→sactl→Web 的同角色续战检查，临时 CLI 仅连接角色 0 的专用配置并在切回前停止。
十人夹具另可用驱动的 `--scenario cross-client-5` 检查混合离线及 5v5 跨端续战。
夹具的 `--scenario multiplayer` 创建十名角色并通过
正常移动和交换名片建立两队名片列表。准备结束后停止角色 0 的 CLI，留给浏览器登录，
其他角色保持独立 sactl 会话。`web-ready.json` 记录就绪信息，不包含密码内容。

浏览器需访问 Web 端口，因此夹具容器使用专用 Docker 网络而非 `--network=none`；只以
`-p 127.0.0.1::18080` 将 Web 发布至宿主回环，使用 `docker port <夹具容器> 18080/tcp`
取得实际地址。保留 `--pull=never --read-only`、只读仓库与可写 `build/` 挂载，不操作现有
游戏容器。fixture 的 `--lifetime` 限制为至多 7200 秒，SIGTERM 或期限到达会清理其子进程。

宿主安装的 agent-browser 使用独立命名会话；在 `build/` 内准备包装脚本，设置
`AGENT_BROWSER_SESSION`、`AGENT_BROWSER_SOCKET_DIR`、`AGENT_BROWSER_PROFILE`、
`AGENT_BROWSER_DOWNLOAD_PATH`、`AGENT_BROWSER_SCREENSHOT_DIR`、`TMPDIR`，
所有目录均指向仓库 `build/`，并设置 `AGENT_BROWSER_RESTORE_SAVE=never` 后执行
`agent-browser "$@"`。不要复用默认浏览器会话。随后运行：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 server/legacy/modern/tests/test-ladder-web.py \
  --work build/ladder/web-run-01 \
  --browser build/ladder/ui/browser \
  --url http://127.0.0.1:<实际发布端口> \
  --container <专用夹具容器名> \
  --sactl /repo/build/ladder/network-bin/sactl \
  --name acceptance-01
```

`--work` 须与夹具目录对应，`--name` 须为新的产物子目录。多人检查须在夹具与驱动两端
均指定 `--scenario multiplayer`。驱动通过真实 UI 登录、操作天梯面板，手动回合通过
页面原生发送入口提交人物防御与宠物待机；对手通过 sactl 操作。成功写入 `passed.json`，
失败保留 `failure-state.json`，结果采样和截图一同保留。结束后关闭专用浏览器会话、
停止该夹具容器，再移除其专用 Docker 网络；这些清理均不触及其他会话或容器。

附加浏览器专项：`--scenario server-crash` 使用十人夹具，验证 5v5 执行回合后游戏服
崩溃和重新登录的技术中断提示；`configuration-drafts` 验证模式及空宠物选择在真实刷新后
保留并提交；`receipts-refused`、`receipts-evicted` 分别验证丢失拒绝回包后的服务重启恢复
及跨客户端推进回执窗口后的原请求拒绝。各次使用新的 `--name` 产物目录，保留原请求、
回执、状态和截图。后两项会重启隔离 GMSV，淘汰场景还短暂切到同角色的独立 sactl。
