# 本地天梯战队指挥程序

目标：Mac 上的独立本地程序使用 sactl 为 1v1–5v5 战队登录、组队、匹配和作战。
每队只有一个指挥官，成员仅观察和执行。游戏服仍拥有全部战斗、匹配、资源和积分规则。
本工具不恢复已移除的 Codex AI 服务、管理页面或 Web worker。

## 实现与验收清单

以下为完整交付范围；未勾选项不能视为完成。

- [x] 公共战斗观察、结构化动作候选，未知字段明确标识；Web/sactl 同源。
- [x] 动作绑定比赛、回合、观察与执行权；过期、重复、未知结果不盲目重发。
- [x] 可增量持久采集完整战斗事件，游标重置/缺口可识别。
- [x] 服务端回合剩余时间只读投影，不改变 30 秒回合机制。
- [x] 本地多角色登录、名片邀请、准备、匹配、结算、再次排队、重连。
- [x] 每队唯一指挥官，角色操作串行，整队决策，持久检查点和停止/接管。
- [x] basic：固定顺序普通攻击及宠物默认技能，无配置时默认启用。
- [x] learned：真实训练/推理入口、能力声明、规则版本检查与实测模型。
- [x] llm：可配置 Chat Completions、整场上下文、结构化计划、时限和故障回退。
- [x] hybrid：先本地候选后大模型审核，最终计划形成前不提交任何候选动作。
- [x] 本地插件扩展协议、取消与超时、模型/策略版本固定到单场。
- [x] 同源训练观察、全队联合动作、原始过程与权威终局持久保存。
- [x] 离线真实引擎 1v1–5v5 训练环境，批量训练、独立评测、场间显式切换模型。
- [x] 1v1–5v5 真实隔离网络端到端及重连测试。
- [x] 普通战斗、天梯、Web/sactl、自动战斗回归；不修改游戏机制。

## 数据和评估边界

已有 v0.1.84 的 10,000 条加点样本不是训练好的出招模型。初版数据的裸人物、
同等级、无宠物条件必须保留，不能直接当作完整天梯策略。训练按规则版本及场景区分，
同一组合的重复和换边留在同一数据划分。终局截断和技术中断不伪造胜负。

现场输入只包含各队员有权观察的本方/公开信息；敌方隐藏属性与未来结果不进入输入。
策略接口只返回候选动作选择，游戏凭据和执行连接由运行器持有。
持续采集和后台训练可以并行，每场比赛固定模型版本，新版本通过留出评测后再切换。

## 使用方式

从 v0.1.97 起提供。需要 Python 3.11+、v0.1.97 或更新的 `sactl` 和提供 `BTIME` 只读观察的游戏服。
当前 Mac 已编译 `build/local-arena/bin/sactl-macos`，示例配置指向这个开发版。
大模型配置只在本地指挥程序中；游戏服、Web 管理端和 sactl 配置不加入模型调用入口。
旧游戏服不提供这些接口，程序会在排队前检查兼容性。

### 在当前 Mac 仓库启动

本地指挥程序从仓库的 `bin/arena-agent` 启动，不随 Homebrew 的 sactl 安装。
在仓库根目录执行；Python 版本须至少为 3.11：

```sh
python3 --version
mkdir -p runtime/local-arena
cp tools/arena_agent/example.json runtime/local-arena/team.json
cp config/sactl/sactl.toml.example runtime/local-arena/captain.toml
chmod 700 runtime/local-arena
chmod 600 runtime/local-arena/captain.toml
```

编辑 `runtime/local-arena/captain.toml`，填入自己的账号和已创建的角色。
连接线上游戏可使用以下配置（替换账号、角色；密码文件通过本地编辑器创建）：

```toml
socket_path = "build/local-arena/captain.sock"
transport = "http"
web_base_url = "https://sa.ichenj.com"
account = "你的账号"
character = "你的角色名"
password_file = "runtime/local-arena/captain.password"
```

