# 本地 AI 训练与评估

从 **v0.2.16** 起，神经模型默认导出为 `.safetensors`：权重使用小端 F32 二进制张量，网络配置、特征/动作契约和训练来源保存在同一文件的 JSON 元数据中。Go 直接加载，不需要 Python 或额外推理服务。`run`、`check`、`hybrid`、原生评估和父模型加载均兼容旧 JSON。

已有神经模型可直接无损转换，不启动训练、不改权重或策略身份：

```sh
sactl ai export-model --model ./model.json --output ./model.safetensors
sactl ai run --profile bot --strategy learned --model ./model.safetensors --matches 1
```

转换保留原文件，拒绝覆盖不同内容；不与 `--data-dir` / `--checkpoint` 混用。训练结束及 `export-model --data-dir` 默认生成 safetensors，显式指定 `.json` 输出仅供旧客户端兼容。旧客户端不能加载二进制模型，需先升级。训练检查点、优化器状态及历史冻结证据仍沿用原存储协议，不因推理文件转换而改写；SQLite 线性模型不适用此转换。

## 直接使用已登录的角色（v0.2.14）

日常在线对战无需初始化队伍目录或编辑配置。先用普通客户端交互登录，密码只留在后台进程内存：

```sh
sactl login --profile bot
sactl --profile bot chars
sactl --profile bot enter '角色名'
sactl ai check --profile bot --strategy learned --model /absolute/path/model.safetensors
sactl ai run --profile bot --strategy learned --model /absolute/path/model.safetensors --matches 1
```

`--model` 是用户本地的外部模型文件，安装包不带模型。v0.2.15 起，推理在本机 Go 客户端运行，检查观察 schema、规则接口版本、特征/动作和人数契约；不要求训练引擎与游戏服 CPU 或程序摘要相同。训练来源原样保留，服务器实际摘要和平台另存于战斗记录；离线训练恢复和受控评估仍核对原环境。`check` 校验模型及配置，不登录或排队；`run` 才读取现有会话、核对身份与服务端规则，然后自动建队、准备、匹配和出招。

省略 `--profile` 使用当前选中的会话；多人队伍重复传入 `--profile first --profile second`，每队只有一个指挥官。省略 `--mode` 按 profile 数量推断；显式模式必须与成员数一致。默认策略为 basic；v0.2.17 learned/hybrid 可自动查找本地模型，存在多个时需显式选择。可用 `--pet-mask 1` 登记第一个宠物槽，人物/宠物实际出战状态仍由正常游戏操作设置。

同一组 profile 默认复用 `~/.local/state/sactl/ai/<队伍摘要>/arena.sqlite3`（尊重 XDG_STATE_HOME），可以通过 `--state-dir` 指定。启动会输出实际数据目录。旧目录绑定原服务器、账号和角色，换角色时须使用另一 profile 或新的数据目录，不能混用未完成的指令状态。显式 `--state-dir` 不应由两个队伍共用。

这些会话由普通 `sactl login/logout` 管理；AI 退出保留已有登录，不写账号密码文件，不自动替换或启动后台进程。后台已退出时先重新登录。首次 Ctrl-C 在本场结束后停止，第二次立即退出。`--matches 1` 完成一场即停止，`--forever` 持续匹配。

LLM/hybrid 的本地配置也可全部用参数提供：

```sh
sactl ai run --profile bot --strategy llm \
  --llm-endpoint https://provider.example/v1/chat/completions \
  --llm-model your-model --llm-api-key-env STONEAGE_ARENA_MODEL_KEY --matches 1
# hybrid：将策略改为 hybrid，并加 --model /absolute/path/local-model.safetensors。
```

密钥由指定环境变量读取，不作为命令参数。其他参数有 `--llm-timeout`（秒）、`--llm-context-bytes`、`--llm-response-format`。使用 `ai run --help` 查看。`--config` 仍支持既有队伍文件，并可用策略、模型、数据目录和 LLM 参数覆盖；不能与 `--profile` 混用。


v0.2.13 同时提供 `basic`、`learned`、`llm`、`hybrid`，均在本地运行。LLM 使用可配置 Chat Completions，密钥由 `api_key_env` 指定的环境变量读取。`context_bytes` 默认 8388608，按完整请求 JSON 字节计量；超限报 `context_limit`，不丢弃早期回合。完整公共历史遵守各观察者事件游标与本地记录截点，不把后到事件传入旧决策。

LLM/hybrid 恢复先复用观察、策略及完整版本一致的最终计划，不重新调用服务商。已有写入/未知预留时不改策略重算。hybrid 保留 `local_proposal`、`proposal_id`、`final_plan_id` 和失败原因；服务商失败可回退本地建议，取消请求则停止。返回的多选、重复 JSON 键、拒答、截断和非法候选都会被拒绝。配置详见随包 `local-arena-agent.md`。现有 learned 产物仍为候选，功能测试不证明策略强度。

