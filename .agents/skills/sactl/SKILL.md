---
name: sactl
description: 使用 sactl 命令行客户端在石器时代游戏中观察角色、战斗和聊天，执行移动、对话、战斗及其他已授权游戏操作。适用于 Codex、Claude Code 等 Agent 操作玩家指定的游戏会话；不用于服务器部署、管理端操作或开发内置 AI 运行时。
---

# sactl 游戏会话

配套客户端版本：v0.2.13。四种本地 AI 策略、完整大模型上下文、最终计划恢复与候选模型限制见 [本地 AI](references/ai.md)。升级时同时更新本 skill 和引用文档。

通过已安装的 `sactl` 操作用户指定的角色。此 skill 是操作说明，不包含大模型、账号或自主运行服务。

v0.2.13 `ai run` 在已有提交或预留后发生策略错误时，不再生成 basic 剩余计划。遇到 `strategy_rejected` 保留原 `state_dir`，按[本地 AI 恢复说明](references/ai.md)核对原因，不删除历史或预留来强迫重算。

本地 AI 工具不迁入 Web。v0.2.13 learned/hybrid 也可加载文件 schema 5：它保留实战模型参与配点搜索的间接来源，即使新策略从零训练也不能丢弃。使用前核对客户端是否支持，具体来源与评估限制见 [本地 AI 训练](references/ai.md)。隔离双模型执行检查可用 `simulate --opponent-strategy learned --opponent-model`；需容器内外客户端都支持，不把少量功能对局当作胜率认证。

v0.2.13 `ai compare-evaluations --baseline <报告> --candidate <报告>` 只读核验两份冻结验证集报告及原始证据，再输出配对得分差和区间；不自动晋级，不把截断或少量样本解释为胜率提升。参数及解释见上述训练引用。