密码文件只保存密码，权限设为 `600`。TOML 的相对路径按启动工作目录解析，
所以这些命令始终在仓库根目录运行；JSON 的相对路径则按 JSON 所在目录解析。
`runtime/` 和 `build/` 已被 Git 忽略，不要把真实账号配置放进受版本管理的 `config/`。

```sh
chmod 600 runtime/local-arena/captain.password
bin/arena-agent check --config runtime/local-arena/team.json
bin/arena-agent run --config runtime/local-arena/team.json --matches 10
# 持续匹配：
bin/arena-agent run --config runtime/local-arena/team.json --forever
```

默认使用当前 Mac 已编译的 `build/local-arena/bin/sactl-macos`。
其他 Mac 可先安装/升级已发布的 sactl，并把 JSON 的 `sactl` 改为 `"sactl"`：

```sh
brew update
brew install k0ngk0ng/tap/sactl  # 已安装时使用 brew upgrade sactl
sactl version
```

2v2–5v5 时，把 `mode` 改成对应人数，并为每位成员配置不同的 ID、TOML 和 socket。
第一位成员为队长，每队仍只有这个指挥程序统一决策。

| 名称 | 代号 | 行为 |
| --- | --- | --- |
| 顺序攻击 | `basic` | 默认；按敌方席位顺序普通攻击，宠物优先普通攻击，否则防御/等待 |
| 经验指挥 | `learned` | 加载本地训练产物，对整队动作组合评分，用有限宽度搜索形成联合计划 |
| 推演指挥 | `llm` | 将全队观察、历史过程和可选动作交给 Chat Completions，严格校验返回计划 |
| 联合参谋 | `hybrid` | 先生成本地模型建议，再交大模型定案；大模型失败时尝试采用本地建议，最终回退 `basic` |

复制 `tools/arena_agent/example.json` 到自己的配置位置。相对路径以该 JSON 的目录为基准。
`mode` 为 1–5，`members` 数量必须完全一致；每个成员指定独立的 sactl TOML 与 socket，
其中 TOML 的 `socket_path` 必须与 JSON 中相同，且明确填写 `account`、`character`。
账号密码仍按 sactl 的 `password_file` 方式配置，不写进策略配置或训练记录。
已有名片会直接使用；否则通过普通移动、核对角色 ID 和交换名片建立联系人。
成员应处于可会合的世界地图，跨地图会合依赖 sactl 的地图数据和正常传送路径。

```sh
bin/arena-agent check --config runtime/local-arena/team.json
bin/arena-agent run --config runtime/local-arena/team.json --matches 10
# 持续匹配：
bin/arena-agent run --config runtime/local-arena/team.json --forever
```

`check` 校验策略和配置结构，不会登录账号。首次 Ctrl-C 请求完成当前比赛后停止；
再次 Ctrl-C 立即取消并关闭本进程启动的会话。立即中断的比赛仍由原有天梯断线规则处理。
接管时先停止旧指挥官，再以相同状态目录启动；模型版本不同的未结束比赛会被拒绝接管。
不要删除 SQLite 检查点来强行重发未知结果。
同一 socket 和同一服务地址/账号/角色都有本机文件锁；身份锁位于项目 `build/local-arena/ownership/`。
已有 sactl 会话退出指挥程序后保留，程序自行启动的会话会关闭。

`learned` / `hybrid` 另设 `"model": "相对配置目录的模型.json"`。
没有真实模型文件、未训练该人数模式，或服务端规则版本不兼容时，不会伪装成已安装的学习策略。
运行中动作类型超出训练覆盖范围会回退；模型文件在启动时载入，整场使用固定内容哈希。
训练可以在另一个进程并行进行；评测新文件后，完成比赛并重启指挥程序来切换版本。

`llm` 示例：

```json
{
  "endpoint": "http://127.0.0.1:8000/v1/chat/completions",
  "model": "your-model-name",
  "api_key_env": "STONEAGE_ARENA_MODEL_KEY",
  "timeout_seconds": 12,
  "context_bytes": 180000,
  "response_format": "json_object"
}
```

