# 本地竞技场 AI 指挥官

从 **v0.2.1** 起，竞技场 AI 指挥功能统一为 **`sactl ai`** 子命令，不再单独安装 `arena-agent`。
在石器百科的 **下载区** 下载安装包，或升级包管理器中的 `sactl` 即可使用。
在线登录、组队、匹配、作战、数据记录及旧版 SQLite 线性训练不需要 Python、Go 开发环境或 Docker。
v0.2.13神经网络训练与实际对战评估还需要兼容的 C 战斗引擎；Mac 当前通过 Docker 提供 Linux 引擎环境，Go 训练与推理由 `sactl` 执行。完整隔离游戏服模拟也使用 Docker。

从零训练的新模型使用 v8 特征，在已有承伤者与保护者历史上加入忠犬攻击，并要求 v8 原生环境。现有 v6/v7 模型仍按原输入规则运行；恢复训练或继承父模型不会自动改版，导出也不重标旧权重。场景、特征和动作分别校验，具体边界见 [特征契约](learned-feature-contract.md)。

每队只有一个指挥官，统一制定人物与宠物的联合计划；成员只观察和执行。
游戏服拥有全部战斗、匹配、资源和积分规则。本工具不恢复旧 Codex 服务、管理入口或 Web worker。

v0.2.13 `ai train --plan-scope member` 另外提供离线独立成员模型，用于公平比较统一指挥的作用；此模型仅用于训练/评估，本地指挥入口拒绝加载。默认 `team` 行为保持不变，具体信息共享和对照限制见 [训练说明](learned-training.md#独立成员的离线训练对照)。

v0.2.13 `ai collect-feedback` / `ai train-feedback` 可先采集本地模型实际遇到的局面，再学习单独保存的规则老师建议，支持恢复和历史数据聚合。它们只操作冻结实验的训练场景，原始比赛动作不会被建议覆盖；并非新的在线策略或胜率保证。完整命令与当前规模限制见 [规则反馈训练](learned-training.md#规则反馈采集后再训练)。

## 直接使用已登录的角色（v0.2.14）

日常在线对战无需初始化队伍目录或编辑配置。先用普通客户端交互登录，密码只留在后台进程内存：

```sh
sactl login --profile bot
sactl --profile bot chars
sactl --profile bot enter '角色名'
sactl ai check --profile bot --strategy learned --model /absolute/path/model.json
sactl ai run --profile bot --strategy learned --model /absolute/path/model.json --matches 1
```

`--model` 是用户本地的外部模型文件，安装包不带模型。启动时按游戏服实际规则摘要和引擎平台检查兼容性；不能把旧 ARM 引擎训练模型当作生产 amd64 模型，也不能靠修改 JSON 标签通过检查。`check` 校验模型及配置，不登录或排队；`run` 才读取现有会话、核对身份与服务端规则，然后自动建队、准备、匹配和出招。

省略 `--profile` 使用当前选中的会话；多人队伍重复传入 `--profile first --profile second`，每队只有一个指挥官。省略 `--mode` 按 profile 数量推断；显式模式必须与成员数一致。默认策略为 basic，learned/hybrid 必须指定模型。可用 `--pet-mask 1` 登记第一个宠物槽，人物/宠物实际出战状态仍由正常游戏操作设置。

同一组 profile 默认复用 `~/.local/state/sactl/ai/<队伍摘要>/arena.sqlite3`（尊重 XDG_STATE_HOME），可以通过 `--state-dir` 指定。启动会输出实际数据目录。旧目录绑定原服务器、账号和角色，换角色时须使用另一 profile 或新的数据目录，不能混用未完成的指令状态。显式 `--state-dir` 不应由两个队伍共用。

这些会话由普通 `sactl login/logout` 管理；AI 退出保留已有登录，不写账号密码文件，不自动替换或启动后台进程。后台已退出时先重新登录。首次 Ctrl-C 在本场结束后停止，第二次立即退出。`--matches 1` 完成一场即停止，`--forever` 持续匹配。

LLM/hybrid 的本地配置也可全部用参数提供：

```sh
sactl ai run --profile bot --strategy llm \
  --llm-endpoint https://provider.example/v1/chat/completions \
  --llm-model your-model --llm-api-key-env STONEAGE_ARENA_MODEL_KEY --matches 1
# hybrid：将策略改为 hybrid，并加 --model /absolute/path/local-model.json。
```

密钥由指定环境变量读取，不作为命令参数。其他参数有 `--llm-timeout`（秒）、`--llm-context-bytes`、`--llm-response-format`。使用 `ai run --help` 查看。`--config` 仍支持既有队伍文件，并可用策略、模型、数据目录和 LLM 参数覆盖；不能与 `--profile` 混用。

## 配置文件方式（可选）

Mac 也可以通过 Homebrew 安装（已有用户执行 `brew upgrade sactl`）：

```sh
brew update
brew install k0ngk0ng/tap/sactl
sactl version
sactl ai version
```

安装包解压后，macOS/Linux 可直接执行 `./sactl ai`，Windows 使用 `./sactl.exe ai`。
一键安装脚本、Homebrew、Scoop、deb/rpm 都只需安装一个 `sactl` 程序。
完整说明与 JSON 示例也随包提供。

先创建队伍配置，目录必须是新的，避免覆盖已有账号和数据：

```sh
sactl ai init --directory arena-team --mode 1
```

编辑生成的文件：

- `arena-team/member-1.toml`：填写 `account`、`character`；默认通过 `https://sa.ichenj.com` 连接游戏。
- `arena-team/member-1.password`：只填写该账号密码，不放进命令参数。
- `arena-team/team.json`：默认策略为 `basic`。多人数队伍由 `--mode 2` 至 `--mode 5` 创建，每位成员使用独立账号/角色、TOML 和 socket。

初始化生成绝对路径，因此可以在其他工作目录启动；移动整个目录后应更新配置路径。
不要同时用浏览器登录受指挥官控制的角色。角色须已创建并能正常进入世界。

```sh
sactl ai check --config arena-team/team.json
sactl ai run --config arena-team/team.json --matches 10
# 持续匹配：
sactl ai run --config arena-team/team.json --forever
```

`check` 只校验配置和策略，不登录或验证密码。真正运行时会检查成员身份和服务端 `BTIME` 兼容性，
旧服不兼容则在排队前退出。现有名片会直接复用；否则按正常移动、核对角色 ID、交换名片、邀请流程组队。
跨地图会合需要在各成员 TOML 配置 sactl 的地图数据目录，或先让队员进入同一张可行走地图。

首次 Ctrl-C 请求完成当前比赛后停止，再次 Ctrl-C 立即退出。立即退出仍适用正常竞技场断线规则。
指挥官整体取消（包括立即停止或隔离测试总超时）会终止当前决策，不将取消记为策略失败，也不再生成 basic 回退计划。单次策略计算超时且指挥官仍在运行时，只有确认本回合尚无提交或预留指令，才可生成 basic 回退计划。
退出时只关闭本程序启动的 sactl 会话，已存在的用户会话保留。
接管前应停止旧指挥官；同一 socket 和同一服务/账号/角色都有文件锁，未完成比赛固定策略版本。
不要删除 SQLite 检查点来强行重发未知结果。

v0.2.13战斗观察立即返回当前已知状态，不再为可选的物品/宠物编号等待刷新。只读 `AI` 自身状态查询在后台继续；缺失字段仍为未知，策略按既有数据完整性检查决定是否回退。世界观察保留原有短暂等待行为。隔离模拟器每个指挥官首次决策会额外查询 `BTIME` 三次以检查截止时间不被观察操作改变，该校验也计入 `turn_timing` 的决策阶段，不能当作纯模型推理延迟。

默认身份锁位于用户缓存目录的 `stoneage-arena/ownership`；可设置 `ownership_dir`，但同一用户所有队伍
必须使用同一个目录，才能防止同一身份被不同指挥官接管。跨进程名片会合也共用这里的锁。

## 从旧命令迁移

旧 `team.json`、成员 TOML、模型和 SQLite 数据不需要转换。把启动命令由 `arena-agent` 或旧版 `sactl arena` 改为 `sactl ai` 即可。
从 v0.2.1 起，`sactl arena` 专门用于普通玩家的竞技场操作；旧 `arena run/train/init` 等命令会报迁移提示，不会执行匹配操作。
旧 `sactl ladder` 保留为普通玩家 `sactl arena` 的兼容别名。底层 `LADDER` 协议、历史 JSON 字段、数据库表和数据目录保留原名称以兼容已有数据。
先结束旧进程的当前比赛，再启动新命令。保留原 `state_dir`，不要删除检查点。
`init` 默认将当前可执行文件写入配置；旧配置若写了其他 `sactl` 绝对路径，会继续尊重该路径。
如需使用刚升级的客户端，可删除 JSON 的 `sactl` 字段或将其改成 `"sactl"`，此时使用当前可执行文件。
旧手工安装目录若留有 `arena-agent`，不再使用它；包管理器升级会按其清单更新程序。

## 策略

AI 指挥、采集、训练、模型和 Chat Completions 配置仅在本地 `sactl ai` 提供，不增加 Web AI 入口。v0.2.13神经文件还支持 schema 5，用于保留实战模型参与配点搜索的间接来源；即使作战模型从零训练，后续评估也保留未知源配置重叠限制。见 [实战来源的间接影响](learned-build-search.md#实战来源的间接影响)。

| 名称 | 代号 | 行为 |
| --- | --- | --- |
| 顺序攻击 | `basic` | 默认；按敌方席位顺序攻击，宠物优先普通攻击，否则防御/等待 |
| 经验指挥 | `learned` | 本地模型统一指挥；v1 线性评分配合束搜索，v2 神经网络结合战斗历史依次生成全队指令 |
| 推演指挥 | `llm` | 将全队观察、历史和候选动作交给兼容 Chat Completions 的大模型 |
| 联合参谋 | `hybrid` | 本地模型先建议，大模型再定案；失败时尝试本地建议，尚无部分提交时可最终回退 basic |

在 `team.json` 修改 `strategy`。`learned` / `hybrid` 还需 `model` 文件路径；v0.2.13也支持 schema_version=2 的 `champion_directory`，与 model 二选一，见[逐场选模](learned-champion.md#队伍逐场选模)。
模型未覆盖该人数模式、规则版本不匹配时拒绝启动；现场动作超出训练覆盖时，按下述提交状态限制决定是否回退。
每场固定模型内容哈希；训练可在另一进程进行，评估后等当前比赛结束并重启以切换模型。

`llm` / `hybrid` 使用 JSON 的 `llm` 配置，例如：

```json
{
  "endpoint": "http://127.0.0.1:8000/v1/chat/completions",
  "model": "your-model-name",
  "api_key_env": "STONEAGE_ARENA_MODEL_KEY",
  "timeout_seconds": 12,
  "context_bytes": 8388608,
  "response_format": "json_object"
}
```

密钥从指定环境变量读取，不写进战斗数据库。远端要求 HTTPS，本机可用 HTTP。
`response_format` 支持 `json_schema`、`json_object`、`none`；所有返回计划仍须通过同样的严格校验。
HTTP 请求由 Go 的 context 限时取消，禁止重定向转发密钥。`context_bytes` 默认 8388608（8 MiB），范围 16000–67108864，计算完整 JSON 请求（含系统提示、schema 和转义）的字节数，不是 token 数；服务商的 token 限额需另外考虑。传入完整可见历史，不自动摘要或删除早期回合；超限明确记录 `context_limit`，按提交状态执行回退规则。
返回必须只有一个正常结束的选择；拒绝重复 JSON 键、拒答、截断、多选及不合法候选。响应正文上限 1 MiB，错误不保存密钥或原始服务商错误正文。

`llm` 和 `hybrid` 均先检查已保存的本回合最终计划；观察、策略与完整版本一致才续执行，不再次请求大模型。已有提交或未知预留时，版本/观察不符会拒绝重算。hybrid 保存不可变 `local_proposal`、`proposal_id`、`final_plan_id` 和服务商结果；服务商失败时可用本地建议，父请求取消则停止，不继续派发。

## 数据、训练和评估

默认数据位置是队伍目录的 `data/arena.sqlite3`，即配置里的 `state_dir/arena.sqlite3`。

v0.2.13在 `records` 中增加 `kind=turn_timing`：每次进入决策的尝试保存观察采集、历史读库、策略计算、计划落盘和并发提交五段 `durations_ms`，以及 `total_ms`、实际策略、计划摘要、复用标记和结果。计时使用本机单调时钟，从本次 battle 处理开始，到提交阶段返回或失败为止；不含外层排队/轮询等待、服务端结算及计时记录自身落盘。`dispatch_complete` 仅表示提交函数返回，是否写入仍须检查 `submission` 与意图状态；`canceled`、`late_decision`、`error` 不能混入正常完成样本。尚未进入决策的观察失败沿用 `observation_failure`；进程被强制终止、存储故障或旧版本可能缺少计时，不能补成零。计时不进入模型历史或 PPO 特征。
使用 WAL 和事务保存事件、整队观察、联合计划、策略/模型版本、提交意图及回执、终局和积分。
网络断线、事件游标缺口和未知写入结果都有明确标识；未知结果不重复提交。

神经策略恢复时优先完成当前回合已经保存的最终计划。旧观察的计划被明确拒绝并重新决策后，下一回合依据提交事实恢复所用的完整观察；部分提交后的记录不会覆盖原始完整计划。来源冲突或无法确定时拒绝该次推理，不猜测一份历史继续推理。

策略失败后的 basic 回退要求确认本回合尚无已提交或预留指令。已有写入、结果未知的预留，或观察无法核实提交状态时，记录 `strategy_rejected`，不生成新的剩余计划；`reason` 分别为 `partial_submission` 或 `invalid_submission_state`，`kind` 保留原策略失败类别。正常新回合回退仍记录 `strategy_fallback`。非可重试的计划续执行错误会结束指挥进程，保留原 `state_dir`；单次策略截止时间仍按既有可重试规则处理，不能把本次拒绝当作已完成派发。人物死亡且服务端已自动完成其人物指令的标记，本身不等于本地提交；存活宠物仍可正常规划。不要清空历史、删除预留或改策略版本来强迫重算。

新版预留事务同时追加 `submission_intent`，实际客户端回复单独保存为 `submission`。前者表示可能已发送但结果尚未知，后者也不等于技能已经生效，仍须结合后续战斗事件。整队观察中的 `history_record_cutoff` 与成员事件游标分别限制本地执行记录和公共战况的读取范围；后到记录不改写过去的决策上下文。旧记录保留原样，不自动补造截点或提交事实。
终局先采齐本地最终事件再确认结算。持久化失败时停止后续变更操作。

```sh
sactl ai train \
  --database arena-team/data/arena.sqlite3 \
  --database another-team/data/arena.sqlite3 \
  --output models/policy-v1.json

sactl ai evaluate \
  --database independent-team/data/arena.sqlite3 \
  --model models/policy-v1.json
```

保留早期 Python 原型的 SQLite 表和 `joint-observed-v1` 模型字段，已有有效数据可继续读取。
Go 和 Python 的随机序列及模型序列化可能不同，不承诺相同种子得到字节相同的模型或跨实现接管未结束比赛。
模型文件只能新建，不能覆盖正在使用的模型。

上述 `--database` 入口是**实验性的整队线性胜负价值基线**，并非成熟的强化学习模型，也没有稳定提高胜率的证据。
训练和推理共用特征，只使用己方/公开信息，不使用敌方隐藏属性和未来结果。
仅纳入正常战斗终局、无缺口且联合计划有写入回执的数据；回执不等于服务端保证技能生效。
同一阵容的重复比赛和双方视角归入同一划分，避免训练/评估泄漏。
`evaluate` 排除训练阵容组，报告价值预测指标，不将它当作胜率。

旧的 10,000 条裸人物同点数 1v1 加点样本不是出招策略模型，不能直接用于本训练接口。
开发烟测模型只覆盖 1v1 和 5v5，不能当作 2v2–4v4 模型；也不随安装包作为强策略发布。

工作区新增的 v2 原生环境训练使用 `--environment` / `--data-dir`，支持规则模仿热身、PPO、恢复和对战评估，见 [v0.2.13训练入口](learned-training.md)。`learned` / `hybrid` 支持旧线性模型及 commander-policy-v2 神经架构；神经模型文件 schema 2/3/4 分别保留原生、实战示范及两者接续的来源。神经策略要求服务端通过 `BTRULES` 返回匹配的规则摘要及平台。所有策略采集均请求这项元数据，非神经策略允许旧服务器缺失，但记录不能据此补造训练来源。带实战来源的评估保留 unverified_source_artifacts，不冒称已经排除未知训练配置重叠。候选模型可显式选用，但不代表已证明更强。已有受控 1v1、2v2、5v5 的执行验证及独立 5v5 离场、42 回合重放证据，覆盖范围见 [实施记录](learned-implementation.md)；完整规则一致性与策略强度验收仍待完成，工具随 v0.2.13 发布到下载区。

离线规则对照另可显式选择 `independent-control`（如 `evaluate --opponent independent-control`）：各成员获得相同完整公共观察，仅协调自己与宠物，不共享本回合队友的治疗和控制预留。它与集中式 `control` 在 1v1 相同，多人时可能重复选择目标；仍可看到并治疗队友。它是规则对手/教师，不是第五种线上策略，也不等于独立训练模型。默认教师与对手不变，已冻结验证不能事后追加它；比较需另行预定，规则数据不进入 PPO 在策略更新。
所有对局通过正常竞技场完成，使用真实 C 战斗引擎；容器内同样不需要 Python。
`--network none` 隔离生产，既不拉取也不构建镜像，不覆盖已有采集目录。

## 隔离批量采集

此高级入口需要仓库及预先编译的原生工具，在已有 Docker 镜像里启动临时账号服、原生游戏服、网关和两队客户端。

```sh
sactl ai simulate --root . --work build/local-arena/collect-2v2-001 \
  --mode 2 --matches 100 --strategy explore --seed 7

sactl ai train \
  --database build/local-arena/collect-2v2-001/commander-0/arena.sqlite3 \
  --database build/local-arena/collect-2v2-001/commander-1/arena.sqlite3 \
  --output build/local-arena/models/2v2-v1.json

sactl ai simulate --root . --work build/local-arena/eval-2v2-001 \
  --mode 2 --matches 100 --strategy learned \
  --model build/local-arena/models/2v2-v1.json
```

对手默认是 `basic`。v0.2.13可用 `--opponent-strategy basic|learned|explore` 指定另一队的独立指挥官；选择 `learned` 时必须同时给出 `--opponent-model`，其他策略不能带该模型参数。例如比较两个模型的完整登录、匹配和执行链路：

```sh
sactl ai simulate --root . --work build/local-arena/duel-2v2-001 \
  --mode 2 --matches 2 --strategy learned --model build/models/candidate.json \
  --opponent-strategy learned --opponent-model build/models/baseline.json
```

两个模型分别固定到各自指挥官，均须符合实际规则、平台和人数；模型路径限仓库 `build/` 内且不含符号链接。双方原始记录分别保存在 `commander-0/`、`commander-1/`，通过证明分别核对两边的实际模型决策。容器内旧工作程序不认识新参数时必须报错，不能静默使用 basic；此入口启动隔离客户端，不向已有玩家后台发送新操作。`--seed` 用于任一方的 explore 决策，不保证整个原生比赛跨运行复现。少量完整链路比赛只验证执行，不替代预先冻结的强度评估。

默认复用已安装的 `gcc:13-bookworm`（可用 `--image` 指定兼容镜像）。需准备以下 Linux 可执行文件，架构须匹配容器：

```text
build/local-arena/bin/sactl
build/local-arena/bin/stoneage-gateway
build/local-arena/bin/seed-network
build/local-arena/native/gmsv/gmsvjt.exe
build/local-arena/native/saac/saacjt.exe
```

原生工具编译流程见 `server/legacy/modern/build.sh`；`seed-network` 来自
`server/legacy/modern/tests/seed-ladder-network.go`，其余 Go 命令分别在 `cmd/`。
当前开发 Mac 已有这些工具；其他机器需要先准备。不要将 Mac 可执行文件放进 Linux 容器。
`explore` 只用于隔离采集，随机探索联合动作，对手为 basic；不是线上预设策略。
`--reconnect` 检查实际进程断线恢复；`commander-passed.json` 保存对局统计和验证证据。`simulate --timeout 10m` 设置整次隔离测试的时限，包含登录、排队、所有比赛和断线恢复；默认取 10 分钟与每场 2 分钟总和的较大值。它不改变游戏回合截止时间或模型的提交时限。超时保留现场记录，但不会生成通过证明；长局可在新的 work 目录用明确时限重新测试。容器中的 sactl 也必须支持该参数，旧工作程序会拒绝启动，不静默忽略。

对照不同原生引擎版本时，`--native-dir build/<原生目录>` 选择其中的 `gmsv/gmsvjt.exe` 和 `saac/saacjt.exe`，默认仍为 `build/local-arena/native`。目录必须位于仓库 `build/` 内且路径不含符号链接；CLI、网关及账号初始化工具仍取 `build/local-arena/bin`。隔离服的数据和配置从仓库准备，不从这个目录复制；实际规则摘要仍须匹配模型。保留各版本引擎，用独立的新 work 目录测试，避免覆盖旧模型恢复所需的程序。容器工作程序不支持该参数时会拒绝启动。
数据保存在指定的本地 `build/` 目录，当前入口不使用 Docker volume。

## 插件扩展

`plugins` 数组可增加 manifest，再将 `strategy` 设置为该 ID：

```json
{
  "id": "my_policy",
  "version": "v1",
  "modes": [1, 2, 3, 4, 5],
  "command": ["/absolute/path/to/my-policy", "--stdio"]
}
```

每次决策启动一个有限时的进程。stdin 一行 JSON 包含 `schema_version=1`、`operation=decide`、`team`、`history`、`budget_ms`。
stdout 返回一个计划：`schema_version`、`match_id`、`turn`、`observation_id`、`orders`；
每条 order 只能包含 `member_id`、`actor`（player/pet）、`candidate_id`，须覆盖全部未提交且未保留意图的行动槽。
非法、过期、超时和超量输出会被拒绝。插件不能通过协议发送原始游戏命令或独立决策控制成员。
插件是用户安装的可信本地代码，进程环境过滤不等于文件系统安全沙箱。

## 共享客户端接口

`sactl battle-state`、`battle-act <JSON>`、`battle-events <cursor> [stream]` 提供结构化观察、带时序校验的动作和增量事件。
Web 会话的 battle-state/battle-events 接口复用 Go 公共层，动作仍受控制权检查。

被击飞移出的竞技场成员会继续接收公开战况，`battle-state.withdrawn=true` 且无候选动作，指挥官仅给在场成员下令。离场成员不是丢失观察的成员；仍须保留其同回合观战快照和历史截点。完整链路模拟可用 `--allocation 1,17,1,1` 指定双方新建角色的体力、力量、耐力、敏捷，四项整数各 0～20、合计 20，经过普通创建角色接口生效；这不是离线等级 35 的 120 点场景，也不修改线上角色。

模拟不同队员分工可重复传 `--member-allocation`，数量必须恰好为 `--mode`，双方使用同一顺序的阵容。例如 `--mode 2 --member-allocation 0,20,0,0 --member-allocation 14,4,2,0`；不能与 `--allocation` 混用。各成员各自满足 20 点创建预算，不能在队员之间转移预算。

模拟器在开赛前读取全部角色的公开属性，核对指定配点及双方同位置的阵容一致，保存 `initial-roster.json`；属性未知、不完整或不符时拒绝开始比赛。账号交替属于两队，同队位置按账号序号除以二计算。旧模拟器曾误用序号对人数取余，使用不同 `--member-allocation` 的旧多人结果不能作为所声明对称阵容的验收证据，须重新运行；统一 `--allocation`、默认配点和独立原生训练赛程不受此索引问题影响。

`simulate --fixture-level 35` 可准备更高等级的隔离人物，范围 1..140、默认 1。先按上述 20 点配点正常创建人物，退出全部测试客户端并关闭隔离账号服/游戏服/网关，再将本次新 work 目录里的合成人物存档设为所选等级，按原配点比例分配 `20+3*(等级-1)` 点；最大余数法取整数，同余数按体/力/耐/敏顺序，双方每人预算相等。重新启动并正常登录后必须通过公开等级/属性核验才匹配，不修改运行中人物、引擎内存、技能或宠物。此入口是预置测试存档，不模拟练级经历；原存档保存在 `original-profiles/`，修改摘要在 `prepared-roster.json`，实际生效值在 `initial-roster.json`。原生程序、数据表和模型规则校验照常生效。

覆盖特定边界时可加 `--require-withdrawal`（仅多人，要求己方原历史观察者被击飞离场后，指定策略至少产生一次新的计划，不能靠计划复用凑数）及 `--min-battle-turns 20`（至少一场已结算比赛达到 20 个决策回合）。未实际触发就报错，不生成 `commander-passed.json`；这两项不改变游戏伤害、击飞、回合时限或决策。通过产物包含 `observer_withdrawn_decisions` 和每场 `decision_turns`。高等级不保证击飞或长局，仍以实际记录为准，也不作为策略强度认证。容器内工作程序必须同时更新，旧程序会拒绝新增参数。

正式训练比较可先用 `sactl ai experiment` 冻结训练、验证、最终测试家族，再让 `train/evaluate --experiment` 复用清单；v0.2.13新训练默认按近期训练弱点调整类别内对手权重，`sactl ai league --data-dir` 可复核训练矩阵。这些训练成绩不等于独立评测或自动晋级。原生评估同时保留报告及同名 `.data` 目录，`sactl ai verify-evaluation --report` 核对模型、赛程、原始轨迹和策略动作。详细流程、恢复兼容及最终测试限制见 [learned 训练说明](learned-training.md)。

v0.2.13可用 `sactl ai compare-evaluations --baseline ./before.json --candidate ./after.json` 比较同一冻结验证集、相同完整对手集合的两份原生报告。命令先核验双方 `.data`，再按配置家族比较相同场景/阵营/对手的得分变化；保留截断不确定性，输出区间而不自动选择冠军。它是本地只读分析，不调用游戏服务器或大模型。

`sactl ai champion` 提供固定门槛的本地受控原生晋级、失败保留和历史回退，详见 [冠军流程](learned-champion.md)。队伍显式配置 `champion_directory` 后，新排队前重验并选择当前原生冠军，整场和重启恢复保持模型固定；不会改动用户配置，也不把原生晋级称为完整线上认证。

`sactl ai build-search` 固定战斗模型或规则策略，在隔离原生引擎中搜索同预算整数配点，保存双方轨迹并给出原生复验报告。支持中断恢复，只生成建议，不给线上角色洗点/加点。`build-pool` 冻结多个完整阵容，配合 `experiment/train --from-model` 开始下一轮战斗训练；用法、来源隔离和恢复限制见 [配点搜索说明](learned-build-search.md)。

v0.2.13 `sactl ai build-validate init|run|verify|compare` 进一步比较父子模型分别使用均衡/已选配点时的表现，分开报告配点和决策收益。它沿同一补充清单执行并支持显式恢复，报告不替代原实验最终测试或冠军认证；固定五项统计、截断处理及完整命令见 [联合配点验证](learned-build-search.md#分开验证配点收益与决策收益)。
敌方隐藏数值不填猜测值，未知战斗包保留 raw 和未知标识。现有 Web 按钮尚未全部迁入候选接口，不宣称所有客户端能力完全对等。

正式用户直接运行 `sactl ai`，无需仓库、`bin/` 前缀或编译器。

## 版本验证

Go 迁移版已完成真实原生 1v1、2v2、3v3、4v4、5v5 各连续两场对局，包含正常结算和资源恢复；
1v1 验证了 sactl 进程被强制终止后的恢复，5v5 使用 Go 训练产物执行联合推理。
重复查询回合时钟未改变服务端截止时间。旧 SQLite 数据已被 Go 训练器实际读取。
Go 单元测试覆盖未知写入恢复、队伍计划校验、身份锁、规则/模式覆盖、训练划分、
Chat Completions HTTP、重定向拒绝、超时、混合回退及插件取消。
外部真实大模型供应商仍需用户自行配置；目前 HTTP 协议通过本机兼容服务验证。
小样本对局不作为胜率提升证据。