v0.2.13混合训练使用同一网络与 Adam，产物为 schema 6，仅支持声明的队伍人数；这不是胜率认证。`experiment-mix` 合并同规则、同父模型的单模式实验；`train --mixed-experiment` 采集和更新；`train --resume` 自动识别目录。不要改写复合实验为单模式产物。参数与步骤见[混合模式训练](#混合模式训练)。

learned 指挥保留倒下队员的完整观察。人物死亡、菜单关闭且服务器确认已处理其行动时，不重复下达人物指令；存活宠物仍独立规划。不能把人物倒下当成离队，也不能清除提交标记来强迫重新决策；部分已提交的正常行动应恢复原最终计划。

神经 learned 及 hybrid 的本地模型恢复优先复用当前回合已保存的最终计划，再处理需要新推理的历史。同回合旧计划被明确拒绝后，后续回合按写入/未知提交所对应的完整原观察重建记忆；部分计划复用记录不能替代原完整观察。无法唯一确定来源、不同观察混合执行或矛盾回执会拒绝该次推理。只有确认本回合尚无提交或预留指令时，才能回退 basic 并记录 `strategy_fallback.kind`。

v0.2.13在策略失败且已有写入/未知预留时记录 `strategy_rejected`，不派发新的剩余计划。`kind` 保留原失败类别，`reason=partial_submission` 表示已有提交或预留；`reason=invalid_submission_state` 表示无法核实提交状态。非可重试的计划续执行错误会结束指挥进程，原状态保留；单次策略截止时间仍按既有重试规则处理，本次拒绝不算成功派发。不要删除历史或预留记录、改策略版本来强迫重算。服务端自动完成死亡人物指令的标记本身不构成本地提交，存活宠物仍按正常规则处理。

新版 `records` 保存与动作预留同事务提交的 `submission_intent`，发送后另存 `submission` 回执；未知结果保留为未知，written 不是技能生效证明。决策的 `team.history_record_cutoff` 限定当时可见的本地记录，成员的 `event_cutoff` 限定公共战斗事件；后到的命令/缺口记录不能改变较早捕获的决策。旧记录没有这些字段时不补造截点，能唯一确认完整原观察才恢复；不能把旧日志自动当作新版完整执行上下文。

先核对 `sactl ai train --help` 和 `sactl ai evaluate --help`。本文的原生 PPO 命令需要支持 `--environment`、`--data-dir` 的版本；旧版仅支持 SQLite 线性训练。使用用户指定的数据目录、引擎和资源预算，不自行启动无限训练、生产匹配或下载大型镜像。

v0.2.13正常关闭原生 worker 时会发送 EOF，并最多等待 10 秒完成归档；退出失败或超时使命令失败，不能据已有 checkpoint 或评估报告判断整条命令成功。多个采集 worker 的关闭错误都会保留。取消/失败请求仍立即终止对应 worker，已有数据保留，不自动重跑。底层记录须独立验证序号及完整性；共享目录的 `status.json` 仅代表最后报告的一个 worker。需要匹配的新 CLI/worker，旧安装和冻结实验不因源码更新而获得这些修复。

直接 `docker run` 的 worker（含 `environment init` 生成配置）会带本次调用唯一的归属标记，关闭时另用最多 5 秒核对并清理对应容器；不删除 volume 或其他容器。不要覆盖保留的 `org.stoneage.training.worker` 标签。检查或归属验证失败会报错，不得因此执行全局 Docker 清理。自定义 shell/SSH/Docker 包装命令仍须自行管理远端进程，不能仅因本地命令已退出就宣称容器已停止。

训练镜像的发布验收分别检查原生 Linux amd64/arm64 的镜像和 CLI 版本、skill、volume 恢复及模型导出评估。只有构建成功或本地替身测试成功，不代表实际镜像已通过这些检查；未发布版本仍不能作为用户可下载的工具包。

## 实验性队伍计划计数

规则反馈采集与监督更新另见下方“规则反馈训练”，与 PPO、实战示范输入分开。

v0.2.13新建原生训练可显式指定 `--plan-features target-counts-v1`，默认 `none` 保留原计划 GRU。每个候选额外读取此前友方计划的目标、物理攻击、四类异常攻击及治疗覆盖计数；这些是尚未结算的选择，不是已生效的状态或保证恢复量。模型仍能选择重复目标，不加入强制选招规则，未证明胜率提升。

```sh
sactl ai train --environment ./environment.json --data-dir ./counts-training \
  --mode 2 --plan-features target-counts-v1
```

新模型为 `commander-policy-v3`；与 `--plan-scope member` 合用时为 `independent-member-policy-v2`，计数仅含本成员此前选择，仍限离线对照。普通指挥模型可由新版 learned/hybrid 加载，人数/规则/来源限制不变。恢复时不传该参数；继承父模型时保持已有 plan_features，显式矛盾值拒绝，不自动转换旧模型。旧 SQLite/示范训练 CLI 不接受该参数；旧客户端拒绝新配置/模型时须使用支持版本，不删字段降级。默认无此配置的模型和检查点继续原行为。正式强度结论必须来自分别训练、独立评测的候选，不能从新输入或短局测试推断。

神经指挥拒绝非空缺失成员名单和成员数量不符；格式错误的名单记为 invalid_observation。错误文本统一使用 `learned:`，不从旧版 `learned v2:` 前缀猜模型架构。检查决策 diagnostics 的 architecture、策略版本，以及 strategy_fallback 的结构化 kind；不要删除缺员字段来绕过完整队伍检查。

## 规则反馈训练

先核对 `sactl ai collect-feedback --help`、`sactl ai train-feedback --help`。这是本地实验功能：模型实际对战，规则老师只对模型已遇到的公开观察提出单独建议，不代替实际动作、不读取隐藏敌方配点/后续结果，不作为 PPO 轨迹。尚未证明提高胜率。

已有模型必须是同一冻结实验的候选或精确声明的父模型，并支持该人数/规则/观察契约。采集只使用 train 配置组，不能拿 validation/test 对战生成训练标签。

```sh
sactl ai collect-feedback --environment ./environment.json \
  --experiment ./experiment.json --model ./candidate.safetensors \
  --data-dir ./feedback-round1 --matches 32 --teacher sustain \
  --opponent basic --opponent sustain --workers 2
sactl ai train-feedback --collection ./feedback-round1 \
  --data-dir ./feedback-training1 --epochs 4 --output ./feedback-model1.safetensors
```

采集保存双方原始轨迹，每场只把模型一方加入反馈数据。默认采样动作，显式 `--greedy` 改为贪心；默认 teacher=sustain、opponents=basic/sustain、seed=1、matches=32。比赛总数最多 256，必须覆盖每个对手至少一个完整配对家族，当前实验通常是 8 的倍数；旧实验沿用其配对方式。`--workers 1..8` 只改变采集并发，不能改变随机种子、换边和数值结果。

```sh
sactl ai collect-feedback --environment ./environment.json --data-dir ./feedback-round1 --resume
sactl ai train-feedback --data-dir ./feedback-training1 --resume --epochs 2
```

采集恢复完成原有总场数，不传模型/老师/对手/seed/matches；完全采集完后重复恢复无需启动引擎。训练恢复表示再训练指定轮数，不传 collection/model/优化参数，也不再依赖外部采集目录。新训练默认使用最后一个 `--collection` 的模型初始化，可显式 `--model` 选择同实验兼容模型。重复 `--collection` 按顺序聚合历史轮次，当前 CLI 合计最多 256 场反馈；重复同一视角（即使换标签）、未完成采集、不同实验或观察契约拒绝，不能删原始记录来绕过限制。

两命令均支持 `--stop-at-data-bytes N`，0 禁用，在完整检查点之后统计目录内普通文件逻辑长度，包括 volume 内的文件。达到阈值保留数据、输出触发阶段并失败退出；不是硬磁盘配额，可能超出一次写入。恢复可调高阈值或关闭，不自动清理。Ctrl+C 失败退出并保留最后提交状态；初始化未完成可能没有有效指针，保留现场并换新目录。

只有训练成功才导出 candidate；存储停止或取消不导出。`export-model --data-dir ./feedback-training1` 可重新导出，也可 `--checkpoint` 指定已保存轮次；拒绝覆盖不同内容。产物沿用 native 推理 schema，training_report 绑定反馈标签、确切初始化、采集模型和训练记录，保留采集策略的训练影响及 recorded 来源。完整方法与评估使用随包 learned-training.md；继续用原实验 validation/evaluate/verify-evaluation 验证，不把候选或 loss 当作晋级。

## 旧战斗记录导出

v0.2.13先核对 `sactl ai export-data --help`，无需 Python、Docker 或游戏登录即可读取用户指定的 battle-record 根目录或单场目录：

```sh
sactl ai export-data --records ./battle-records --format builds --output ./builds.jsonl
sactl ai export-data --records ./battle-records --equal-points --output ./transitions.jsonl
sactl ai export-data --records ./battle-records --mode pve --output ./pve-transitions.jsonl
```

默认 transitions/pvp-1v1；builds 只导出同预算、外部条件一致的 1v1 PVP，观察一致不等于受控实验。输入只读，支持 events.jsonl.gz。输出必须是新文件且父目录已存在；无有效记录或取消不发布，重复 match_id 拒绝整个导出。stdout JSON 含无效原因/过滤计数及输出 SHA-256，部分有效时仍会成功，不能只看退出码。来源日志及摘要不是服务端签名。

输出保留旧 schema 1、换边/重复分组和终局含义；默认排除 defeat/turn_limit 以外原因，`--include-abnormal` 才纳入。PVE 单独标记，不用于 PK 胜率。unknown 的策略/动作掩码不补造，结算标签不作决策输入；离场后没有下一观察不虚构后续 transition。此数据缺少当前策略概率、候选集和完整队伍历史，报告明确 `on_policy_ppo=false`；不能直接放进原生 PPO 的 shards 或当作已支持的神经热身输入。完整旧 JSONL/SQLite 转换仍须实现。具体字段/限制见随客户端附带的 learned-training.md。

原始 ruleset_id 必须是 64 位十六进制摘要；早期只有 native-validation 等文字标识的调试记录会记为 invalid_rules。保留原始证据，不改字段伪装成当前规则。摘要只是记录字段，不是来源签名。

## 本地比赛示范导入

v0.2.13 `sactl ai import-demonstrations --database ./team/state/arena.sqlite3 --output ./demonstrations.jsonl` 从本地指挥官 SQLite 重建可模仿的整队决策，重复 --database 可导入多个停止的数据库。必须先停止采集并完成 WAL checkpoint；Mac 不得打开容器仍在写入的 SQLite/WAL。工具仅以 immutable/read-only 访问，非空 WAL/journal 会拒绝，摘要稳定不等于已经证明没有写进程。

输出 JSONL 和 `.manifest.json` 均须是新路径。只有明确 defeat 结算、完整连续观察/历史、无回退/缺口/未知提交、每条选择都有匹配 written intent 的整场才导入；重复计划不重复计样本，重复比赛同一方拒绝整个导入。written 不是技能生效确认。检查报告的 excluded，旧 basic 未保存规则摘要时记为 missing_rules_metadata，不补造来源。

新指挥官的所有策略均在初始化和逐回合采集时请求 BTRULES，保存实际观察中的规则摘要/平台。非神经策略允许旧服务器不返回此扩展，额外等待约 250ms 后保留缺失字段；查询错误、必需的 BTIME 或整体取消仍返回错误。神经 learned/hybrid 要求元数据存在和接口兼容，不要求训练 CPU/程序摘要与服务器相同。仅发送请求不是收到响应，也不能把旧数据库补上当前服务器的规则。旧本地客户端若不支持 BTRULES 查询，先更新工作程序。

v0.2.13可用 `sactl ai train --demonstrations ./demonstrations.jsonl --data-dir ./demo-training --epochs 2 --batch-episodes 8` 进行本地模仿，不启动 Docker、游戏服务或 Python。训练目录必须新建、父目录存在；人数/特征从数据取得，同次训练要求相同规则/平台/人数/特征。完整冻结数据、模型、Adam 及每轮报告；用 `sactl ai train --data-dir ./demo-training --resume --epochs 2` 额外训练两轮，不再依赖原始输入路径。恢复不能覆盖参数或数据。同目录并发写入、损坏证据或混用原生 PPO/旧 SQLite 参数会拒绝。

Ctrl+C 保留最后一轮已提交状态，输出 demonstration_training_interrupted 并以失败状态退出；初始化未完成则可能尚无可恢复指针，保留现场、换新目录。完成至少一轮后用 `sactl ai export-model --data-dir ./demo-training --output ./recorded-model.safetensors` 导出候选，可选 --checkpoint 固定旧状态而不倒退进度。不能把 demonstration-latest.json 或 learning 文件当作推理产物。胜负不充当价值标签，loss 下降不代表胜率提高。

示范候选为文件 schema 3，网络架构仍是 commander-policy-v2；更新后的 learned/hybrid 能读取，旧客户端会拒绝。配置 model 指向导出文件；在线推理须符合观察/动作/人数契约，训练平台仅为来源记录。recorded_training 保存来源摘要和 roster 分组，不伪造合成配置分组或竞技场认证。在线指挥、隔离 simulate 和规则/平台/特征兼容的原生 evaluate 可以加载；评估会在 unverified_source_artifacts 标记候选/对手的未知配置重叠，verify-evaluation 从冻结产物重算标记，不能删除它冒充独立评估。标记非空不允许自动冠军晋级。导出会重新核验数据/报告/权重/优化器进度，损坏或零训练轮时拒绝，不覆盖不同内容的已有模型。

原生 PPO 接续先用 `experiment --environment ./environment.json --from-model ./recorded-model.safetensors --mode 5 --output ./experiment.json` 固定父模型和目标模式，再用 `train --environment ./environment.json --experiment ./experiment.json --from-model ./recorded-model.safetensors --data-dir ./native-training`。恢复用同目录 --resume，不重复指定父模型或实验。沿用精确权重、重新初始化 Adam；只从新引擎轨迹进行 PPO。导出的 schema 4 子模型保留原实战来源和新的原生训练分组，不能把示范数据改为 PPO schema。用原实验 validation 评估子模型；跨人数初始化不会给父模型增加推理模式。

v0.2.13 build-search 的 --model / --opponent-model 均支持匹配规则/平台/人数的实战模型。搜索、build-pool、experiment 及后代用 recorded_selection_artifacts 保留直接或间接参与配点选择的实战模型摘要；从零训练也继承池的来源。此类原生子模型为 schema 5，learned/hybrid 可加载；旧客户端拒绝，不要改 schema 绕过。它与直接模仿/权重继承的 recorded_training 分开，评估仍保留 unverified_source_artifacts，不能据此自动晋级。恢复、报告与导出池会核对 search-policies 中的固定模型，不要删掉这些文件；详见随包 learned-build-search.md。

默认 --features commander-observed-v8，支持 v6/v7；它编码已有观察，不能使未知技能自动获得支持。数据按 recorded-roster-v1 分组，不能替代原生合成配置分组或证明训练/测试不重叠。不能把示范数据传给当前 PPO 或旧 SQLite 线性训练；不得手工添加概率、初始敌方配点或改 schema 来绕过检查。

## 父模型探索初始化

v0.2.13首次原生 `train` 可在 `--from-model` 基础上指定 `--initial-policy-scale 0.5`；单模式 1v1～5v5 和 `--mixed-experiment` 均支持。值域为 `(0,1]`，须能表示为正 float32，默认 1 不改变旧行为。小于 1 时仅缩放最终动作评分层的权重和偏置，采集前执行一次，重新初始化 Adam；不修改父文件、观察编码、价值头或历史记忆，不在每回合推理时重复缩放。单模式不得同时开启模仿热身。

```sh
sactl ai train --environment ./environment.json --experiment ./next-experiment.json \
  --from-model ./parent.json --initial-policy-scale 0.5 --data-dir ./exploration-training
sactl ai train --environment ./environment.json --data-dir ./exploration-training --resume
```

实验必须事先声明同一父模型。新采集轨迹记录实际缩放后策略的身份和概率，不能重新标记旧轨迹来训练。目录保存原父模型、初始化权重/优化器和初始化回执，导出报告绑定同一来源；恢复会重算并核对初始化，但继续使用已训练状态，不再次缩放。缩放模式采用新的 checkpoint schema，旧客户端应拒绝读取。新参数不能用于 SQLite、示范训练或覆盖 `--resume`，即使传入 1 也需要父模型且不能在恢复时传入。缩放是探索实验，没有稳定胜率提升证明，仍须独立评估。

## 混合模式训练

先分别创建每个模式的 `experiment`，再按人数递增的顺序合并。若子实验声明父模型，合并及首次训练均需同一 `--from-model`。`--weights` 是各模式 PPO 损失权重，`--families-per-batch` 是采样量；每个家族 8 场，两项默认每个模式都是 1。

```sh
sactl ai experiment-mix --experiment ./mode1.json --experiment ./mode5.json \
  --weights 1,1 --families-per-batch 1,1 --output ./mixed.json
sactl ai train --environment ./environment.json --mixed-experiment ./mixed.json \
  --data-dir ./mixed-training --batches 1 --workers 2
sactl ai train --environment ./environment.json --data-dir ./mixed-training \
  --resume --batches 1 --workers 2
sactl ai evaluate --environment ./environment.json --mixed-experiment ./mixed.json \
  --mode 1 --model ./candidate.safetensors --opponent basic --output ./validation-1.json
sactl ai verify-evaluation --report ./validation-1.json
```

首次训练输出 candidate 的实际路径，评估使用该路径；为每个声明模式分别运行评估，不能只报汇总分。场景和批次采样量由 manifest 固定，不同时传 `--experiment`、`--mode`、`--batch-matches`、热身、示范或 SQLite 参数给混合训练。当前混合训练直接进行 PPO，规则对手均匀抽样；可配置 `--rule-opponent` 与 `--opponent-mix`，默认比例 30,50,20。`--plan-scope member` 仍是离线独立决策对照，不能用于在线指挥。

恢复只允许调整本次批次数、workers、输出及存储阈值；中断/关闭失败保留已提交进度且不导出成功模型，取消返回失败并输出 `training_interrupted`。`export-model --data-dir` 自动识别混合目录，`--checkpoint` 可选历史版本。只采集、没有被接受的更新时不可导出。

`evaluate --mixed-experiment` 必须显式 `--mode`，使用完整 validation/test 分组，报告及 `.data` 保留完整混合实验与当前子实验。`--split test` 在实验旁固定整个模型，再分别固定各模式的对手；不能在看过部分模式结果后换模型。`verify-evaluation` 重放双方策略；`compare-evaluations` 仅比较同一混合实验、同一模式的 validation 报告，区间调整仍仅覆盖该份比较的对手，不自动覆盖多个模式。

### 跨实验共享验证集

不同父模型或训练目标产生不同实验编号；普通评估会排除其他实验模型的留出家族。若对照研究有意让两边共享完整、相同顺序的配点家族及 train/validation/test 划分，先声明这一关系：

```sh
sactl ai experiment-compare --left-experiment ./commander-mixed.json \
  --right-experiment ./member-mixed.json --output ./comparison.json
sactl ai evaluate --validation-comparison ./comparison.json --mode 5 \
  --model ./commander.json --opponent-model ./member.json \
  --environment ./environment.json --output ./comparison-mode5.json
sactl ai verify-evaluation --report ./comparison-mode5.json
```

`--model` 必须是左侧实验的实际混合候选，唯一的 `--opponent-model` 必须是右侧实验候选；不能互换或重标来源。只允许完整 validation 分组，不接受 `--split test`、其他对手、`--experiment` / `--mixed-experiment` 或覆盖场景/场数的参数。每个模式单独执行，训练和选型数据仍排除。声明不证明两边训练预算相同或某种架构更强。

报告和 `.data` 绑定双方完整实验、模型及原始轨迹。中断留下的证据目录固定原对手；更换模型需要新的输出。`compare-evaluations` 还要求相同的比较声明，不能混入普通评估或另一个对照。此声明不释放 test、不影响冠军登记册，也不授予模型资格；旧客户端会拒绝新参数。

### 多模式冠军

v0.2.13 `champion init/challenge --mixed-experiment ./mixed.json` 使用一个登记册和一次挑战预算覆盖全部模式。必须每个模式都通过，`assessment.bounds[].mode` 标明人数；任一模式有截断/不达标就不晋级。默认每模式、每规则对手 256 个八局家族，两个模式首次共 20,480 场，五个模式共 51,200 场；创建登记册不执行对局，只有用户已授权的有界挑战才运行。恢复已完成模式只做核验，不重跑；失败、放弃和回退保留所有模式的测试曝光及挑战次数。状态、回退、放弃命令不变。相应队伍可用同一 `champion_directory` 选择同一模型，各队 state_dir 必须独立。规则、预算及例子见随包 learned-champion.md；功能存在不代表已有合格混合冠军或已发布。

## 动作频次加权

v0.2.13在模仿阶段可显式启用 `sqrt-action-frequency-v1`；默认 `none` 保留原行为。原生 `ai train` 的热身使用 `--warmup-action-weighting sqrt-action-frequency-v1`，要求开启热身；`ai train --demonstrations ...` 和 `ai train-feedback` 使用 `--action-weighting sqrt-action-frequency-v1`。这些都是新训练目录的配置；`--resume` 使用已经保存的权重算法与数据，不能重新指定该参数。

按完整训练集的“人物/宠物 + 动作类型”计数，逆频次平方根加权后将所有示范动作的平均权重归一到 1，包括被迫等待。不会按胜负、账号、目标编号、验证集或当前小批次重新调整；不改变合法动作、队伍指挥、模型输入或 PPO 损失。报告同时保留原始交叉熵、加权损失、各类计数和权重。恢复和导出核对冻结数据，旧客户端不应继续读取它不支持的加权检查点。

混合模式 PPO 和旧 `--database` 入口拒绝这些参数。该选项用于检验低频动作拟合问题，不是要求玩家一定换宠，也不代表已经提高实际胜率；保持明确的模型对照与评估。

## 新模型训练

训练和推理都在 Go 客户端中，不需要 Python 运行环境。`--environment` 指向 schema_version=1 的 JSON，`command` 为原生引擎启动 argv 数组。调用方需要事先配置兼容引擎；这不是游戏账号配置。Linux 引擎可由 Docker 执行，模型训练器也可在已挂载数据 volume 的容器中运行；Mac 原生进程不能把 Docker volume 名直接当成本地目录。

v0.2.13 `sactl ai environment init --image <已安装的 ai-training 镜像> --directory <新目录>` 检查真实引擎后生成 environment.json，固定本地镜像 ID，规则档案保留在目录的 rules 中。它不下载镜像、不登录游戏；失败的目录和 worker.log 保留供诊断。`environment check --environment <文件>` 再次检查规则/平台。训练镜像为 `ghcr.io/k0ngk0ng/stoneage/ai-training:v0.2.13`，先显式 `docker pull`。在挂载 volume 到 `/data` 的容器中直接传 `train --data-dir training`、`train --data-dir training --resume` 等 ai 子命令；入口已含 `sactl ai`，环境由 `STONEAGE_TRAINING_ENVIRONMENT` 提供，显式 --environment 优先。Mac 原生训练的数据使用本地目录；不要把 volume 名当成本地 --data-dir。详见随客户端提供的 learned-training.md。

```sh
sactl ai train --environment ./environment.json --data-dir ./ai-data --batch-matches 32 --batches 10
sactl ai train --environment ./environment.json --data-dir ./ai-data --resume --batches 10
```

`--batches` 表示本次再完成多少个优化批次；`--batch-matches` 为每批实际采集局数。默认场景为 1v1，每人人物点数 120、每人一只受控宠物点数 120、等级 35；`--pet-points 0` 禁用宠物。点数是隔离训练环境的整数预算，不会给线上角色洗点。`--mode 2` 等可以训练多人输入形状，但每个模式都须独立评估。

新实验默认在 PPO 前进行 32 场规则采集、8 轮模仿热身。`--warmup-matches` 为 4 的倍数（0 禁用），`--warmup-epochs` 控制轮数，`--warmup-learning-rate` 默认 .001；重复 `--warmup-teacher basic` / `--warmup-teacher focus` 等可覆盖默认五种教师（basic、focus、guard-break、defensive、sustain）。热身不将胜方所有动作视为正确标签，不向 PPO 混入规则数据；完成后保留模型、重新初始化 PPO 优化器。热身场数/轮数在 `warmup_games`/`warmup_epochs` 单独报告，不计入 PPO 的 `games`/`completed_batches`。

原生模仿热身按分片完整校验观察、老师动作与赛程，然后只保留训练所需数值输入在内存。原始分片须继续保留，恢复热身仍重新读取校验；不产生独立数值缓存格式，也不改变模型/优化器格式或训练顺序。人数、回合长度和计算图仍影响实际内存；`--stop-at-data-bytes` 是磁盘保留量阈值，不是 RAM 上限。

v0.2.13 `train --gae-lambda` 开放原 PPO 的优势估计参数，默认 .95、范围 [0,1]；1 使用完整已采集回报，截断仍使用价值 bootstrap。它不改变奖励或 `--sequence-length`，不保证胜率改善。先确认安装版本支持该参数；它只用于新训练，`--resume` 固定保存值，旧 SQLite 训练不接受。需要对比时使用同一父模型和冻结实验分别新建目录，不能改写旧 checkpoint。

`train --entropy-weight` 控制 PPO 条件动作熵奖励，默认 .01，必须是有限非负数；0 只关闭这项奖励，采集仍从当前策略概率采样，实战默认仍为条件贪心。它不是温度或随机动作开关，不保证越大/越小越强。新实验保存该值，恢复和旧 SQLite 训练拒绝覆盖；对照实验只改变这一项并保留完整验证结果。

v0.2.13 `train --opponent-mix 80,10,10` 指定规则/历史/同代自我对战的整数百分比，范围 0..100、总和 100；默认 `30,50,20`。它不改变类别内部的 `--opponent-sampling` 权重规则。历史池为空时其份额转给同代模型；比例是逐局概率，不保证每批场数精确相等。新比例随 checkpoint 固定，`--resume` 和旧 SQLite 训练不能覆盖；旧程序遇到该参数或 checkpoint 必须拒绝，不能忽略后继续。`league` 显示有效 `opponent_mix`，旧目录保留原赛程；对手占比不是胜率，调整后仍须独立评估。

新训练日志可读 `report.epochs[].objectives` 的策略损失、已加权价值/熵损失及未加权 `conditional_entropy`，均为更新前每队伍回合均值。它沿已记录前缀累加条件动作熵，不是精确联合计划熵或胜率；旧记录缺失时不要补成 0。这些诊断不替代独立比赛验证。

训练前后比较可用同一个子实验执行 `evaluate --experiment <子实验> --model <其声明的父模型> --split validation`。只接受完整摘要与 `initial_model` 相同的原父模型；它的规则、声明人数及训练/选择数据排除仍要通过检查，不修改模型归属。子模型与父模型使用同一保留赛程，报告均可 `verify-evaluation`。这条父模型路径不开放 `test` 或晋级；跨人数迁移也不赋予父模型未训练人数的推理资格。旧 CLI 拒绝时应更新，不得修改模型 JSON 绕过来源检查。

中断后使用相同目录加 `--resume`。已完成比赛、待训练分片、模型、优化器、随机序列进度和历史对手引用保留；未完成对局不当成有效样本。恢复使用 checkpoint 中的参数，不能同时传新的点数、seed、epochs 等设置。引擎程序/表摘要、平台或场景版本变化时拒绝恢复，应新建实验目录。不要删除锁文件来并发写入同一训练目录；进程退出会自动释放系统锁。

v0.2.13原生 `train --workers 2` 使用独立引擎进程并行采集，允许 1～8、每次调用默认 1；这是本次资源设置，可在 `--resume` 时调整，不改变模型配置、比赛序号/seed 或串行优化器顺序。进度中的 `collection_workers` 显示本次设置。各引擎必须具有相同规则/平台/场景，按预定顺序提交连续分片；失败窗口不参与训练，中断后保留已提交前缀并重采其余比赛。更多 worker 会占用更多内存，不代表梯度更新同比加速。数值内核加速也不会自动增加采集进程；并行度仍由本次 `--workers` 指定，完整训练耗时需在本机实测。该参数不适用于示范/旧 SQLite 训练，也不让多个训练器并发写一个目录；旧 CLI 拒绝新参数，应更新客户端。

原生 `train --stop-at-data-bytes N` 在已保存 checkpoint 后检查数据目录保留文件的逻辑字节，达到阈值输出 `storage_limit_reached` 并非零退出；不删数据或自动导出候选。默认 0 关闭，恢复可调整且需再次显式传入；不影响训练配置或优化器。它不是硬配额，单次提交可超出阈值；不跟随目录内部符号链接；数据目录位于 Docker volume 内也照常统计，但目录之外的引擎归档、镜像或其他 volume 不计入。需要留磁盘余量；示范和旧 SQLite 训练不接受此参数。完整口径见随包 `learned-training.md`。

v0.2.13原生训练增加 `--plan-scope team|member`，默认 team 保持统一指挥。member 仅作离线独立成员对照：完整信息、历史编码与参数共享，每位成员只协调自己的人物和宠物，不共享队友当前拟定动作；仍用共享价值头和整队 PPO 训练，不代表每人独立训练一个网络。例：`sactl ai train --environment ./environment.json --data-dir ./member-baseline --mode 2 --plan-scope member --warmup-teacher independent-control`。模型架构为 `independent-member-policy-v1`，可作为 `evaluate --opponent-model` 的真实训练对手，或用 `evaluate --model` 单独评估；原有实验/人数/来源限制仍生效。它不是线上策略，learned/hybrid 及联网 simulate 会拒绝加载。

恢复不得覆盖 plan-scope；`--from-model` 保留父模型架构并拒绝矛盾参数，不能给现有模型改标签制造对照。新参数不适用于旧 SQLite/示范训练 CLI，旧客户端拒绝时应升级。比较统一指挥优势需在相同初始化、训练预算、教师/对手和固定评测赛程下分别训练，不能把随机权重、不同训练预算或少数胜局作为结论。

当前受控场景为 `controlled-battle-v8`，从零新建模型采用 `commander-observed-v8` 特征及 `attack-guard-switch-guardian-v7` 动作契约，包含忠犬攻击。离线击飞、离场指令及 PVP 收益处理与竞技场共用规则，支持离场成员观战、只为在场成员规划和有序历史。v6/v7 模型仍按原特征推理/评估；`--resume` 和 `--from-model` 保持 checkpoint/父模型版本，不自动升级。单决策超过 512 个效果时明确拒绝/回退，不静默截断。其他旧场景/特征版本不可改标签绕过检查；`export-model` 不负责跨特征版本转换。受控多人击飞后采集和 PPO 已验证，仍不能描述成所有多人技能及策略强度已完成验收。

v3 修正了受控宠物技能槽容量及出战宠物观察缺失，增加毒、石化、混乱、催眠攻击。出战选择通过只读 `S(AI)` 的可选 `active_pet` 字段投影到 `Own.BattlePetSlot` / `BattlePetSlotKnown`，不会伪造技能成功回执。v2 及以前的受控宠物可能始终等待，旧实验不能作为宠物决策强度证据；即使网络维度不变，也必须按新动作/规则版本重新采集训练。

v6 加入收宠/召唤动作和己方备用宠物表示，最多 40 个实体行，其中最多 20 个是场上人物/宠物；备用宠物不产生行动槽，也不计入群体治疗。全队仍只有一个指挥官，换宠回合的 W 指令仍属于原宠。召唤要求自身观察中的出战选择、SPET 待机掩码、PETST 可召唤掩码及骑乘状态；未知时不猜测。历史按真实顺序编码等待、收宠和召唤。

v7 场景增加 `--reserve-pets 0..2`，默认 0；用于新建 train、experiment、evaluate、build-search。每人额外携带指定数量的备用宠物，双方数量一致，每只宠物独立满足 `--pet-points` 预算；最多三只可选宠物，符合原生 SPET 限制。初始宠物保留七个已核实技能，随机备用宠物拥有攻击、防御及一种破防/状态技能，按槽位压紧排列。模型通过公开观察读取各宠物属性与技能，编号掩码只作场景元数据，不作为连续数值输入。配点搜索同时搜索备用宠物配点和专长技能，冻结实验、配点池、评估及断点恢复均保留完整阵容，不允许覆盖 reserve-pets。需新建 v7 实验；特征/动作契约仍为 v6，旧场景产物不可改标签复用。真实多宠切换和采样训练已验证，少量对局不代表学会有效的换宠战术。

v5 增加 `--healing-items 0..15`，默认 0。双方每个人物的背包各供应相同数量的小块肉（模板 1234），每件占一个槽，只在新对局重置；回合内真实消耗，不自动补充。此参数可用于新建 train、experiment、evaluate、build-search，随冻结实验、配点池与恢复保存，不能额外覆盖。模型观察己方剩余库存；没有可靠物品身份时排除该道具动作。可以与治疗护甲组合，但不代表所有道具已经支持。

v4 增加受控治疗装备：新建 train、experiment、evaluate 或 build-search 时可设置 `--healing-magic 10`（单体）或 `20`（整侧），默认 `0` 不配治疗装备。双方使用同条件的原生护甲及 100 MP，实际消耗和恢复由原生魔法结算；已冻结实验或配点池继承此配置，不能用额外参数覆盖，恢复也不得修改。单体/群体治疗分别消耗 8/20 MP；候选按当前公开 MP 排除不可支付的魔法。它是离线实验装备条件，不是给线上角色发装备或补气力。

魔法身份与耗气、名称来自同一条原生 J 报文的可选 `id=` 扩展，投影为 `Magic.ID/IDKnown` 和候选 `MagicID/MagicIDKnown/MPCost`；旧服务器缺少 ID 时保留普通客户端操作，但 learned 不猜测治疗语义。卸下装备清空旧魔法。动作词表升级后需要新建训练，不得把 v3 模型改标签复用。规则热身现可使用 sustain 主动治疗、使用小块肉并替换濒死出战宠物，PPO 采样也已覆盖治疗；复杂道具和其他魔法仍未覆盖。

目录包含 `shards/`（压缩轨迹和校验清单）、`learning/`（模型与优化器）、`checkpoints/`、`latest.json`、`models/`、`reports/` 和 `engine.log`。模型和优化器只在热身轮次或 PPO 批次更新后保存大文件，每场比赛只提交小进度记录。恢复会继续未完成的热身；旧 schema 1 实验按原配置继续，不额外添加热身。不要手动改这些文件来绕过兼容校验。

v0.2.13原生 `ai train` 默认保持单次采样 GAE；实验参数 `--opening-rollouts 2..64` 固定完整开局、引擎 seed、阵营及对手，每次实际重新运行、使用独立动作 seed。重复数必须整除 `--batch-matches`（仍是实际场数）。`--policy-advantage gae` 用于同采样调度对照，`opening-loo` 使用同组其他终局回报均值作策略基线，保留 GAE 价值目标和 KL 回退，要求重复数至少 2。值 1 禁用重复采样；不影响热身。

例：`sactl ai train --environment ./environment.json --data-dir ./repeat-loo --batch-matches 32 --opening-rollouts 4 --policy-advantage opening-loo`；续训只传 `--environment ./environment.json --data-dir ./repeat-loo --resume` 和可选 `--batches`。两种重复采样模式均要求完整终局；有截断时保留待处理批次并拒绝更新，不自动改为败局或跳过。恢复也会拒绝同一截断批次，需要新目录和更长采集上限。参数不能用于 SQLite/示范训练或覆盖恢复配置。

新目录使用 checkpoint schema 4，按场提交，支持组内中断恢复；每批保存 `openings/` 来源回执，恢复/导出会重放采样核验并检查模型和 Adam 状态链，成本随历史数据增长，但不重跑所有梯度更新。旧客户端须拒绝该格式。历史独立 `opening-*-pilot` 诊断目录仍不能传给 `train --resume`。复制同一局日志不构成独立采样，胜局不等于每条动作正确；目前没有可靠胜率提升证据，默认算法和晋级门槛不变。这些参数从 v0.2.13 提供，旧安装先升级并检查实际 `--help`。

结束时 `candidate_saved.model` 给出模型路径；不指定 `--output` 时使用内容摘要命名，不覆盖旧模型。训练输出中的 `candidate` 只表示训练产物，尚未证明更强，也不会自动替换正在比赛的模型。支持 v2 的v0.2.13可在队伍配置的 `model` 中显式选择此文件，`learned` / `hybrid` 均按 schema 加载；下载区旧版本不因此自动获得能力。

v0.2.15 起，v2 排队前核对观察 schema 和规则接口版本，并通过 `BTRULES` 采集服务端实际规则摘要及平台；缺失元数据仍拒绝，但训练来源与服务端 CPU/程序摘要不同不阻止本地推理。每场固定模型；同回合轮询不重复推进记忆，部分提交后沿用已保存计划（hybrid 沿用大模型最终批准的计划）。缺少完整历史、事件缺口或场内不支持的观察会明确记录失败原因；只有确认本回合尚无提交或预留时才可回退 basic，不能把回退结果计为模型决策。诊断中的条件动作概率和 `estimated_return` 不是经过校准的胜率。目前有初步完整 1v1、2v2、5v5 执行验证，5v5 已验证原历史观察者离场后的继续规划及保存历史重放；这不代表全部模式、技能、完整竞技场规则一致性或策略强度已经通过验收。

指挥官整体取消时（立即停止或隔离测试总超时），当前决策直接结束，不记录为策略失败、不生成 basic 回退计划；单次策略预算耗尽而指挥官仍运行时，仍须先满足没有提交或预留指令的回退条件。首次 Ctrl-C 仍是等待当前比赛结束后停止，再次 Ctrl-C 才立即取消。

v0.2.13 `state_dir/arena.sqlite3` 的 `records.kind=turn_timing` 记录一次决策尝试的观察采集、历史读库、决策、计划落盘、提交五段单调时钟耗时（`durations_ms` / `total_ms`），绑定 observation_id、计划摘要、实际策略和复用标记。它不包含外层轮询等待、游戏结算或计时行自身落盘；`dispatch_complete` 不等于全部指令写入/生效，须结合 submission 和意图状态。统计时分别报告取消、迟到、错误及复用，不能只选成功记录冒称全部请求的 p95；旧记录、强制结束或存储失败造成的缺失不能补零。计时不会传入模型历史或训练特征。

v0.2.13战斗观察不会等待可选的物品/宠物编号刷新；后台只读刷新继续，尚未返回的字段保留未知，不能据此假设模型所需数据齐全。`simulate` 每个指挥官首次决策还包含额外的 `BTIME` 校验请求，这部分也记在决策阶段；解读模拟器耗时时须注明，不能把它当作纯模型推理耗时。

训练方案对照须固定可执行程序、模型/环境和预算，保留版本及原始证据；开发期间有数值内核优化，也不要中途替换正在进行的对照程序。单个矩阵算子的耗时改善不代表整场决策或完整训练同比加速，须分别测量。

指挥程序的比赛记录保留各成员观察对应的事件流、游标和缺口；同一回合之后收到的事件不能用于此前决策。重连后的 stream 可能改变，即使 sequence 相同也不是同一事件。出现缺口时不能把历史描述成完整数据。

## 真实对战评估

```sh
sactl ai evaluate --environment ./environment.json --model ./ai-data/models/<摘要>.safetensors --output ./evaluation.json --matches 1000 --opponent basic --opponent focus
```

新建评估的 `--matches` 是**每个对手**的场数，必须为 8 的倍数：两个 seed × 两种配点归属 × 左右场地。不指定对手时比较 basic、focus、guard-break、defensive、sustain 五种固定策略；`--opponent-model <路径>` 增加冻结的模型对手。不要将模型输出概率称为胜率。

新实验默认 `pairing=roster-side-v1`，人物/出战宠物/备用宠物完整交换，热身与 PPO 也在八局周期内覆盖双方配点。实验 v2、checkpoint schema 3、评估 v3 与冠军注册表 v2 显式记录新格式。旧实验/断点/报告继续四局原语义，不能编辑字段改成新版本；新客户端使用旧实验时也继承旧赛程。旧四局只换场地，固定配点归属可能影响绝对胜率，须结合相同条件的基线解释。配点搜索仍固定待评分阵容并使用原四局规则。

评估会排除各模型用过的初始配置组，保持重复、换边、双方视角在同组。报告分别给出胜、负、真正平局和采集截断；截断不会计成平局。`completed_score` 是正常完成比赛的胜=1、平=.5 平均分，须结合截断数量解释；组数不足 20 时不输出 cluster bootstrap 区间。报告不会自动认证或晋级线上模型。

## 固定分组实验

正式训练比较先核对 `sactl ai experiment --help`，创建训练/验证/最终测试清单：

```sh
sactl ai experiment --environment ./environment.json --output ./experiment.json --mode 1 --train-groups 1024 --validation-groups 256 --test-groups 256
sactl ai train --environment ./environment.json --experiment ./experiment.json --data-dir ./ai-data --seed 1 --batch-matches 32 --batches 10
sactl ai evaluate --environment ./environment.json --experiment ./experiment.json --split validation --model ./ai-data/models/<摘要>.safetensors --output ./validation.json
```

新评估家族每个对手八场，256 组即每个对手 2,048 场；旧实验仍为四场。清单最多 4,096 个家族，固定规则、平台、场景、pairing 和三组配置；热身/PPO 只采集 train，验证和测试绝不混入训练。训练可选 seed 和学习参数，不能另外覆盖人数、点数、等级、回合上限或赛程。清单副本存入训练目录的 `experiments/`，恢复只传 `--resume`，不能换实验。绑定实验的模型必须用对应清单评估，评估时不能另外传 `--matches`、seed 或场景参数。

先用 validation 选模型，最终选择后才执行 `--split test`。该命令在对战前写入清单旁的 `.test-selection.json` 固定模型及对手集合；只允许同一选择中断重试，不允许换候选或删掉记录后继续用测试集挑模型。此约束不防手动复制清单，不代表完整实验认证。训练重叠会报错；重复、漏掉或改写评测计划中的比赛不能保存为完整报告。evaluate 本身不晋级，受控原生冠军晋级/回退由下文的 champion 流程执行；极少量流程测试仍不是胜率验收。

上述不带 `--experiment` 的评估用于未绑定实验的探索模型，不能当作已经事先保留了最终测试集。

## 固定策略的整数配点搜索

先确认安装版本支持 `sactl ai build-search --help`。该入口在隔离引擎中运行，不会给线上角色加点：

```sh
sactl ai build-search --environment ./environment.json --data-dir ./build-search-data --model ./model.json
sactl ai build-search --environment ./environment.json --data-dir ./build-search-data --resume
```

`--model` 固定战斗模型；也可用 `--policy basic` 等规则，不能同时指定。`--opponent` / `--opponent-model` 可重复，默认五个规则对手。模型和参数固定保存，恢复只能指定环境、数据目录及 `--resume`，原始模型文件无需再次可用。规则、平台或策略变化须另开实验。

人物、宠物分别保留每人同预算，默认各 120 点、等级 35、每项至少 1。多人模式按队员分别搜索，不能把队员点数互相转移。该受控预算与普通创建角色的 20 点规则不同，不直接转成线上创建/加点命令。

初始均衡/偏科/随机配点后，用整数转移产生候选；两层小评分器输入己方配点及备用宠物的已配置技能类别，筛选后的候选必须回原生引擎实测。搜索、验证、最终测试使用预先分开的对手阵容，验证选定后不允许根据测试结果改候选。默认设置、五个对手最多约 5,440 场；须遵守用户的资源预算，先用显式较小的 `--initial-candidates`、`--generations`、`--search-groups`、`--validation-groups`、`--test-groups` 验证链路。

任何采集截断都会保留数据并停止建议，不能当平局、败局或评分标签；需要在新目录增加 `--max-turns`。原始双方轨迹放在 `shards/`，固定模型放在 `search-policies/`，`search-objects/` 与 `search-states/` 复用不可变数据，`search-latest.json` 指向已提交进度。

`build-report.json` 是 `candidate-advice`，包含实测对照、配置和策略版本。`delta_from_balanced` 及至少 20 个测试阵容时的配对区间用于比较；区间跨 0 不能描述为稳定提升。建议不会自动应用或晋级。

## 配点与战斗策略交替训练

v0.2.13新增以下入口；先核对安装版本的 `--help`：

```sh
sactl ai build-pool --search-dir ./build-search-data
sactl ai experiment --environment ./environment.json --build-pool ./build-search-data/build-pool.json --from-model ./parent.json --output ./next-experiment.json
sactl ai train --environment ./environment.json --experiment ./next-experiment.json --from-model ./parent.json --data-dir ./next-training --batch-matches 32 --batches 10
```

池导出要求搜索完整结束且原始证据可验证，保留多个验证候选的完整队伍阵容，不根据测试结果再挑池。默认训练 n×(n+1)/2 个阵容对（含镜像），纯池模式不能把 `--train-groups` 设得更大。人数、点数和等级由池固定；验证/测试使用池外新阵容，排除祖先训练/选择/保留评测来源。

需兼顾更广的配点分布时，v0.2.13 `experiment --build-pool ... --pool-train-groups 6 --train-groups 32` 将六个池内家族与二十六个新生成家族一起冻结；必须显式给总数，池内数量须为正、小于总数且不超过池的阵容对数量。新家族双方均在池外，排除历史来源。新建清单均匀交错两类家族，保持每个家族八场配对完整；上述比例下每批 128 场包含 24 场池内、104 场新配点对战。旧清单及恢复仍按原顺序，不自动重排。此数量不是每批梯度权重或学习回合比例，小批次可能只有一类、少量批次可能未覆盖全部家族；不等于已证明更强。混合清单为 `commander-pool-mix-experiment-v1`，旧客户端拒绝，不能删字段或改 schema；该本地命令不通过游戏 daemon。跨人数组合仍用 `experiment-mix`，不要混淆。完整示例见随包 `learned-build-search.md`。

`--from-model` 必须与 experiment 中的父模型一致，使用父权重、新优化器，默认不再模仿热身；父文件保持不变。只用池从零训练或只用父模型微调也支持。恢复只用原训练目录、环境和 `--resume`，不重复传来源参数。下一代独立测试不能复用父实验的保留集合，换 seed 不会消除来源重叠。

旧绑定实验模型缺少保留评测来源时，用 `sactl ai export-model --data-dir ./parent-training` 从原 checkpoint 重新导出；默认新文件按摘要命名，不启动引擎、不训练、不改权重。只有旧模型文件而没有原训练目录时，不可手改元数据绕过。子模型引用祖先分片但不复制全部数据，保留源搜索、父训练目录和原始轨迹。

继续用 validation 选候选，test 只检验固定选择；下一轮搜索新建目录并固定新模型。这是显式交替工具，不是自动冠军晋级或已经证明更强的共同进化。

v0.2.13 `sactl ai build-validate init|run|verify|compare` 可补充比较“父模型/子模型 × 均衡/已选配点”，完整命令见随包 `learned-build-search.md`。`init` 在观察子模型成绩前绑定 `--search-dir`、训练 `--experiment`、准确父模型 `--from-model`，并冻结 `--groups` 个新对手阵容。`run --manifest ... --model ... --environment ... --output ...` 对父子模型各执行一次；中断仅重复原参数并加 `--resume`，保留报告 `.data` 与源搜索，不改变模型身份。所有子命令都需要 `--search-dir`。

用 `build-validate verify --report ...` 只读核验，`compare --baseline <父模型报告> --candidate <子模型报告>` 输出五项固定配对差值。统计按完整对手阵容聚合，少于二十个阵容不给区间，有任意截断则五项差值/区间均不输出。查看共同校正区间及 `unverified_source_artifacts`，不能把配点改善称为网络变强。补充报告不属于原实验最终测试，普通 `verify-evaluation`/冠军工具拒绝；不要改 schema 或把模型改标为另一个实验。此流程是本地工具，不通过游戏 daemon，不增加 Web AI 功能。

## 旧数据兼容入口

```sh
sactl ai train --database ./events.sqlite --output ./linear-v1.json
sactl ai evaluate --database ./events.sqlite --model ./linear-v1.json
```

这些是既有 v1 线性胜负评分模型和预测指标，不等于上述策略对战评估。`--database` 不能与原生环境/PPO 参数混用。只有配点与最终胜负的旧数据不具备完整逐回合观察、候选和概率，不能直接投入 PPO。

## 独立评估证据核验

### 本地原生冠军

v0.2.13 `sactl ai champion init --directory ./champions --experiment ./experiment.json` 冻结单一规则/平台、人数、预算、装备/宠物条件、回合上限、pairing 及门槛。默认五个规则对手，每个至少 256 配置组（新八局赛程首次至少 10,240 场；已有冠军时增加其 2,048 场，旧四局注册表保持原场数）。仍按配置组计算置信下界，不把八场当独立样本。创建不比赛，`champion challenge --directory ... --environment ... --experiment ... --model ...` 才消耗完整最终测试；先用 validation 选好候选，不拿 test 选模型。

门槛只在 init 设置，不能 challenge 时放宽。以配置家族计算单侧下界，按挑战次数和对手分摊 alpha；五规则下界默认均须超过 .5，旧冠军比较超过 .45。任何截断都不晋级。正常退出但 `result.assessment.passed=false` 是合法失败，不把退出码当通过。失败保留旧冠军；没有通过过的模型时冠军为空。

`champion status --directory ...` 重验原始证据和全部决策，events/event_ids 对应。`champion rollback --directory ... --to <已通过的事件摘要> --reason ...` 只恢复历史合格模型；`champion abandon --directory ... --reason ...` 放弃 pending 挑战，但保留其测试曝光和挑战编号。中断可用原参数重跑；不能换候选替代 pending。已使用/放弃的测试配置不能改 seed 重用。保留整个注册表，包括 evaluations 的报告与 `.data`、models、events、attempts、reservations 和 selections。

v0.2.13队伍逐场选模：在现有 team.json 设置 `schema_version: 2`、`strategy: "learned"`（或 hybrid）和 `champion_directory: "./champions"`，删除 `model`；相对路径从 team.json 所在目录解析，其他成员/状态目录/LLM 配置保留。先 `sactl ai check --config ./team.json` 只读核验，再按用户授权运行 `sactl ai run --config ./team.json --forever`。旧客户端拒绝 schema 2，不改成 1 绕过。固定 model 的 schema 1 仍兼容。

新排队前重验并选取原生冠军，包括回退结果；输出 selection 的完整评估条件和 `arena_certified=false`，不声称完整线上认证。为空、损坏、人数或服务器规则不符则报错，不悄悄用 basic 排队。进行中的挑战不会替换已提交冠军。排队前模型副本及选择信息原子写入 `state_dir/arena.sqlite3`，排队/倒计时/比赛中不换模型；重启使用保存副本，未知排队结果保留原请求和模型。恢复时保持原策略、登记册路径及 hybrid 的 LLM 配置，另起配置使用独立 state_dir；不能删除日志绕过版本锁。check 检查的是登记册当前结果，run 恢复已有会话时可能仍使用上一场固定版本。下一场无法重验登记册时停止排队。保留数据库，运行中的 Docker volume SQLite 不从 Mac 直接打开。

champion 命令只更新本地登记册；显式配置 `champion_directory` 的队伍才会在新排队前读取变化，固定 model 的队伍不受影响。完整线上执行认证及真实晋级模型的端到端选模仍待验收。核验耗时随历史增长，支持 Ctrl+C。先核对安装版本帮助，不把工作区开发命令当作已发布入口。

v0.2.13原生 `evaluate --output ./validation.json` 同时保存 `validation.json.data/`，包含首局前冻结的 spec、模型副本和双方压缩轨迹。一起保留或移动报告及同名目录，不只复制胜负 JSON。

### 恢复中断的原生评估

先核对安装版本的 `sactl ai evaluate --help` 是否支持 `--resume`。中断后保留原输出路径及 `.data` 目录，在完全相同的命令后加 `--resume`。程序先重建赛程并重放所有已保存比赛的策略动作，校验通过后复用这些比赛，只执行剩余赛程；普通调用遇到已有中断记录会拒绝，不从第一场重新打。模型、对手及其顺序、实验、人数模式、split 和场景参数都不能更换。

`evaluation_resumed` 事件给出 `reused`、`completed`、`total` 和 `remaining`；后续 `evaluation_game.completed` 包含已复用场数，`executed` 仅计本次新打的比赛。序号提交记录在 `.data/commits/`，不要编辑。旧版未保存序号的数据只能在轨迹能唯一映射到赛程连续前缀时恢复；有缺号、重复、歧义、损坏或缺失文件时会拒绝并保留数据。完整报告仍不可覆盖。恢复前先处理导致中断的资源或配置问题；此选项不会扩大已约定的实验预算，也不授权自动重跑。

`sactl ai verify-evaluation --report ./validation.json` 不启动引擎或联网；它重建独立赛程、核对原始轨迹并重新推理每条动作，验证后输出报告摘要。该结果仅说明证据一致，不代表模型更强、晋级或线上竞技场认证。没有完整证据的旧报告会被拒绝，不能手工补字段替代重新评估。

核验耗时随回合数增加，支持 Ctrl+C。中断评估不发布残缺报告，原始分片保留；支持 `--resume` 的版本按上述方式核验并续接原赛程，不重新执行已验证的比赛。不支持该参数的旧版本不能据此恢复，先升级并核对帮助。已有完整报告不覆盖。一次目录只允许一个写进程，不能删除锁文件强行并发。Hashes 校验本地完整性，不是服务端签名或 C 引擎重放。

v0.2.13 `sactl ai compare-evaluations --baseline ./before.json --candidate ./after.json` 会先核验双方报告及各自 `.data`，只比较同一冻结 experiment/validation、相同场景设置和完整对手集合；不筛选有利的共同对手，不启动比赛或 LLM。输出每个对手的 `paired_delta`（候选减基线，胜=1、平=.5）、按完整配置家族重采样的 `paired_ci95` 和 `bonferroni_bootstrap_interval`。后者只校正本次对手数量，不校正多次调参/多个候选；这些是近似 bootstrap 区间，不是自动晋级结论。

任一方存在截断时，该对手的 `censored_pairs` 保留，但配对均值/区间缺失；不得自行丢掉截断或把它当失败。少于 20 个家族不提供区间，见 `interval_unavailable`。不要用两侧各自完成局的 `completed_score` 替代缺失的配对差值。输出绑定报告/模型摘要并保留未知实战来源 `unverified_source_artifacts`；`arena_certified` 始终为 false，不修改模型或冠军登记册。整个核验可 Ctrl+C；错误或取消不输出部分比较。

## 训练对手调度

v0.2.13热身默认 `--warmup-batch-episodes 8`：每次按八条完整单方轨迹更新，32 场教师比赛的一轮共八次更新。保存的优化器步数决定轨迹打乱顺序，轨迹内部保持连续历史；取消不会提交半轮更新。设为 0 或旧 checkpoint 缺少此字段时保留旧全量单次更新。该参数不能在 resume 时改动，也不能用于旧 SQLite 训练。

v0.2.13新训练还默认保存 `ppo.update_guard=backtrack-v1`：每次更新后重算完整历史下的整队近似 KL，超过 `--target-kl` 就恢复更新前权重和 Adam 状态、减半学习率重试，最多减半八次。耗尽后停止本批优化，比赛与批次进度仍保留；同一学习状态不会重复占用历史对手池。查看 `post_update_kl`、实际 `learning_rate`、`backtracks` 和 `rejected_updates`；原 `approx_kl` 是更新前数值。此检查不保证模型强度。旧目录 resume 继续保存的旧算法，新保护需新建训练目录，不得改旧配置/摘要冒充兼容恢复。

新模式 `warmup_report.optimizer_updates` 记录实际小批更新次数；loss/gradient_norm 是各小批更新前、按动作数加权的统计，不是整轮冻结模型指标。只会模仿高频普通攻击也可能得到较高总体一致率，须分别检查治疗、道具和换宠，再用独立比赛评估，不以 loss 下降宣称更强。

v0.2.13新训练默认 `--opponent-sampling weakness-v1`。规则/历史/同代对手类别仍按 30%/50%/20% 采样（没有历史时归同代）；在规则与历史类别内部，用最近八批训练成绩提高困难对手的权重，保留容易对手的探索机会。截断不计成失败，权重只在完整批次提交后更新。历史池保留最近 16 个及至多 16 个较难的旧状态，不删除旧模型文件。

`sactl ai league --data-dir ./ai-data` 复核训练分片、实际对手身份、赛程和结果，输出训练矩阵及采样权重。它标记 `purpose=training-diagnostics`，不能替代独立 validation/test 或作为晋级证据。复核读取已提交批次的原始数据，大目录耗时随场次增长；保留 `shards/`、`learning/`、`league/` 和 checkpoint。

新目录可用 `--opponent-sampling uniform` 做旧调度对照；恢复不能修改模式。没有该字段的历史 checkpoint 保持旧随机赛程，`league` 对这种目录返回 uniform 和空矩阵。该参数不能和旧 `--database` 训练混用。先检查安装版本帮助，不把工作区开发命令当成已发布能力。

## 治疗与换宠教师

`sustain` 是本地训练/评估的固定规则教师，不是第五种在线 AI 策略。它以 focus 集火为基础，依据己方已知 HP、合法候选及已核实效果，安排单体/群体治疗或有限道具；队伍共享本回合的名义恢复计划，避免重复治疗。无法通过已计划治疗保住低血宠物时，选择已知可召唤、健康且有攻击技能的备用宠物。该回合的宠物指令仍属于原出战宠物。未知 HP、敌方、备用实体不会成为治疗目标；名义恢复量不代表引擎保证成功。

新实验默认五种热身教师和规则对手；可用 `--warmup-teacher sustain` 明确选择教师，并用重复的 `--rule-opponent sustain --rule-opponent focus` 选择 PPO 的规则对手池。这些参数随 checkpoint 固定，恢复时不能覆盖。历史 checkpoint 没有 rule_opponents 字段时继续原四种对手调度，旧教师身份和决策不变。评估与配点搜索通过 `--opponent sustain` 使用该对手，或 `--policy sustain` 固定搜索控制器；默认评估/搜索也包含第五种对手。已锁定的旧最终测试必须沿用原对手集合，不能用新的默认集合改写。

示例（v0.2.13，使用新的实验目录）：

```sh
sactl ai train --environment ./environment.json --data-dir ./sustain-training --reserve-pets 2 --healing-magic 20 --healing-items 6 --warmup-teacher sustain --rule-opponent sustain --rule-opponent focus --batches 10
```

这为热身和对手覆盖提供更多行为，不是 sustain 或 learned 的胜率认证。

## 隔离全链路检查时限

隔离全链路检查 `sactl ai simulate` 可用 `--timeout 10m` 设置包含登录、匹配、所有比赛及恢复的总时限。默认至少 10 分钟，超过五场按每场两分钟增加；不改变真实战斗回合时限。超时记录不能当通过证明，重新检查需新 work 目录。容器内外都要使用支持该参数的客户端，旧工作程序应明确拒绝。

`simulate --native-dir build/<原生目录>` 可选择独立的 `gmsv/gmsvjt.exe` / `saac/saacjt.exe`，默认 `build/local-arena/native`；目录限仓库 build 内、路径不含符号链接。数据与配置仍从仓库准备，模型仍须通过实际规则摘要校验。其他工具保持使用 `build/local-arena/bin`。保留旧引擎供旧模型恢复，使用新 work 目录；容器内的旧客户端不支持此参数时必须拒绝，不能忽略后换用默认引擎。

v0.2.13 `simulate --opponent-strategy learned --opponent-model build/models/baseline.safetensors` 可为另一队指定独立的 learned 指挥官，配合主队的 `--strategy learned --model build/models/candidate.safetensors` 做双模型完整链路检查。对手默认为 basic，也可选 explore；只有 learned 对手允许且必须传 opponent-model。两份模型各自验证观察/动作和人数契约，路径均限仓库 build 内且不得含符号链接；原始记录按 commander-0/commander-1 分开保存。新参数会传给容器工作程序，旧程序必须拒绝，不能忽略后与 basic 对战。这是隔离测试入口，不接管用户已有会话；少量比赛不证明策略更强。双方 explore 使用声明的 seed，但不据此宣称整个引擎可跨运行复现。

多人不同配点用重复的 `--member-allocation`（数量等于 `--mode`），双方同位置使用同一配点，每个人物独立满足普通创建角色的 20 点预算。模拟器在开赛前核对公开角色属性并保存 `initial-roster.json`；不完整或不符时停止。旧版模拟器曾把交替分队的账号序号错误映射到配点位置，不同成员配点的旧多人结果须重跑，不能作为对称阵容验收。默认或统一 `--allocation`、离线原生训练数据不受这一问题影响。

v0.2.13 `simulate --fixture-level 35`（1..140，默认 1）用于高等级完整链路 fixture：正常创建 20 点角色后，关闭全部隔离客户端/服务器，只调整本次 work 内新建合成人物的离线存档等级及配点，再重启登录并核对公开属性。按创建配点比例以最大余数法分配 `20+3*(等级-1)` 点，双方同位置一致；不改变宠物、技能、引擎或线上人物。保留 `original-profiles`、`prepared-roster.json` 和实际 `initial-roster.json`，不将预置存档描述为经过真实练级。

边界覆盖可要求 `--require-withdrawal`（多人，原历史观察者离场后有新的指定策略计划）和 `--min-battle-turns 20`（至少一场结算达到指定决策回合数）。只有实际记录满足才生成通过文件；不能把普通短局成功当成离场/长局验收。它们不改变游戏规则或强迫模型选择动作。旧容器工作程序须拒绝未知参数，不能略过后跑默认一级角色。

learned 的历史重建仍从保存的观察和事件截点开始，旧回合仅恢复记忆，当前回合生成计划。该优化不改变模型格式或免除历史完整性检查；缺口仍会明确回退。纯网络或历史重建基准不包含数据库、网络和指令提交，不能作为整轮截止时间验收。

## 导出历史训练阶段

比较训练阶段时可用 `sactl ai export-model --data-dir ./training --checkpoint <摘要>` 导出已提交的历史权重；摘要来自该目录 `checkpoints/<摘要>.json`，不是 learning state 或模型摘要。省略时仍导出 latest。完成至少一轮模仿即可导出候选，只有采集记录不能导出；纯模仿模型没有 PPO 训练报告。该命令核验来源且不回退训练进度，不编辑 latest、checkpoint 或原始分片来挑模型。候选沿用冻结实验及数据隔离，必须另外评估；旧客户端不支持该参数时会拒绝。

v0.2.13原生模型导出（含训练结束后的自动导出）在历史核验时支持 Ctrl+C；核验取消不会发布候选或改变已提交训练数据，可从同一目录重新导出，不需要重训。长历史核验期间暂时没有输出不代表进程已停止，应先核对原进程状态。

纯模仿候选的教师轨迹核验同样在各分片及逐回合规则重放间检查取消；正在读取的单个压缩分片会先读完，不承诺按下 Ctrl+C 后立即返回。取消不跳过来源校验，也不把未核验模型作为成功结果输出。

## 跨人数初始化训练

v0.2.13支持 `experiment --from-model ./model-1v1.safetensors --mode 2` 创建多人训练实验，再用 `train --experiment ... --from-model ./model-1v1.safetensors --data-dir ./new-training` 初始化权重。必须新建实验和目录；规则/平台/架构仍须兼容，保留祖先数据隔离，使用新优化器。需要模仿热身时显式传 `--warmup-matches`，因为继承权重默认关闭热身。

迁移仅用于学习起点。父模型不增加适用人数，子模型只声明实际训练的新人数，且仍需独立验证。不要直接把 1v1 模型放进 2v2/5v5 队伍配置，也不要编辑模型的 modes 绕过加载检查。每个模式保留自己的已验证模型；不能因为网络能接收多人输入，就宣称多人战术有效。

## 可选控制教师

`control` 是训练/评估规则，不是在线第五种策略。它继承 sustain 的治疗、道具和换宠安排，协调集火与状态攻击：同一目标每回合最多一次状态尝试；避免对已有异常状态、未知 HP 或濒死敌人施加控制；石化/睡眠避开本回合计划攻击的目标。实际成功率、出手顺序和持续时间由引擎决定。

v0.2.13可显式使用 `--warmup-teacher control` / `--rule-opponent control`，或 `evaluate --opponent control`、`build-search --policy control --opponent sustain`。重复参数可组合多个规则。默认仍为原五种教师/对手，冠军门槛也不变；恢复时不能修改已保存的池。新教师需要独立效果验证，不能因模型模仿成功就声称胜率提升。

v0.2.13新增显式规则基线 `independent-control`，可用于同样的教师、规则对手、评估与配点搜索参数。每个成员看到完整公共实体、状态和候选信息，只协调自己的人物与宠物，不共享其他成员本回合的治疗、换宠和控制目标预留；仍能治疗受伤队友。1v1 与 `control` 行为相同，多人可能重复治疗/控制。它不是新增在线策略，也不是独立训练的模型，不宣称 learned 已胜过该对照。默认对手集合不变，已冻结实验不能临时追加它；应另行冻结比较并保留其独立规则身份，规则轨迹不能当作 PPO 在策略样本。

```sh
sactl ai experiment --environment ./environment.json --from-model ./candidate.safetensors --mode 2 --train-groups 8 --validation-groups 32 --test-groups 128 --output ./coordination-experiment.json
sactl ai evaluate --environment ./environment.json --experiment ./coordination-experiment.json --split validation --model ./candidate.safetensors --opponent control --opponent independent-control --output ./coordination-evaluation.json
```

示例人数须与现有模型支持的模式一致；单独实验将该模型声明为精确父模型，沿用其训练/选择/保留来源排除。绑定实验的模型不能省略兼容实验直接评估，也不能删除来源字段绕过。这里不启动新训练，不使用最终 test。

```sh
sactl ai train --environment ./environment.json --data-dir ./control-training --reserve-pets 2 --healing-magic 20 --healing-items 2 --warmup-teacher control --warmup-teacher sustain --rule-opponent control --rule-opponent sustain --rule-opponent focus --batches 10
```
## 公共战斗承伤者与模型版本

公共战报的 `recipient`/`guardian` 为可选战场槽位，语义见 [战斗观察](battle.md)。`commander-observed-v7` 按实际作用对象编码伤害/吸收，并保留保护者；缺失时明确未知，不用原目标补齐。v6 模型保持原编码，守护/反射承伤归属仍计入未知。网络配置 `input_schema` 和模型摘要绑定版本，不能重标旧权重；当前支持 v6/v7/v8，各自按声明版本观察；v8 保留 v7 历史语义并加入忠犬动作，要求 v8 原生环境。新增输入/动作不等于模型已学会对应战术，反射技能与策略强度仍需扩展和验证。

## v8 主宠技能配置

v0.2.13 train、experiment、evaluate、build-search 可用 `--pet-skills 1,2,3,60,80,110,20` 指定双方主宠技能集合，最多七项且不重复。20 为忠犬，攻击目标并保护自己的主人，名义攻击力 -20%；不是选中队友保护。支持的其他 ID 为 1 攻击、2 防御、3 破防、60 毒、80 石化、90 混乱、110 催眠。默认省略保留原七技能。备用宠物带攻击、防御及从所选集合抽取的一个专长，没有专长时仅带攻击、防御。

显式技能集要求 `controlled-battle-v8` 工作程序，实际槽位按固定词表压紧，不扩大原生七槽容量。新训练为 `commander-observed-v8` / `attack-guard-switch-guardian-v7`；原 v6/v7 恢复仍使用原输入/动作及匹配的程序和规则，不自动升级。技能集合随实验、配点池和 checkpoint 固定，绑定实验或 resume 不允许覆盖。旧客户端会拒绝新参数，未发布的本地工作区能力不能当成下载区现有能力。此配置只作用于本地受控训练，不修改线上宠物技能。


## 模型查找与实时输出（v0.2.17）

已进入角色的当前会话可用 `sactl ai run --strategy learned --matches 1`，无需 profile 或模型参数（存在唯一可用本地模型时）。查找顺序为当前目录、当前目录下 `runtime/ai-models/`、`$XDG_DATA_HOME/sactl/models/`（默认 `~/.local/share/sactl/models/`）。自动索引只查各目录直接包含的 safetensors，并校验人数兼容性；同一策略身份副本去重，第一层有多个模型时停止并列出路径，不擅自选择。旧 JSON 仍可显式指定。`--model filename` 沿上述目录查找，含目录的显式路径只读取该路径；配置文件中的模型路径仍相对配置文件。

默认 `ai run` 输出可读的匹配、回合决策、模型评分、动作目标、提交状态和服务器战报；脚本必须使用 `sactl ai run ... --json` 获取 JSON Lines，不能再假定默认输出是 JSON。`ai check` 仍输出 JSON。预期回报不是胜率，“指令已发送”不是技能生效证明；以随后服务器战报为准。多人显示首个受控成员的战报，各成员训练记录照常保存。显示信息不会改变决策、历史、预留或重试规则。

“已加入竞技场匹配” / `state=queued` 表示已自动排队，不再发起第二次匹配。等待时每 15 秒提示；不因暂未找到对手而重启 daemon、删除状态或另起一个指挥官。