完整 endpoint 指向 Chat Completions；远端要求 HTTPS，本机可用 HTTP。
`response_format` 支持 `json_schema`、`json_object`、`none`，最终输出都经过相同结构校验。
不要求服务商支持工具调用。模型请求由可取消的独立进程执行，密钥通过 stdin 传入，
不放在进程参数或数据库中。历史超过配置预算时保留最新过程及明确标识的早期摘要；
完整原始过程仍保留在 SQLite。当前观察本身超过预算会回退，不截掉队员数据后假装完整。

## 数据、训练与评测

每支队伍的 `state_dir/arena.sqlite3` 使用 WAL 和事务保存：

- `events`：各队员实际收到的战斗包、同源结构化效果及增量序号。
- `records`：整队回合观察、联合计划、策略/模型版本、提交回执与可识别的采集缺口。
- `battle_intents`：人物/宠物每场每回合的提交意图，写入前落盘；未知结果不重发。
- `results`：服务端终局、双方阵容、积分变化及战斗统计；结算前先收齐本地最终事件再 ACK。
- `pending_ladder` / `match_policies`：天梯幂等请求恢复和单场策略版本固定。

人物、宠物、道具、精灵及换宠候选来自客户端实际可观察条件；它们不是服务端成功保证。
动态 HP 以回合参战名单为准，己方属性来自各队员自己的观察；敌方隐藏攻击、防御等不填猜测值。
`battle-state`、`battle-act`、`battle-events` 与 Web 的同名接口复用 Go 公共层。
Web 原有按钮仍通过原有交互入口执行；已有界面规则没有全部迁移为候选接口，不能宣称全项目完全对等。
特殊战斗包保留 `raw` 与未知标识；无法从协议确定的技能名/效果不补造。

```sh
bin/arena-agent train \
  --database build/local-arena/team-a/arena.sqlite3 \
  --database build/local-arena/team-b/arena.sqlite3 \
  --output build/local-arena/models/policy-v1.json

bin/arena-agent evaluate \
  --database build/local-arena/independent-team/arena.sqlite3 \
  --model build/local-arena/models/policy-v1.json
```

初版训练器是无第三方依赖的**整队线性胜负价值基线**，并非已训练成熟的强化学习模型。
训练与推理共用 `learning.features`，包含公开血量、己方属性、动作/目标和集中攻击等组合特征。
仅使用正常战斗终局、无缺口且动作有写入回执的记录；写入回执不等于服务器保证技能生效。
技术中断不伪造胜负。相同阵容的重复比赛和双方视角留在同一数据划分，按比赛权重训练，
避免长对局淹没短对局。每个模式只有一组阵容时没有独立留出集，会明确返回 `null`。
`evaluate` 排除训练阵容组，并报告价值预测损失；它不是胜率证明。

训练产物包含规则/特征版本、模式和动作覆盖、参数、数据划分及评测结果，默认标记 `experimental`。
输出文件只能新建，不能覆盖正在使用的旧模型。更强的模型可以通过插件替换；
要宣称改善胜率，需要更多阵容、加点、宠物和装备覆盖，以及原生引擎中的独立对照评测。
旧裸人物 1v1 加点数据不能直接作为本接口的出招训练集；未携带当前规则版本的旧采集也会被排除。
服务端改变战斗规则时须同步更新 `BTIME` 的规则标识，并重新训练/验证对应模型。

## Mac 上隔离采集真实对局

`simulate` 在容器内启动临时账号服务、原生游戏服、网关和两支 sactl 队伍。
所有对局仍通过正常登录、组队和天梯完成，采用实际 C 战斗引擎，支持 1v1–5v5。
容器使用 `--network none`，不连接生产；数据写入指定的新 `build/` 目录。
它是完整协议环境，适合验证和采集真实观察，吞吐量低于原有裸人物 1v1 离线生成器。