需要区分配点与战斗决策收益时，v0.2.13 `ai build-validate` 提供父子模型的补充验证；它使用独立清单和报告，不能交给普通冠军流程。创建、恢复、来源保留和统计解释见 [配点与策略交替训练](references/ai.md#配点与战斗策略交替训练)。

v0.2.13 `ai collect-feedback` 用冻结本地模型采集训练场景，`ai train-feedback` 学习单独保存的规则建议；它们不是在线竞技场命令。先核对版本帮助、完整采集状态和资源预算，恢复不覆盖数据/参数；操作顺序与边界见 [规则反馈训练](references/ai.md#规则反馈训练)。

v0.2.13训练支持实验参数 `--opening-rollouts` / `--policy-advantage gae|opening-loo`，默认仍是单次采样 GAE；只对完整终局执行重复开局更新，原生训练目录可恢复，旧的独立 pilot 目录不可恢复。尚未证明稳定强度提升；按引用文档和实际 `--help` 区分v0.2.13与已发布客户端。

v0.2.13 `train --initial-policy-scale` 可在声明父模型的新原生训练中一次性缩放策略输出层，默认 1 保留原权重；不是运行时温度，也未证明胜率提升。恢复时不重复传入，适用模式和来源验证见 [父模型探索初始化](references/ai.md#父模型探索初始化)。

v0.2.13 `train --plan-scope member` 可训练离线独立成员对照，默认 team 仍为统一指挥。对照模型仅供离线评估，不能用于 learned/hybrid 联网指挥；不能修改旧模型标签或在恢复时覆盖架构。信息共享及公平比较条件见训练引用。

v0.2.13 `train --plan-features target-counts-v1` 可显式训练带计划目标计数的新架构，默认 none 不变。它不是强制避开重复目标的规则，也未证明提高胜率；从旧父模型迁移或恢复时不能覆盖该配置，详见训练引用。

## 接入

v0.2.13 `ai experiment-mix` 与 `ai train --mixed-experiment` 支持同一指挥模型跨模式训练；评估必须逐模式保留完整复合实验，不能改写成单模式产物。`champion init/challenge --mixed-experiment` 共享全部模式的统计预算，全部通过才晋级，完整挑战场数较大。恢复、模型导出、场数及参数约束见 [混合模式训练](references/ai.md#混合模式训练)。

两个复合实验有不同来源、但故意使用相同完整分组时，通过 `ai experiment-compare` 声明共同验证集，再用 `evaluate --validation-comparison` 比较；不要改写模型编号或删除留出来源。仅支持 validation，不用于晋级；见 [跨实验共享验证集](references/ai.md#跨实验共享验证集)。

v0.2.13模仿训练可显式选择 `sqrt-action-frequency-v1` 动作频次加权，默认仍为 `none`；原生热身使用 `--warmup-action-weighting`，示范及反馈训练使用 `--action-weighting`。权重只从冻结训练数据计算，恢复时不可覆盖，不用于 PPO；见 [动作频次加权](references/ai.md#动作频次加权)。该实验选项尚无胜率改善证明。

v0.2.13原生评估中断后须显式使用 `evaluate --resume`，校验并复用已完成比赛；参数、模型和赛程保持不变。先处理原中断原因，不自动扩大预算。版本要求、结构化进度和旧数据限制见 [恢复中断的原生评估](references/ai.md#恢复中断的原生评估)。

- 先用 `sactl version`、`sactl --help` 确认实际版本和可用命令。战斗日志要求 v0.1.91 或之后支持该命令的版本。
- 用户指定 `--profile`、`--config` 或 `--socket` 时，每条命令沿用同一选择。不要自行切换账号、角色、线路或传输地址。
- v0.2.3 起，用户直接运行 `sactl login` 隐藏输入密码，无需配置文件或 `init`，默认使用客户端内置的公开游戏网址；登录凭据只留在后台进程内存。`status` 不触发登录，v0.2.8 起 `logout` 默认原地登出并清除凭据，回记录点必须用 `logout --record-point`。已发布的 v0.2.7 仍须显式用 `logout --in-place` 才能原地登出；先核对版本。
- `sactl --json observe` 获取观察；守护进程未启动时参阅 [连接与结果处理](references/session.md)。`sessions` 查看会话，`use <名称>` 切换默认会话；脚本和并行 Agent 显式固定 `--profile`。
- sactl 自己持有登录会话，不会接管浏览器会话。同一角色不能同时供浏览器与 sactl 登录。沿用用户授权的角色，不为了接入而抢占其他会话。
- 不打印配置中的密码，不把凭据放进命令参数、skill 或提交文件。HTTP 传输连接用户提供的 Web 地址，不需要 SSH。

## 观察、操作、核验

1. 用 `sactl --json observe` 读取当前 `data`。现有观察字段保持 Go 名称大小写，如 `Phase`、`Revision`、`Battle.CommandReady`；不要假设它们是小写 JSON 字段。
2. 根据用户目标和当前状态选一个有明确参数的操作。命令成功表示该命令的返回语义成立，不代表整个游戏目标完成。
3. 用返回的结构化数据和后续观察核验变化。需要服务端事件时用 `sactl --json wait 30s`，之后重新观察；`wait` 超时本身不代表任务失败。
4. 串行执行同一角色的动作。完成目标就汇报；用户要求持续活动时，循环必须保留停止条件，不能由 skill 擅自增加永久运行目标。

物品操作使用当前 `Inventory[].Index` 槽位，宠物操作使用 `Pets[].Slot`。物品模板编号见 `AI.Items[].TemplateID`；支持合并输出的版本也有 `Inventory[].TemplateID` / `TemplateIDKnown`。宠物跨会话身份使用已确认的 `StableID` / `IdentityKnown`。槽位、图片编号 `Graphic`、场景对象 `ID` 都不能当作唯一实例身份；当前协议不提供背包物品唯一实例 ID。v0.2.8 起登录后及物品/宠物身份变化后自动获取扩展状态，装备和背包均纳入编号范围（装备编号需新版服务端）。`query AI` 是诊断用的服务器状态请求，不调用大模型；v0.2.9 起 `query --help` 可离线查看全部代码及用途，范围见[手动状态查询](references/session.md#手动状态查询)；服务端尚未返回或确实未知时仍保留 unknown，不用旧槽位推断。聊天 `Channel="P"` 是原始协议类型，不能据此判断为私聊；文本已包含服务端发送的说话者名称，不再次拼接内部身份 ID。

读取 JSON 的 `ok`、`kind`、`error`、`data`，不要解析 `text` 来决定动作。遇到 `kind="unknown"` 或提交后连接中断，结果可能已生效：先观察核实，不能直接重发交易、邮件、物品操作等动作。无法核实时报告未知并停止该动作链。具体语义见 [连接与结果处理](references/session.md)。

## 战斗

按需阅读 [战斗观察与指令](references/battle.md)。

- `sactl --json observe` 提供回合和人物/宠物指令就绪状态。
- `sactl --json battle-log` 提供与 Web 战斗面板同源的参战快照和可读/结构化日志。它不是另一个战斗控制状态机。
- 换宠使用已观察的 `switch_pet` 候选；本回合宠物命令仍属于原出战宠物，结算后再确认新宠物。槽位语法、缺失状态及收宠限制见战斗引用文档。
- 协议缺失的信息不能推测为已知：宠物没有气力；普通攻击日志不一定有技能名；快照不保证是动画每一帧的即时数值。

## 其他操作与能力边界

- v0.2.13竞技场：`arena contacts` 的结构化名片同时含 `slot` 和 `id`；用 `arena invite <slot> <id>` 邀请选中的角色，不单独缓存数字槽位。组队、匹配、战斗、结算和重连命令及尚未验收的范围见 [竞技场玩家流程](../../../docs/arena-player-flow.md)，实际客户端/服务端版本须支持这些命令。
- 地图：`walk`、`goto`、`warp`、`exits`、`encounters`。除单步移动外，多项导航依赖本地 2.5 地图数据；缺数据时不要编造路线。
- NPC：观察 `ActiveWindow`，使用 `talk`、`choose`、`reply`；选项、目标和坐标来自当前观察，不来自臆测。
- 背包、宠物、邮件、队伍、交易：先查 `sactl --help`，再调用对应命令。出售/丢弃、转账/交易、删角色以及对外聊天/邮件必须处于用户授权范围；用户已有授权无需反复确认。
- 原始 `send` 是诊断后备接口，不是缺少高层能力时的默认替代。不能仅凭 Web 按钮猜测原始包参数。
- Web 与 sactl 的全部功能尚未证实对等。当前 CLI 有 `auto-battle`，不能假定它也有自动任务、自动练级等同名高层命令。缺少入口或结构化结果时明确说明差距，不宣称任务成功。
- 普通游戏接口不提供 AI 的长期记忆或任务调度；协议保活由客户端自动维护。本地 `sactl ai` 指挥程序另有已授权的模型策略入口。本地 Agent 可以在用户授权范围内管理这些能力，游戏操作仍通过 sactl 及服务端权限校验。
- 本地模型训练与真实对战评估按需阅读 [本地 AI 训练](references/ai.md)。先核对安装版本的 `sactl ai train --help`；分组实验还需支持 `sactl ai experiment`，使用 validation 选模型、test 仅检验最终候选；`sactl ai build-search` 固定战斗策略搜索合法整数配点，只产出建议。交替训练使用 `build-pool`、`experiment --from-model` 和 `train --from-model`，旧 checkpoint 可用 `export-model` 重新导出，v0.2.13 `--reserve-pets 0..2` 为每人添加同预算备用宠物，绑定实验或恢复时不能另行覆盖；`sustain` 是训练/评估用的治疗换宠教师，可选 `--warmup-teacher`、`--rule-opponent` 或 `--opponent`，不是新的在线策略；具体来源/恢复限制见引用文档。新的原生 PPO 入口与旧 SQLite 线性训练的输入不同，不能混用，也不能把候选模型或训练 loss 当成已验证的竞技场胜率。
- v0.2.13 `sactl ai league --data-dir` 核验训练对手矩阵，不能替代独立评测。原生评估保留报告及同名 `.data` 目录，用 `sactl ai verify-evaluation --report` 核对赛程、模型、轨迹和策略动作；核验成功不等于晋级或强度认证。`sactl ai champion` 管理固定门槛的受控原生晋级/回退，失败保留旧冠军，放弃和回退不重置测试曝光；它不自动切换线上队伍或授予完整竞技场认证。调度兼容、中断重跑与数据保留规则见 [本地 AI 训练](references/ai.md)。
- v0.2.13 `sactl ai export-data` 可只读导出旧 battle-records 为配点/逐回合 JSONL，不需要 Python 或游戏登录。输出必须是新文件，检查结构化报告中的无效/过滤计数；旧记录缺少行为概率，不能冒充当前 PPO 数据。格式与示例见 [旧战斗记录导出](references/ai.md#旧战斗记录导出)。
- v0.2.13 `sactl ai import-demonstrations` 可从已停止并完成 checkpoint 的本地队伍 SQLite 重建模仿样本；`train --demonstrations` 可冻结数据并进行可恢复的本地模仿训练，完成后用 `export-model` 导出有实际来源的 schema 3 候选。用声明同一父模型的 experiment/train 可接续原生 PPO，schema 4 子模型仍保留实战来源；新客户端 learned/hybrid 支持两者。评估的 unverified_source_artifacts 不是可忽略的提示，非空时不能据此自动晋级。不要打开容器仍在写入的 SQLite/WAL；导入、训练不等于模型晋级，示范 checkpoint 也不能冒充在线模型，完整使用边界见 [本地比赛示范导入](references/ai.md#本地比赛示范导入)。