```sh
bin/arena-agent simulate --work build/local-arena/collect-2v2-001 \
  --mode 2 --matches 100 --strategy explore --seed 7

bin/arena-agent train \
  --database build/local-arena/collect-2v2-001/commander-0/arena.sqlite3 \
  --database build/local-arena/collect-2v2-001/commander-1/arena.sqlite3 \
  --output build/local-arena/models/2v2-v1.json

bin/arena-agent simulate --work build/local-arena/eval-2v2-001 \
  --mode 2 --matches 100 --strategy learned \
  --model build/local-arena/models/2v2-v1.json
```

`explore` 仅用于隔离采集，随机探索联合动作，对手是 `basic`；不是第五种上线预设策略。
`--seed` 固定探索策略的随机源，不宣称控制原生服务端全部随机性。
生成目录的 `commander-passed.json` 含实际对局数和胜负统计。
`--reconnect` 会在已提交过动作后强制中断一个测试 sactl 进程，检查恢复。

前提是本机已有兼容镜像（默认 `gcc:13-bookworm`，可通过 `--image` 指定），以及当前源码编译的 Linux 工具：

```text
build/local-arena/bin/sactl
build/local-arena/bin/stoneage-gateway
build/local-arena/bin/seed-network
build/local-arena/native/gmsv/gmsvjt.exe
build/local-arena/native/saac/saacjt.exe
```

本次开发环境已准备并验证这些文件。`simulate` 不拉取镜像、不构建镜像、不覆盖旧采集目录；
缺工具时明确报错。其他机器需要先按 `server/legacy/modern/build.sh` 的原生现代化编译流程准备
GMSV/SAAC，`seed-network` 来自 `server/legacy/modern/tests/seed-ladder-network.go`；
Go 工具需编译为容器架构。不能直接把 Mac 可执行文件挂进 Linux 容器。

## 策略插件协议

在独立 JSON 配置中增加 `plugins`，并将 `strategy` 设为插件 ID，例如：

```json
{
  "id": "my_policy",
  "version": "v1",
  "modes": [1, 2, 3, 4, 5],
  "command": ["/absolute/path/to/my-policy", "--stdio"]
}
```

每次启动一个进程，stdin 一行 JSON 包含 `schema_version=1`、`operation=decide`、`team`、`history`、`budget_ms`。
stdout 返回一行计划 JSON：`schema_version`、`match_id`、`turn`、`observation_id`、`orders`；
每个 order 严格为 `member_id`、`actor`（`player`/`pet`）、`candidate_id`。
必须覆盖全部尚未提交且未保留意图的行动槽。不能输出原始协议命令、执行登录或自行控制队员。
非法、过期、超时输出由指挥官拒绝；进程会被终止，输出大小有上限。
插件是用户安装的本地可信代码，不是操作系统安全沙箱；环境过滤不代表禁止它读取本机文件。

## 本次验证证据

- Go 公共观察、CLI 和 Web 回归及 race 检查通过；Web 战斗目标、面板、结算、天梯及自动战斗测试通过。
- Python 覆盖整队计划校验、持久化未知提交、锁、训练/推理、HTTP Chat Completions、混合回退及插件超时。
- 真实原生环境完成 1v1、2v2、3v3、4v4、5v5 各连续两场；正常积分结算与战后资源恢复通过。
- 强制中断 sactl 后恢复比赛；重复查询原生时钟未延长截止时间。
- 实验模型完成真实 1v1 和 5v5 对局。小样本中尚未显示优于 `basic`，不作为高胜率模型发布。
- 未连接外部真实大模型供应商；HTTP 协议、结构化输出和时限通过本机兼容服务测试。

当前烟测产物为 `build/local-arena/models/joint-experimental-v3.json`，来自 6 场有效原生对局，
训练覆盖模式为 1v1 和 5v5。2v2–4v4 的运行闭环已验证，但使用该模型文件会因缺少对应训练覆盖而拒绝；
应先按上述命令采集对应人数的数据再训练。该文件只用于验证训练与推理接通，不默认启用或作为正式强策略发布。
