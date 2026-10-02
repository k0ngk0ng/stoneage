# learned v0.2.10训练入口

当前实现了 Go CLI 的原生采集、规则模仿热身、实战示范训练/导出、PPO 批训练、恢复、冻结分组实验、对战评估及固定策略下的整数配点搜索。受控场景覆盖人物/宠物攻击、防御、破防、状态攻击、两种治疗装备、小块肉、多宠切换及忠犬攻击。配点池可以用于下一轮策略训练；这些能力已分别执行验证，不代表模型已掌握有效战术。在线 learned/hybrid 已接入 v2 架构加载和历史推理，learned 已有完整 1v1、2v2、3v3、5v5 执行证据；带实战来源的原生评估会明确保留未知配置重叠。工具包随 v0.2.10 发布；全部游戏动作覆盖与策略强度验收仍未完成。详见 [实现记录](learned-implementation.md)。

## 混合模式训练

模仿阶段另提供实验性动作频次加权：原生热身参数为 `--warmup-action-weighting sqrt-action-frequency-v1`，示范和反馈训练为 `--action-weighting sqrt-action-frequency-v1`，默认均为 `none`。对完整冻结训练集的角色/动作类别计数 n，以 `sqrt(N/n)` 为原始权重，再归一化到所有示范动作的平均权重为 1。权重不读取胜负、验证/测试集或当前小批次；模型结构、输入和 PPO 目标均不变。报告区分普通交叉熵与加权目标，并保留计数和权重。恢复不允许覆盖，数据或报告不一致则拒绝；未开启热身、混合 PPO 和旧 SQLite 入口拒绝不适用参数。固定 5v5 对照已完成：六规则对手的校正差值区间均含零，加权模型直接对普通模型为 112 胜 / 256 场，尚不支持将该方法作为强度改进采用；低频动作拟合改善不能作为晋级依据。完整范围与恢复流程的状态快照例外见 [实现记录](learned-implementation.md)。详细入口见配套 skill 的“动作频次加权”。

混合模式训练使用单一网络与 Adam 状态，每批对各模式重新采集，再按显式模式权重更新。v0.2.10入口为 `experiment-mix`、`train --mixed-experiment`、自动识别目录的 `train --resume` / `export-model`。评估用 `evaluate --mixed-experiment ... --mode N`，每份报告保留完整复合实验及当前模式子实验，`verify-evaluation` 重放双方策略；`compare-evaluations` 只比较同一复合实验、同一模式。具体可执行示例见配套 skill 的混合模式训练说明。

不同来源的混合候选若故意共享完全相同的家族、顺序和 train/validation/test 划分，使用 `experiment-compare --left-experiment LEFT --right-experiment RIGHT --output comparison.json` 明确声明，再用 `evaluate --validation-comparison comparison.json --mode N --model LEFT_MODEL --opponent-model RIGHT_MODEL --environment environment.json --output report.json` 比较。仅支持 validation 和唯一右侧模型对手；模型须分别匹配各自实验，不能改写实验编号、互换模型、覆盖场景或释放最终 test。训练/选型来源仍排除，报告及原始证据绑定完整声明；`verify-evaluation` 重放双方，配对报告还须使用相同的比较声明。这是显式共同验证集，不是同等训练预算、协同优势或晋级证明。普通评估继续排除其他实验的留出家族。

每个模式的采样家族数与损失权重分别冻结；每个家族含 8 场。当前混合训练为直接 PPO，不支持单模式热身、示范或重复开局参数；规则对手均匀抽样，历史对手来自本次混合训练的已接受更新。schema 6 产物必须包含覆盖全部声明模式的已接受更新、实际数据量及训练/选型/留出来源。历史检查点导出不移动当前进度。最终 test 在实验旁固定整个候选模型，再固定各模式对手。`champion init/challenge --mixed-experiment` 联合验收全部模式，共享挑战次数和误判预算，全部通过后才替换一个共享模型；具体场数及恢复/回退见 learned-champion.md。跨模式强度/保留能力与正式工具包仍待验证。此功能不改变 Web 界面。

## 原生评估断点恢复

原生评估中断时保留 `<output>.data`，使用完全相同的 `evaluate` 命令加 `--resume`。恢复先验证冻结模型、完整赛程前缀及双方策略动作，再执行剩余比赛；不加该参数时拒绝已有中断记录。每场在分片保存后写入不可变的序号提交记录，完整报告只在全赛程结束后发布。旧版无序号的数据须能唯一对应连续前缀；缺号、重复、歧义或损坏均拒绝，不通过重打修补。结构化输出区分复用场数与本次新执行场数；具体字段见配套 skill 的“恢复中断的原生评估”。此能力不自动重启资源超限的实验，也不扩大预算。

## 规则反馈：采集后再训练

开发中的原生环境适配器在正常关闭时发送 EOF，等待 worker 排空归档并退出，最多等待 10 秒；超时或非零退出码会使调用失败，多采集进程分别等待并汇总关闭错误。取消或失败的战斗请求仍会终止对应进程，不能把这种退出当作归档完整。训练已提交的 checkpoint、分片及评估记录保留，关闭失败不会自动删除、重跑或发布候选；须分别核验 Go 训练数据与底层战斗归档。

这一行为需要匹配的v0.2.10 CLI 和原生 worker：新 worker 的离线日志队列会等待写入，多个 worker 共用目录时各用自己的状态临时文件。`status.json` 只代表最后报告的进程，并非全部 worker 的健康汇总。旧 worker 可能忽略写入失败，旧 CLI 可能直接终止进程；更新源码不等于现有安装或正在跑的实验已更新。

对于直接的 `docker run` 引擎配置（包括 `environment init` 生成的配置），每次启动还会分配唯一的容器归属标记。关闭时用独立的最多 5 秒清理窗口核对并移除本次启动的容器，避免只结束 Mac 上的 Docker 命令、却留下容器继续运行；不会删除 volume 或清理其他容器。归属不明或 Docker 检查失败会明确报错。标记 `org.stoneage.training.worker` 保留给程序使用，不能在配置中覆盖。自定义 shell、SSH 或带全局参数的 Docker 包装命令须自行处理远端进程生命周期，本地程序不会猜测其容器归属。

v0.2.10提供 `sactl ai collect-feedback` 和 `sactl ai train-feedback`，用于在模型实际遇到的局面上学习规则老师建议。真实执行的动作、概率、价值、奖励和后续观察保持不变；建议单独存储，不冒充执行记录或 PPO 样本。老师只读取当时公开观察，不能使用敌方隐藏配置或未来结果。这是可选监督训练方法，尚无可靠胜率提升证据。

先准备已验证可运行的 `environment.json`、冻结的 `experiment.json` 及该实验候选（或精确声明的父模型）。模型必须支持实验的人数、规则、平台与特征。采集只运行实验的 train 家族，完整保留双方原始轨迹；每场仅标注模型一方。

```sh
sactl ai collect-feedback --environment ./environment.json \
  --experiment ./experiment.json --model ./candidate.json \
  --data-dir ./feedback-round1 --matches 32 --teacher sustain \
  --opponent basic --opponent sustain --workers 2
sactl ai train-feedback --collection ./feedback-round1 \
  --data-dir ./feedback-training1 --epochs 4 --output ./feedback-model1.json
```

采集目录和训练目录都须为新路径、父目录存在。采集需要本地兼容原生 worker；训练和导出只运行 Go，不启动 Docker 或登录游戏。环境参数也接受 `STONEAGE_TRAINING_ENVIRONMENT` 默认值。默认采样网络动作，显式 `--greedy` 才使用贪心；默认 seed=1、teacher=sustain、opponents=basic/sustain、matches=32。场数最多 256，须为实验完整配对家族的倍数并至少覆盖每个对手一个家族，当前实验通常每家族八场；旧实验保持既有方式。

采集恢复继续原来总场数，不重复指定模型、实验、老师、对手、seed 或 matches。允许调整 workers（1..8）、环境启动文件和磁盘停止阈值，但引擎规则/平台/场景必须一致。完成后重复恢复不启动 worker：

```sh
sactl ai collect-feedback --environment ./environment.json --data-dir ./feedback-round1 --resume
sactl ai train-feedback --data-dir ./feedback-training1 --resume --epochs 2
```

训练新运行默认从最后一个 `--collection` 的模型开始，也可显式 `--model` 指定同实验兼容初始化；新 Adam 从零开始。默认每次一轮、batch-episodes=8、sequence-length=16、learning-rate=.001、gradient-clip=.5。重复 `--collection` 按顺序聚合既有轮次，CLI 当前合计最多 256 场；未完成采集、重复视角（包括换标签）、跨实验或混合特征拒绝。训练恢复从保存的 Adam 继续，只读运行目录中的冻结副本，不依赖外部采集目录，不能覆盖参数或换数据。

后续反馈轮次显式用新候选采集、新 seed、新目录，再以新目录训练并按需聚合历史 collection；不要改旧检查点或标签。采集器逐场提交，模型采样随机流和换边赛程固定；一、多个 worker 与中断恢复的数值结果一致，运行时 match/stream 标识可以不同。目录使用系统文件锁，不能删锁文件并发写入。

两命令的 `--stop-at-data-bytes N` 在完整检查点后统计目录内普通文件逻辑长度，含挂载 volume 内的文件；0 禁用。达到后保留现场、输出 `storage_limit_reached` 及 `storage_trigger_stage`，失败退出且不导出候选。阈值不是硬配额，可能超出一次写入；恢复可调整，不自动清理。Ctrl+C 失败退出，报告最后指针是否存在；恢复时再校验全部证据。初始化失败可能无指针，保留原目录并换新目录。

训练成功后才输出 `candidate_saved`；`sactl ai export-model --data-dir ./feedback-training1` 可重新导出，`--checkpoint` 选择历史完整轮次而不倒退训练。不同内容不会覆盖旧模型。导出沿用 native 推理 schema，并用 `commander-feedback-export-v1` 报告绑定初始化、反馈数据、采集模型、各轮权重和优化器来源；采集策略影响访问的局面，其既有训练配置组也必须保留。既有 recorded/recorded-selection 来源及不确定性继续传递。

候选仍须使用原实验的 validation 对战评估及 `verify-evaluation` 核验，未自动晋级或扩展支持人数。最终 test 继续保留给预先冻结的最终选择；不能把验证对战标注后混入训练。

## 可选计划目标计数架构

`train --plan-features target-counts-v1` 为新建原生训练启用实验性 `commander-policy-v3`；默认 `none` 保持旧 v2。网络除计划 GRU 外，还按当前候选读取已规划友方动作的八项计数，细节见特征契约。这些是意图，不是已结算的伤害、控制或治疗；不禁止重复目标。默认宽度下新增 512 个可训练参数，共 198,466 个。新增投影从零初始化，同 seed 的原有参数及初始输出不变，不表示训练结果会相同或更强。

```sh
sactl ai train --environment ./environment.json --data-dir ./counts-training \
  --mode 2 --plan-features target-counts-v1
sactl ai train --environment ./environment.json --data-dir ./counts-training --resume
```

该示例仅说明入口，不构成足够训练预算或强度推荐。配置保存在 checkpoint 的 `network.plan_features` / 导出模型的 `network.config.plan_features` 并参与模型和训练身份；恢复不能覆盖，`--from-model` 继承父架构并拒绝矛盾的显式值，不给旧权重自动加层或重标。新架构可用于新版本地 learned/hybrid，但仍须符合原模式、规则和来源检查；旧客户端拒绝新参数或模型。示范训练 CLI 和旧 SQLite 入口不接受该参数。结合 `--plan-scope member` 时架构为 `independent-member-policy-v2`，新增计数只包含本成员计划，继续拒绝联网指挥。对照实验须匹配来源、初始化、训练数据和预算，并另行评测完整比赛；拟合改善不等于竞技场胜率改善。



## 使用

旧版服务端 `battle-records` 的 Go 导出入口见下文“旧战斗记录”。它输出观察和结果样本，不把缺少行为概率的旧日志转换成 PPO 分片。

```sh
sactl ai train --environment ./environment.json --data-dir ./ai-data --batch-matches 32 --batches 10
sactl ai train --environment ./environment.json --data-dir ./ai-data --resume --batches 10
```

首次调用可设置 `--mode`、`--points`、`--pet-points`、`--level`、`--max-turns`、`--seed`、`--epochs`、`--sequence-length`、`--learning-rate`、`--target-kl`、`--gae-lambda`。恢复时这些参数来自 checkpoint，不能传入另一组值；`--batches` 是再完成的批次数。默认每批 32 场，1v1、人物和宠物各自 120 点、等级 35。训练使用规则策略、冻结历史网络和同代冻结副本，固定规则轨迹不会进入当前模型的 PPO 批次。

原生训练可用 `--workers 2` 并行采集，范围 1～8，每次调用默认 1。每个 worker 是独立引擎进程，启动时要求规则摘要、平台和场景一致；同一批使用固定策略快照，比赛仍按预定序号、seed 和对手安排，按序保存分片/checkpoint，梯度更新始终串行。worker 数只控制本次执行资源，不写入模型配置，可以在 `--resume` 时调整，例如：

```sh
sactl ai train --environment ./environment.json --data-dir ./ai-data --workers 2
sactl ai train --environment ./environment.json --data-dir ./ai-data --resume --workers 2
```

每轮并发窗口最多保留一场/worker，全部采集成功才开始按序提交；worker 失败或取消时取消并等待其他采集结束，不使用该未提交窗口的数据。提交过程中中断则保留已提交的连续前缀，恢复重采余下比赛，不能按完成快慢挑选样本。进度字段 `collection_workers` 记录实际调用设置。更多进程增加内存占用，且不会并行化模仿/PPO 优化器；不能据 worker 数宣称训练成比例加速。示范训练和旧 SQLite 训练拒绝此参数；旧 CLI 同样拒绝未知参数，需使用支持该入口的版本。

原生模仿热身在开始或恢复训练轮次时，按分片校验原始观察、老师动作和采集赛程，内存中只保留随后更新需要的数值帧、动作及来源标识。完整原始分片仍保存在磁盘，恢复热身时重新读取校验；没有另建可绕过来源检查的数值缓存格式。该方式减少完整观察的驻留，不改变训练顺序、模型/checkpoint 格式或优化器计算。实际内存仍随对局长度、人数、数据量和计算图增长，不是固定内存上限；下述磁盘停止阈值也不限制 RAM。

原生训练的 `--stop-at-data-bytes 10737418240` 设置 10 GiB 的保留文件停止阈值，默认 `0` 不检查；可以在 `--resume` 时增大或关闭，不修改模型/checkpoint 训练配置。启用后在 ready 和每个已提交 checkpoint 后统计数据目录普通文件的逻辑字节总量（含历史、模型、日志），达到阈值返回非零并输出 `storage_limit_reached`、`data_bytes`、`stop_at_data_bytes`、`storage_check_event`。所有数据保留，不自动删文件，也不自动导出候选；即使最后一批已经保存，仍明确报告阈值停止，可单独用 `export-model` 导出。

这是停止阈值，不是文件系统硬配额：初始化、单次提交或优化更新及引擎日志可能使总量超过阈值，需要给磁盘留余量。每次检查遍历目录，启用后会增加文件系统开销；根目录可以是符号链接，但不跟随其内部链接，数据目录挂载在 Docker volume 内时同样统计其中的文件；数据目录之外的引擎归档、Docker 镜像、其他 volume 或目录不计入；稀疏文件按逻辑长度、硬链接按每条路径计算。示范和旧 SQLite 训练拒绝此参数。恢复时仍需显式传阈值，否则本次默认 0。该能力不提供自动清理或保留期淘汰。


### 父模型探索初始化

v0.2.10首次原生训练可指定 `--from-model ./parent.json --initial-policy-scale 0.5`。该选项适用于已声明同一父模型的单模式实验（1v1～5v5）或复合实验；默认 1 保留精确父权重及原来的序列化格式。参数须在 `(0,1]` 内且转换为 float32 后仍为正数。小于 1 时，只在第一场采集前将最终动作评分层 `score.1.w` / `score.1.b` 乘以该值，并使用新的 Adam；其他权重不变，父模型原件不变。单模式必须关闭模仿热身（继承父模型时默认关闭）。

```sh
sactl ai train --environment ./environment.json --experiment ./next-experiment.json \
  --from-model ./parent.json --initial-policy-scale 0.5 --data-dir ./exploration-training
sactl ai train --environment ./environment.json --data-dir ./exploration-training --resume
```

缩放会改变真实采样分布和训练梯度；它不是运行时温度，不能将旧策略采集的 PPO 轨迹改标签复用。目录保留原父 artifact、初始学习状态和 `initializations/` 中的不可变回执，绑定训练配置、原权重、缩放系数及新权重。恢复/导出重新核对这些来源及首批实际策略身份，然后继续保存的训练状态，不再次缩放。回执也包含在导出的训练报告中；推理模型架构不变。

缩放训练使用单模式 checkpoint schema 5，或复合训练配置/checkpoint/export-report v2，旧客户端须拒绝。省略该选项或取 1 保留原 schema。显式传参需要 `--from-model`；SQLite、示范训练和 `--resume` 均拒绝该参数，恢复时也不能重复传 1。该能力未证明稳定胜率提升，只应作为有固定预算和独立验证的训练实验。

### 独立成员的离线训练对照

v0.2.10 `train --plan-scope team|member` 默认 `team`，保持原指挥官架构与旧模型摘要。`member` 用于检验团队联合计划的作用：成员共享同样的完整公共观察、己方共享信息、历史编码及网络权重，但每个成员仅保留自己的人物→宠物计划记忆，不读取队友本回合尚未执行的选择。相同输入的历史编码可以复用计算，数值上等价于各成员用相同权重独立重算；共享价值头与整队 PPO 目标保持不变。因此这是“独立成员执行、共享参数和集中式训练”的对照，不是每个成员各训练一个不同网络，也不是独立 Q-learning。

```sh
sactl ai train --environment ./environment.json --data-dir ./member-baseline \
  --mode 2 --plan-scope member --warmup-teacher independent-control
sactl ai train --environment ./environment.json --data-dir ./member-baseline --resume
```

模型导出标记为 `independent-member-policy-v1`，配置 `network.config.plan_scope=member` 参与权重摘要、checkpoint 和报告核验。可作为离线 `evaluate --opponent-model <模型文件>` 的真实训练对手，也可用 `evaluate --model` 检查它自身的效果；实验绑定、人数、引擎规则、训练数据排除等原有条件继续生效。正常 `learned`/`hybrid` 指挥及联网 `simulate` 拒绝该对照模型，不新增线上策略。

只有新建原生训练可以指定 plan-scope；恢复不允许覆盖，继承父模型时保持其架构并拒绝矛盾的显式参数。不能把已训练的 commander JSON 改标签当成独立训练结果。该参数不适用于旧 SQLite 或示范训练 CLI；旧客户端不支持时必须升级，不能删参数后冒充同一个实验。公平比较须预先固定相同网络规模、初始化 seed、训练预算、教师/对手和评测赛程；在相同条件下分别训练两种模型。少量执行测试及 1v1 相同输出都不证明多人指挥优势。

`--gae-lambda` 默认 .95，范围 [0,1]，控制优势估计的偏差与方差权衡；设为 1 时使用完整已采集回报减去当前价值基线，截断仍保留价值 bootstrap，不把未结束比赛当成平局。它不同于 `--sequence-length`（反向传播片段长度），也不改变奖励定义。较大的值可能加强长期终局信号，也可能增加训练方差，须在同一冻结评测上比较，不能视为强度开关。该值已包含在训练配置/报告中；新 CLI 仅开放原算法参数，默认和旧目录恢复行为不变。旧 CLI 不支持此参数，应先检查 `train --help`，不能删除参数后冒充同一实验。

`--entropy-weight` 默认 .01，控制 PPO 的条件动作熵奖励，接受有限非负数。0 只移除这项损失，不关闭训练采样、不改变胜负奖励，也不把实战从条件贪心改为随机动作。提高它倾向保留更多探索，但可能损害已有行为；降低它也可能导致过早收敛，必须用独立对战衡量。参数保存在原有 `ppo.entropy_weight` 中；只有新训练可设置，恢复严格沿用保存值，旧 SQLite 入口不接受。

默认原生 CLI 训练仍使用上述 GAE 和原来的单次采样调度。v0.2.10新增实验参数 `--opening-rollouts 2..64`：对同一完整开局/引擎 seed/阵营/行为模型/对手实际重复运行，使用不同动作随机种子；次数必须整除 `--batch-matches`。默认值 1 禁用重复采样，不改变旧格式和训练结果。`games`、`--batch-matches` 仍按实际比赛计数，例如 32 场、每开局重复 4 次，包含 8 个逻辑开局。不同开局即使偶然配置完全相同也不会合并。热身采样不受影响。

`--policy-advantage gae`（默认）可作为相同重复采样调度的对照；`--policy-advantage opening-loo` 要求至少重复两次，用同组其他对局的终局回报均值作为策略基线，并保留原 GAE 价值目标、递归 PPO、按回合归一化和 KL 回退。留一估计要求 gamma=1。两种重复采样模式都要求完整终局，拒绝截断、不完整分组、重复采样 seed 及与保存采样流不符的动作，不把截断改成失败；截止批次会保存在原目录且恢复时继续明确拒绝，需在新目录设置更长采集上限重做实验。普通单次 GAE 仍正常使用截断 bootstrap。

```sh
sactl ai train --environment ./environment.json --data-dir ./repeat-loo \
  --batch-matches 32 --opening-rollouts 4 --policy-advantage opening-loo --batches 10
sactl ai train --environment ./environment.json --data-dir ./repeat-loo --resume --batches 10
```

这些参数只能用于新建原生训练，不能覆盖恢复配置或与 SQLite/实战示范训练混用。重复采样目录使用 checkpoint schema 4，旧客户端明确拒绝；每场原子提交分片及计数，中断在组内也可继续采集，整批齐全后才更新。每批的 `openings/` 回执绑定有序分片、采样摘要、估计器、更新前后完整学习状态；恢复和导出会重新核验采样、赛程、模型身份及优化器步数，核验成本随已完成数据增长。此过程不是重新执行所有历史梯度更新。每次更新后重新采集，不复用旧行为轨迹。早期 `build/learned/opening-*-pilot` 是独立实验产物，仍不能用 `train --resume` 恢复。实验尚未证明稳定胜率提升，不改变默认算法或冠军门槛；研究结果见 [实现记录](learned-implementation.md)。

新更新日志的 `report.epochs[].objectives` 分开记录 `policy_loss`、已乘系数的 `value_loss` / `entropy_loss`，以及未乘系数的 `conditional_entropy`。都是本轮更新前在完整递归轨迹上按队伍回合取均值；三个损失之和与原 `loss` 在浮点舍入误差内一致。条件熵沿记录的计划前缀求和，不是枚举所有整队计划的精确联合熵，更不是胜率。旧报告没有此字段时保持缺失；新记录只增加诊断，不改变数值更新。

从父模型开始的新实验，验证集也允许评估其声明的原始父模型：`sactl ai evaluate --environment <环境> --experiment <子实验> --model <原父模型> --split validation --output <新报告>`。完整模型摘要必须与实验 `initial_model` 一致，规则和已声明人数必须兼容；不修改父模型的来源、权重或认证范围。它与子模型使用相同验证场景，可以比较训练前后效果，且原始证据仍由 `verify-evaluation` 核验。最终 `test`、冠军挑战与晋级仍要求该实验的训练后候选；这项能力不会为父模型开放子实验的最终测试。对比必须固定相同对手池，不能用不同对手的总胜率推断进步。

新实验默认先采集 32 场规则对战、执行 8 轮模仿热身，再进行指定数量的 PPO 批次。`--warmup-matches` 必须为 4 的倍数，设为 0 可做无热身对照；`--warmup-epochs` 设置热身更新轮数，`--warmup-learning-rate` 默认 .001。重复 `--warmup-teacher` 可以指定 basic、focus、guard-break、defensive、sustain 的非重复子集，默认使用五者；每组配置的换边会同时交换教师。热身和 PPO 都使用 `--sequence-length`。这些是可复现实验起点，不代表已经找到最优超参数。

新训练默认 `--warmup-batch-episodes 8`，按完整单方轨迹分小批更新；32 场产生 64 条双方轨迹，因此每轮八次更新，八轮共 64 次。轨迹顺序由已保存的优化器步数确定性打乱；单条轨迹内部不打乱、不跨模型更新复用旧回合记忆。`--sequence-length` 只控制反向传播片段，不是优化器小批大小。

设 `--warmup-batch-episodes 0` 可复现旧的全量单次更新和轨迹顺序；旧 checkpoint 缺少该字段时保持这一行为。新参数随训练配置固定，恢复时不能覆盖，也不能用于旧 SQLite 训练。一个完整热身轮次成功后才提交权重、优化器与进度；即使取消发生在后面的小批，外部状态也不接受半轮更新。

`warmup_report.optimizer_updates` 记录新模式每轮更新次数。`cross_entropy_before_update` 和 `gradient_norm` 在小批模式按动作数量汇总各小批更新前的值，不是同一份冻结权重在整轮数据上的评估，也不能跨两种更新方式直接当强度指标。另用固定模型、独立比赛和动作分析检查效果，尤其不能用“多数动作都是攻击”的总体准确率掩盖治疗/换宠没有学会。

热身重放规则的真实动作，不按胜者筛选，也不把比赛回报当作动作正确性标签。每轮从比赛开头重建回合记忆，连续片段反向传播，以每个行动者的平均交叉熵训练策略；价值头不接受教师胜负监督。最后一轮完成后保留权重、清空 Adam 动量，再开始 PPO。进度事件 `warmup_collected`、`warmup_trained` 及 `warmup_games`/`warmup_epochs` 独立于 PPO 的 `games`/`completed_batches`。

新建训练默认保存 `ppo.update_guard=backtrack-v1`：每次 Adam 尝试更新后，用暂定权重从每场开头重建回合记忆，计算相对本批冻结采集策略的整队平均近似 KL。超过 `--target-kl`（默认 .03）时从更新前的权重、步数及动量重新尝试，每次学习率减半，最多减半八次；所有尝试都不合格则保留先前已接受的更新并停止本批优化。比赛和已消费批次仍保存，不会因本批零次更新而无限重采；历史对手池对相同学习状态只保留一个位置。该限制是已采集轨迹上的经验统计，不保证未见局面、单个动作或胜率不退化。

每个接受的 epoch 报告 `post_update_kl`、实际 `learning_rate` 及非零时的 `backtracks`；`rejected_updates` 汇总本批拒绝次数，`stopped_for_kl` 表示停止而不是仅发生减步重试。已有 `approx_kl` 仍为更新前统计，不能误读为最终权重的变化。取消或错误不提交半个 PPO 批次。旧 checkpoint 缺少 update_guard 时保留原来的更新前检查及对手池顺序，不暗中改变续训语义；要启用新行为须创建新训练目录，不能编辑旧记录的配置或摘要。

训练分组根据实际初始配点/宠物/等级/人数归一化，忽略 seed、采集上限和左右阵营顺序；不能靠换边或改 seed 把训练场景放入测试。新实验默认 `pairing=roster-side-v1`：每家族八局，两个随机种子 × 两种配点归属 × 左右场地。人物、出战宠物和备用宠物的完整阵容一起交换。

PPO 学习方与热身教师也在八局周期内接触两套阵容；批次可以跨越周期边界，恢复保存的顺序。热身仍允许四场的倍数，四场覆盖一个 seed 的两种配点归属与场地。配点搜索 `build-search` 的目的不同：它固定待评分阵容，继续按原四场评价该阵容，不交换待评分配点。

旧清单、checkpoint、报告和注册表缺少 pairing 时继续原四局赛程，只交换场地，算法保持同一配点归属。有限家族中的阵容强弱会影响旧报告的绝对胜率，须结合相同配置的规则基线和分组区间解释。不能改标签冒充新赛程；新 CLI 使用旧清单训练也继承旧 pairing。新格式分别为 experiment v2、checkpoint schema 3、evaluation v3、champion registry v2，让旧程序明确拒绝，不静默按四局执行。

完成时输出 `candidate_saved` 及推理文件路径。模型保存 schema、网络参数、权重摘要、训练报告和数据分片/配置组摘要、规则及平台；checkpoint 另含优化器和采样进度。所有产物仅标为 candidate，不自动改动用户指定的在线模型。

## 旧战斗记录

v0.2.10 `sactl ai export-data` 直接读取已有 `metadata.json`、`events.jsonl`（或 `.gz`）、`result.json`，无需 Python、Docker、账号登录或游戏后台。输入可以是整个记录根目录，也可以是一场比赛目录；只读原文件。

```sh
sactl ai export-data --records ./battle-records --format builds --output ./builds.jsonl
sactl ai export-data --records ./battle-records --equal-points --output ./transitions.jsonl
sactl ai export-data --records ./battle-records --mode pve --output ./pve-transitions.jsonl
```

默认 `--format transitions --mode pvp-1v1`；其他模式为 `pvp`、`pve`、`all`。`builds` 始终要求同预算、同已记录外部条件的 1v1 PVP，保留 `experimental_control_verified=false`，因为观测到条件相同不证明实验受控。`--equal-points` 也只接受可核验的双方 1v1 人物预算，不宣称已经比较多人或宠物总预算。

输出沿用旧 `battle-export.py` 的 JSONL schema 1 与分组算法，保留规则、终局、实际观察及独立服务端标签；不输出未列入字段契约的额外字段。请求必须引用已结束的同方同回合观察，真实数值缺失/类型错误不补零，预算重新计算，序号/身份/完整性及 gzip 校验失败会排除该局。跨回合观察不连续也排除，不虚构缺失状态；人物离场后没有下一观察时不生成该人物缺失的后续 transition。因此逐回合导出不保证每个人物都有终局 transition，不能据此当作完整 recurrent 训练轨迹。

受控原生环境的 Go 训练分片与额外 C `battle-records` 是不同数据来源。已发现旧原生适配器会把离场成员的占位 `N` 记录为引用旧观察的新请求；严格读取器会以 `broken_observation_reference` 排除，不能删除这类请求或补造观察后冒充原始数据。开发源码已修复占位符处理，但既有日志不自动修复。另有 `native-step-v1` 记录缺少旧导出器所需实验分组字段，会被排除为 `invalid_experiment`；使用相应完整 Go 分片进行现代训练，不将额外日志的零丢失标记等同于可导出或可作 PPO 样本。

规则字段要求原始记录中已保存 64 位十六进制摘要。早期调试数据若只有 `native-validation` 等文字标识，会以 `invalid_rules` 排除；保留原文件用于历史分析，不补造规则摘要或把其结果用于当前模型认证。记录内的摘要本身不证明规则档案可用或来源可信，正式训练仍须核验实际环境与档案。

默认保留 `defeat`、`turn_limit`，其他原因需明确 `--include-abnormal`。旧格式没有胜者的终局保留 censored/truncated，不补成真实平局或败局。服务器结算指令和行动顺序仅作标签，不能进入决策前 observation；它们也不保证技能实际生效。未知策略仍为 null，`legal_action_mask` 仍为 null，没有当前 PPO 所需的采样概率。此输出可供配点分析、回归与后续旧数据转换，当前 `ai train --environment` 不直接消费它；完整旧 JSONL/SQLite 神经热身导入仍需后续接入。

`--output` 必填，父目录须已存在；使用 0600 临时文件，全部处理完后原子发布新文件，不覆盖已有路径。没有任何可用样本或取消时不发布；同根目录重复 match_id 拒绝整个导出，避免重复加权。stdout 给出 JSON 报告：导出/过滤/无效局数、无效原因、行数、文件 SHA-256 和 `on_policy_ppo=false`；只要至少一局有效可以成功，须检查无效统计。原始输入不修改，输出摘要只是内容校验，不是来源认证。每局事件解压限制 64 MiB、单行 1 MiB；超限局明确排除，不能静默截断成有效样本。不要将未发布命令当成当前下载安装版本已具备的能力。

## 本地比赛示范导入

v0.2.10 `import-demonstrations` 从已经停止并完成 WAL checkpoint 的本地指挥官数据库重建完整队伍决策。不要用 Mac SQLite 打开仍在 Linux 容器中写入的数据库；先停止采集。命令以 `mode=ro&immutable=1` 读取，拒绝非空 WAL/journal、检查数据库完整性与导入前后文件摘要；这些检查不代替调用者确认采集已停止。

```sh
sactl ai import-demonstrations --database ./team/state/arena.sqlite3 --output ./demonstrations.jsonl
# 可重复 --database；同一比赛同一方重复出现会拒绝，避免重复训练权重。
```

输出为独立的 `commander-demonstration-v1` JSONL 和同名 `.manifest.json`，两个路径必须都是新文件、父目录已存在。manifest 最后发布，加载器核验摘要/大小/计数/原始观察重编码，缺少匹配 manifest 的文件不能作为完成数据。默认 `--features commander-observed-v8`，也可选择受支持的 v6/v7；它选择如何从已有公开观察编码，不改变旧权重或补齐缺失信息。

每个样本保留源数据库 SHA-256、比赛与结算摘要、真实规则/平台、所用策略版本、完整阵容分组、逐回合原始观察/事件截点、编码帧、选择及与其完全一致的 written intent。written 只证明客户端已写入指令，不保证服务端技能生效。相同回合的计划复用不重复计数；候选不受特征版本支持、缺少回合、没有结算、事件缺口、策略回退、未知提交结果或完整计划未全部写入时排除整场。只接受 defeat 的明确胜负结算，胜局和败局均可作为显式选择的模仿来源。

`recorded-roster-v1` 使用全部参赛角色 ID 排序分组，重复/换边同组；角色 ID 不进入网络。这个分组不能恢复敌方隐藏配点，不是原生实验的 `ScenarioGroup`，不得用它声称已排除合成测试配置重叠。不同模式、规则、平台和特征的数据不得混入同一次更新。没有规则摘要的旧 basic 记录会以 `missing_rules_metadata` 排除，不从另一方数据库或当前服务器猜测补齐。

本地指挥官现在对所有策略请求 `BTRULES`，初始化和逐回合采集均读取实际投影，再保存带事件截点的观察。basic、llm 和旧线性模型允许服务器缺少此扩展，每次最多额外等待约 250ms（不含命令调用耗时），缺失字段保持为空，不能用于要求完整来源的示范导入。神经 learned/hybrid 仍要求实际规则与模型匹配；BTIME、查询错误和整体取消不会因元数据可选而被忽略。旧客户端连查询本身也不支持时会明确失败，需更新本地工作程序。

当前已实现 `battletrain.ImitateDemonstrations` 训练内核，与原规则模仿共用连续历史/计划的梯度更新；保留完整比赛顺序、分批确定性更新、整轮失败/取消回滚及优化器恢复。它学习记录中的动作，不以胜负作为动作正确性或价值头标签，不补造行为概率。已用真实 5v5 记录验证更新与恢复。

v0.2.10可直接训练这些示范，不启动游戏服务器、Docker 或 Python：

```sh
sactl ai train --demonstrations ./demonstrations.jsonl --data-dir ./demo-training \
  --seed 901 --epochs 2 --batch-episodes 8
sactl ai train --data-dir ./demo-training --resume --epochs 2
```

首次 data-dir 必须是新目录、父目录已存在；挂载 Docker volume 时可选卷内的新子目录。自动从数据选择人数和特征版本，规则/平台/人数/特征必须各自一致。首次可配置 seed、batch-episodes（默认 8，0 为整批）、sequence-length、learning-rate 和 gradient-clip。`--epochs` 表示本次额外完成的整轮数，默认 1；它在原生 PPO 入口仍表示每批更新轮数，二者不混用。示范入口拒绝 environment、database、experiment、from-model、output 和 PPO 特有参数。

训练目录冻结完整 JSONL+manifest，保留内容寻址的模型/Adam、每轮报告及独立 `demonstration-latest.json` 提交指针。整轮完成才原子更新指针；Ctrl+C 在该轮提交前发生时保留上一完整轮，输出 `demonstration_training_interrupted` 并以失败状态退出。恢复只接受 data-dir、resume、epochs，不重新读取外部数据，也不允许换训练参数。报告、数据或学习状态损坏时拒绝恢复，不悄悄重置优化器；同一目录只允许一个训练进程。初始化中断若尚未生成指针，保留现场并使用新的训练目录，不伪造可恢复进度。

完成至少一轮后，使用同一导出命令生成可供本地 learned/hybrid 读取的候选模型：

```sh
sactl ai export-model --data-dir ./demo-training --output ./recorded-model.json
# 可选 --checkpoint <已保存的 checkpoint 摘要>，不倒退当前训练进度。
```

导出重新核验冻结数据、每轮报告、权重及 Adam 进度，未完成训练或来源损坏时拒绝。默认输出为训练目录 models 下的内容寻址文件，指定路径不覆盖不同内容。模型包含推理权重及来源摘要，不包含优化器、原始角色阵容和登录凭据。新产物使用文件 schema 3，网络架构仍是 commander-policy-v2；只有更新后的客户端支持。配置中的 strategy 选 learned，model 指向该文件；hybrid 仍需配置本地 Chat Completions 提供商。

规则/平台/人数继续严格检查，模型仅覆盖示范数据对应的模式。recorded_training 保存数据集、训练 checkpoint、源数据库摘要和 recorded-roster-v1 分组；不填写未知的合成场景、不伪造 TrainingGroups/HeldoutGroups 或完整竞技场认证。在线指挥、隔离 simulate 和匹配规则/平台/特征契约的原生 evaluate 均可使用该候选；未知敌方配置导致的源数据重叠会在报告及 CLI 的 `unverified_source_artifacts` 中列出模型摘要。候选和对手都纳入检查。`verify-evaluation` 会从冻结产物重算此字段，删除/篡改标记不会通过核验。已知的原生配置分组继续排除；字段非空时不能声称全部测试数据独立，也不能通过自动冠军晋级。

可从示范候选初始化原生 PPO，必须先冻结使用同一父模型的实验。例如目标模式为 5v5：

```sh
sactl ai experiment --environment ./environment.json --from-model ./recorded-model.json \
  --mode 5 --train-groups 32 --validation-groups 8 --test-groups 8 --output ./recorded-experiment.json
sactl ai train --environment ./environment.json --experiment ./recorded-experiment.json \
  --from-model ./recorded-model.json --data-dir ./native-training --batches 10
sactl ai train --environment ./environment.json --data-dir ./native-training --resume --batches 10
sactl ai export-model --data-dir ./native-training --output ./child-model.json
# validation 供开发选模，不提前消耗 test。
sactl ai evaluate --environment ./environment.json --experiment ./recorded-experiment.json \
  --model ./child-model.json --split validation --opponent basic --output ./validation.json
```

PPO 从父模型的精确权重和新 Adam 状态开始，之后只使用新引擎采集的 on-policy 轨迹；不会把示范动作补造为 PPO 数据。直接接续的子模型使用文件 schema 4，保留原 recorded_training，同时记录实际原生环境、实验、父模型及原生配置分组。接续、恢复、再导出和下一代实验均核验来源继承；来源不确定性不会因训练几轮自动消失。新客户端 learned/hybrid 支持 schema 2/3/4/5；旧客户端遇到新字段/格式明确拒绝。跨人数初始化不会扩充父模型的可用模式，目标模式须训练和评估其自己的子模型。

带实战来源的模型也可作为 `build-search --model` 指挥策略或 `--opponent-model` 对手。搜索、配点池和后续实验通过 `recorded_selection_artifacts` 保留两方的实战模型摘要及继承的间接来源；恢复和导出配点池会从冻结模型重新核验。使用这种池从零训练也受到来源影响，不能因没有 --from-model 就丢弃标记。此类原生子模型采用 schema 5；间接配点影响与直接梯度父模型的 recorded_training 分开表示。下一轮训练/搜索继续传递，评估仍列入 unverified_source_artifacts 并禁止自动晋级。已知原生配置组继续排除，未知实战配置不冒充已隔离。示例见 [配点搜索](learned-build-search.md#实战来源的间接影响)。

不要把示范 JSONL 传给 `train --environment` 或旧 `train --database`，也不能给旧 checkpoint 改 schema 来绕过检查。选择较差的示范也可能学到较差行为，模仿 loss 下降或少量 PPO 更新不构成胜率提升。

## 训练对手调度与证据

新训练默认 `--opponent-sampling weakness-v1`：规则、历史模型、同代冻结模型三类的采样比例为 30%/50%/20%；没有历史模型时，该部分使用同代模型。在规则和历史模型类别内，按最近八个已提交批次的训练胜负调整权重，难以击败的对手权重更高，容易的对手仍保留探索机会。采集截断单独计数，不影响难度。整个采集批次固定权重，训练成功提交后才更新。

v0.2.10 `train --opponent-mix 80,10,10` 可固定三类占比，依次为规则、历史、同代自我对战，均为 0..100 的整数百分比，总和必须是 100。它控制类别选择，`--opponent-sampling` 仍控制类别内部的对手权重；uniform 也可使用该比例。历史池为空时其份额转给同代模型。百分比是每局抽样概率，不是每批精确配额；不会把规则对手的动作轨迹加入 PPO。默认和显式 `30,50,20` 保持旧随机赛程及训练数值，旧 checkpoint 不补写新字段。`league` 输出 `opponent_mix` 便于核对。提高规则占比可能减少自我对战探索，不保证更强，应冻结其他条件后独立评估。

历史池至多保留 32 个学习状态，其中最近 16 个保留，其余优先保留较难的旧对手；同难度优先较新的状态。淘汰只影响采样池，不删除已有模型或原始证据。对手按学习状态摘要识别，报告同时保存实际网络摘要。

```sh
# 验证原始分片、赛程、对手身份和成绩，再输出 JSON。
sactl ai league --data-dir ./ai-data

# 新实验可显式选择旧均匀调度，便于对照。
sactl ai train --environment ./environment.json --data-dir ./uniform-data --opponent-sampling uniform
```

`league/` 保存每批不可变赛程与结果；checkpoint 保存引用、最近成绩和历史池。恢复、导出模型及 `league` 均从原始训练分片复核这些数据，拒绝不一致记录；大型数据集检查会随已训练场次增长。`league` 输出标记 `purpose=training-diagnostics`，成绩是训练表现，不能当独立验证或晋级报告。uniform/历史目录没有该矩阵时返回空列表，不虚构历史证据。

`--opponent-sampling`、`--opponent-mix` 与 `--rule-opponent` 随首次训练固定，不能在 `--resume` 时覆盖，也不能用于旧 SQLite 线性训练。历史 checkpoint 缺少采样字段时保持原有均匀调度及随机赛程。旧 CLI 必须拒绝未知的新参数或保存了新比例的 checkpoint，不能略过比例后继续训练。

## 独立评估的原始证据

v0.2.10新增 [本地冠军晋级与回退](learned-champion.md)：`sactl ai champion init/challenge/status/rollback/abandon` 冻结门槛、执行独立最终测试并保留审计记录。受控原生成绩与完整线上认证仍分别验收。

v0.2.10原生 `evaluate --output ./validation.json` 同时保存 `./validation.json.data/`：`spec.json` 在首局前冻结配置、实验划分和对手集合，`models/` 保存参与模型的不可变副本，`shards/` 保存每场双方完整轨迹。报告中的 evidence 与每局 shard 摘要关联这些对象。在线 SQLite 预测评估保持原输入与输出。

```sh
sactl ai verify-evaluation --report ./validation.json
```

核验不启动战斗引擎，也不连接游戏服：从冻结模型和实验重建未污染的赛程，核对双方观察/特征/动作/结果，再按原顺序重新运行规则或贪心网络，确认每条动作来自冻结策略，并重新计算统计。输出 `evaluation_verified` 和报告摘要；不授予竞技场认证，不晋级模型。大报告核验需要读取全部轨迹并重新推理，可以 Ctrl+C 中止。

迁移数据时一起移动报告及同名 `.data` 目录；不依赖原训练目录中的模型路径。已有完成报告不能覆盖。中断评估保留原始分片，但不发布残缺报告；可用完全相同参数重跑，改模型、对手或配置须用新输出路径。重跑会重新进行比赛，不声称从中断场次续接。目录内只允许一个评估写进程。

旧的汇总报告仍可阅读，但 `verify-evaluation` 明确拒绝缺少原始证据的报告，不能补写一个 evidence 字段冒充新格式。完整性核验也不是服务端数字签名或 C 引擎结算重放，不对人为重造整套数据作真实性担保。强度门槛、多人线上时限和规则一致性仍需各自验收。

v0.2.10增加配对比较入口，无需编写临时分析程序：

```sh
sactl ai compare-evaluations --baseline ./parent-validation.json --candidate ./candidate-validation.json
```

这是本地只读命令，不运行比赛或调用 LLM。两份报告及各自 `.data` 都必须完整；它先执行与 `verify-evaluation` 相同的原始证据核验，再要求相同冻结 experiment、validation 划分、实际环境/场景设置及完整对手集合。不只取有利的共同对手。每场核对实际开局、阵营和对手策略身份，允许双方报告的对手段落顺序不同；相同阵容的合法重复开局仍按冻结赛程配对。

每个对手返回胜负平/截断统计、配置家族数，以及 `paired_delta`（候选得分减基线，胜=1、平=.5）。以完整配置家族重采样 10,000 次，固定 seed=8301，输出 `paired_ci95` 和 `bonferroni_bootstrap_interval`；后者按 `.05 / 本次完整对手数` 分配 alpha。旧四场家族和新八场家族分别按原赛程计算，组内比赛不是独立样本。区间是 bootstrap 近似估计，校正仅覆盖本次比较的多个对手，不覆盖反复调参、多个模型/检查点或重复调用。

任何一方有截断时，该对手保留原始统计及 `censored_pairs`，但不输出配对均值或区间，不删除该对局、不按输局处理。少于 20 个配置家族仍可显示完整得分差，但不显示置信区间；`interval_unavailable` 给出原因。嵌套 `baseline/candidate.completed_score` 只针对各自完成局，出现截断时不能拿两者相减当配对提升。

输出绑定双方报告/模型摘要，并保留双方 `unverified_source_artifacts`。正的点估计、单个对手的区间或命令成功都不等于冠军资格；命令始终 `arena_certified=false`，不修改模型或注册表，不代替预先冻结的 champion 门槛。核验长历史支持 Ctrl+C，未成功时不输出部分比较报告。

## 预先冻结训练、验证与最终测试

正式比较使用同一个实验清单，先划分配置家族，再开始学习：

```sh
sactl ai experiment --environment ./environment.json --output ./experiment.json --mode 1 --train-groups 1024 --validation-groups 256 --test-groups 256
sactl ai train --environment ./environment.json --experiment ./experiment.json --data-dir ./ai-data --seed 1 --batch-matches 32 --batches 10
sactl ai evaluate --environment ./environment.json --experiment ./experiment.json --split validation --model ./ai-data/models/<摘要>.json --output ./validation.json
```

新清单 `commander-experiment-v2` 保存 pairing、规则/平台、人数、双方人物与宠物配点、等级、采集上限及分组。最多 4,096 个家族，三个集合都必须非空；同一配置家族不能靠改 seed、换边或互换配点归属进入另一集合。`--*-groups` 是配置家族数，每家族八场，256 组即每个对手 2,048 场；旧 v1 清单仍是每家族四场。生成清单只读取引擎元数据，不采集对战。

绑定清单后，热身与 PPO 都只采集 train 家族，循环使用训练家族时换用新的对战 seed；训练 seed 可用于多次独立训练。`--mode`、点数、等级和 `--max-turns` 来自清单，不能另外覆盖。训练目录将清单按内容摘要保存到 `experiments/`，checkpoint 和推理模型均引用摘要；`--resume` 使用已保存清单，不再传 `--experiment`。旧的未分组实验保留原行为，不补标签冒充事先保留了测试集。

验证用于选超参数和候选模型。选定后只对最终候选执行：

```sh
sactl ai evaluate --environment ./environment.json --experiment ./experiment.json --split test --model ./ai-data/models/<最终候选摘要>.json --output ./test.json
```

第一次最终测试在比赛前写入 `experiment.json.test-selection.json`，固定候选完整摘要和对手集合；中断后允许同一选择重新运行，不允许在原清单旁换模型或对手反复选优。保留该文件；它防止误操作，不防手动删除或复制清单后重新测试。最终测试结果不可再用于这轮实验的调参。evaluate 只输出评估；自动更新受控原生冠军使用另行冻结门槛的 champion 流程，完整线上认证仍待验收。

绑定实验的模型评估必须指定匹配的 `--experiment`。评估使用整个指定集合，不能用 `--matches`、seed 或场景参数缩减/替换；任何候选或对手训练组与该评测集合重叠都会报错，不悄悄跳过。默认五个规则对手，也可显式指定 `--opponent` 和 `--opponent-model`；正式对手矩阵及晋级门槛还需独立冻结。报告包含完整实验清单和实际比赛，保存时核对种子、换边、重复、配置及对手版本，不接受用重复比赛凑齐数量。

## 从 1v1 权重开始多人训练

新实验的 `--from-model` 可以用相同规则、平台和特征架构的父模型初始化不同人数模式。这里迁移的是权重，目标人数由新实验固定，优化器重新开始；父模型不改，祖先训练、选择及保留评测家族继续隔离。例如：

```sh
sactl ai experiment --environment ./environment.json --from-model ./model-1v1.json --mode 2 --reserve-pets 2 --healing-magic 20 --healing-items 2 --output ./experiment-2v2.json
sactl ai train --environment ./environment.json --experiment ./experiment-2v2.json --from-model ./model-1v1.json --data-dir ./training-2v2 --warmup-matches 128 --warmup-teacher sustain --warmup-teacher control --batches 10
```

子模型仅声明新训练的目标人数，仍是未经完整竞技场认证的 candidate；它不自动继承父模型的适用人数或成绩。1v1 父模型仍不能直接用于 2v2 实战/评估，不允许改其 modes 字段冒充多人模型。多人强度和执行链路必须分别评估。已有同人数训练和恢复语义不变；旧客户端可能拒绝跨人数初始化，应使用支持该功能的v0.2.10。

## 环境文件

v0.2.10新增 `sactl ai environment init/check` 与独立 `ai-training` 镜像构建目标。从 v0.2.10 发布该镜像，须先显式下载；旧 `legacy-runtime` 或任意基础镜像不能冒充训练镜像。初始化不会自动下载，发布流水线须先完成 amd64/arm64 实际引擎与采集训练测试。

发布流程为 amd64 和 arm64 分别使用原生 Linux 托管 runner，检查匿名下载、镜像与 CLI 版本、镜像内 skill、volume 恢复与导出模型评估；两种架构都通过后才发布 Release。构建时的短训练检查不代替这些镜像发布后的验证。另行比较 ai-training 与 legacy-runtime 的 amd64 默认规则摘要。原生编译时间固定为源码提交时间，避免同源码因 `__DATE__` / `__TIME__` 不同而产生不同二进制摘要。此比较不覆盖生产自行修改的配置和表；上线时仍以游戏服返回的实际规则元数据为准。

Mac 原生 Go 训练、Docker 仅运行 Linux 引擎：

```sh
SA_TRAINING_IMAGE=ghcr.io/k0ngk0ng/stoneage/ai-training:v0.2.10
docker pull "$SA_TRAINING_IMAGE"
sactl ai environment init --image "$SA_TRAINING_IMAGE" --directory ./ai-runtime
sactl ai environment check --environment ./ai-runtime/environment.json
sactl ai experiment --environment ./ai-runtime/environment.json --output ./experiment.json
sactl ai train --environment ./ai-runtime/environment.json --experiment ./experiment.json --data-dir ./ai-data
```

`ai-runtime` 必须是新目录、父目录已存在。初始化检查本地镜像标签和真实工作进程的规则/平台，成功后才生成 `environment.json`；工作进程失败时保留 `worker.log` 与规则档案供诊断。配置固定镜像的本地 `sha256:` ID，后续 tag 更新不会改变这个实验的引擎；不要清理仍被训练引用的镜像。规则档案写入 `ai-runtime/rules`，训练数据另存 `ai-data`。配置保存绝对挂载路径，移动目录或换机器需重新初始化匹配版本，而不是手改模型规则摘要。

模型还绑定引擎平台。Apple Silicon 原生 ARM64 引擎训练的模型不能冒充 amd64 模型；如果目标游戏服是 linux-amd64，安装训练镜像时显式选择该平台（`docker pull --platform linux/amd64 "$SA_TRAINING_IMAGE"`），再初始化并核对输出。Mac 上这会使用仿真，速度须实测；CLI 固定实际检查到的平台，不按本机 CPU 替换。上面的流程只准备环境，不授予模型线上能力或强度认证。

如果希望全部训练数据存在 Docker volume，直接运行镜像内的 Go CLI：

```sh
# 固定已安装镜像 ID；避免同一 tag 更新后无意更换训练引擎。
SA_TRAINING_ID=$(docker image inspect --format '{{.Id}}' "$SA_TRAINING_IMAGE")
docker volume create stoneage-ai-data
docker run --rm -i --pull never --network none --read-only \
  --cpus 2 --memory 2g --tmpfs /tmp:rw,size=64m \
  --mount type=volume,src=stoneage-ai-data,dst=/data \
  "$SA_TRAINING_ID" experiment --output experiment.json
docker run --rm -i --pull never --network none --read-only \
  --cpus 2 --memory 2g --tmpfs /tmp:rw,size=64m \
  --mount type=volume,src=stoneage-ai-data,dst=/data \
  "$SA_TRAINING_ID" train --experiment experiment.json --data-dir training --batches 10
# 新容器从同一 volume 恢复已提交进度。
docker run --rm -i --pull never --network none --read-only \
  --cpus 2 --memory 2g --tmpfs /tmp:rw,size=64m \
  --mount type=volume,src=stoneage-ai-data,dst=/data \
  "$SA_TRAINING_ID" train --data-dir training --resume --batches 10
```

镜像入口是 `sactl ai`，默认工作目录 `/data`，无需再写 `sactl ai` 前缀。`STONEAGE_TRAINING_ENVIRONMENT` 默认指向随镜像提供的引擎 argv，train/experiment/evaluate/build-search/champion challenge 及 environment check 自动使用它；显式 `--environment` 优先。volume 保存实验、轨迹、checkpoint、模型和 `/data/rules`，删除容器不会删除它们。正常停止容器会留下已提交 checkpoint；强制中断未提交更新仍按恢复契约处理。建议每个训练目录只运行一个写进程，不同时启动两个 resume。

模型导出先运行同一镜像的 `export-model --data-dir training`，取得输出路径。可创建一个不启动的容器读取 volume，把该文件复制到本地，再删除这个临时容器；保留 volume 及原始训练数据：

```sh
SA_EXPORT_CONTAINER=$(docker create --pull never --network none --read-only \
  --mount type=volume,src=stoneage-ai-data,dst=/data,readonly "$SA_TRAINING_ID")
# 替换为 export-model 输出的实际模型路径。
docker cp "$SA_EXPORT_CONTAINER:/data/training/models/<模型摘要>.json" ./model.json
docker rm "$SA_EXPORT_CONTAINER"
```

不能把 checkpoint 当成推理模型。Mac 原生 learned 加载复制出的模型，仍需匹配实际游戏服的规则摘要、平台与人数模式。若还要在本地评估绑定实验的模型，一并复制对应的 experiment.json；训练恢复则保留整个 volume。

```json
{
  "schema_version": 1,
  "command": ["/path/to/native-worker", "--arguments-for-that-worker"]
}
```

`command` 是直接执行的 argv，不是拼接后交给 shell 的字符串。必须输出受支持的逐回合协议及有效规则/平台元数据；直接启动不带规则摘要的裸引擎只适用于低层调试，会被训练器拒绝。当前工具包装入口为 `server/legacy/modern/run-battle-environment.sh --config setup.cf`：从准备好的 GMSV 工作目录启动，先通过 `prepare-battle-rules.sh` 归档程序/选定战斗表/数值配置摘要，再启动专用 C CLI。`STONEAGE_BATTLE_RECORD_DIR` 指向可写的规则档案目录。

Mac 可原生运行 Go 训练器，以 Docker argv 启动 Linux worker。容器建议 `--pull never --network none --read-only`，将原生程序和数据只读挂载，仅规则档案目录可写。训练不需要账号服、游戏账号或生产网关。正式镜像中的包装脚本路径为 `/opt/stoneage/bin/run-battle-environment.sh`，工作目录为 `/opt/stoneage/defaults/gmsv`；需要包含本次改动的镜像版本，v0.2.9 及以前镜像没有此入口。

规则摘要现在由原生 `--battle-rules` 导出的实际配置和编译选中的表生成；主机覆盖、默认值及 getter 限制与引擎一致，归档复用会核验内容。规则格式升级后须新建实验，不能改旧 checkpoint 的摘要来恢复。热重载会清除线上兼容元数据。覆盖清单和未验证边界见 [规则来源契约](learned-rules-contract.md)。

当前新场景为 `controlled-battle-v8`，新建模型采用 `commander-observed-v8` 特征、`attack-guard-switch-guardian-v7` 动作，加入忠犬攻击及主宠技能组合。v8 模型要求 v8 环境对技能语义做原生校验。仍支持 v6/v7 模型和原 v7 场景的推理、评估、导出与恢复，但必须保留对应原生程序及相同规则摘要；`--from-model`、`--resume` 不自动升级特征，也不跨规则迁移。`export-model` 不转换特征或重标权重。新模型仍为 candidate，不能把新增动作或训练通过等同于策略变强。

v3 修正了受控宠物技能槽容量及出战宠物观察缺失，增加毒、石化、混乱、催眠攻击。出战选择通过只读 `S(AI)` 的可选 `active_pet` 字段投影到 `Own.BattlePetSlot` / `BattlePetSlotKnown`，不会伪造技能成功回执。v2 及以前的受控宠物可能始终等待，旧实验不能作为宠物决策强度证据；即使网络维度不变，也必须按新动作/规则版本重新采集训练。

v6 加入收宠/召唤动作和己方备用宠物表示，最多 40 个实体行，其中最多 20 个是场上人物/宠物；备用宠物不产生行动槽，也不计入群体治疗。全队仍只有一个指挥官，换宠回合的 W 指令仍属于原宠。召唤要求自身观察中的出战选择、SPET 待机掩码、PETST 可召唤掩码及骑乘状态；未知时不猜测。历史按真实顺序编码等待、收宠和召唤。

v7 场景增加 `--reserve-pets 0..2`，默认 0；用于新建 train、experiment、evaluate、build-search。每人额外携带指定数量的备用宠物，双方数量一致，每只宠物独立满足 `--pet-points` 预算；最多三只可选宠物，符合原生 SPET 限制。初始宠物保留七个已核实技能，随机备用宠物拥有攻击、防御及一种破防/状态技能，按槽位压紧排列。模型通过公开观察读取各宠物属性与技能，编号掩码只作场景元数据，不作为连续数值输入。配点搜索同时搜索备用宠物配点和专长技能，冻结实验、配点池、评估及断点恢复均保留完整阵容，不允许覆盖 reserve-pets。该场景可配合受支持的 v6/v7 特征，动作契约仍为 v6；旧场景产物不可改标签复用。真实多宠切换和采样训练已验证，少量对局不代表学会有效的换宠战术。

v5 增加 `--healing-items 0..15`，默认 0。双方每个人物的背包各供应相同数量的小块肉（模板 1234），每件占一个槽，只在新对局重置；回合内真实消耗，不自动补充。此参数可用于新建 train、experiment、evaluate、build-search，随冻结实验、配点池与恢复保存，不能额外覆盖。模型观察己方剩余库存；没有可靠物品身份时排除该道具动作。可以与治疗护甲组合，但不代表所有道具已经支持。

v4 增加受控治疗装备：新建 train、experiment、evaluate 或 build-search 时可设置 `--healing-magic 10`（单体）或 `20`（整侧），默认 `0` 不配治疗装备。双方使用同条件的原生护甲及 100 MP，实际消耗和恢复由原生魔法结算；已冻结实验或配点池继承此配置，不能用额外参数覆盖，恢复也不得修改。单体/群体治疗分别消耗 8/20 MP；候选按当前公开 MP 排除不可支付的魔法。它是离线实验装备条件，不是给线上角色发装备或补气力。

魔法身份与耗气、名称来自同一条原生 J 报文的可选 `id=` 扩展，投影为 `Magic.ID/IDKnown` 和候选 `MagicID/MagicIDKnown/MPCost`；旧服务器缺少 ID 时保留普通客户端操作，但 learned 不猜测治疗语义。卸下装备清空旧魔法。动作词表升级后需要新建训练，不得把 v3 模型改标签复用。规则热身现可使用 sustain 主动治疗、使用小块肉并替换濒死出战宠物，PPO 采样也已覆盖治疗；复杂道具和其他魔法仍未覆盖。

`--data-dir` 是运行 Go 训练器的进程所见路径。原生 Mac 路径与 Docker volume 名不是同一事物；将训练器本身运行在挂载 volume 的容器中时，才可直接使用该 volume 的挂载路径。无需仓库的训练镜像与初始化入口已加入v0.2.10，实际发布产物仍待 CI 构建与下载验收，不把本地基础镜像挂载测试当成已发布镜像验证。

## 恢复与数据

v0.2.10 `export-model` 可从已提交的历史 checkpoint 导出候选，适合比较模仿结束与 PPO 之后的实际对战效果：

```sh
sactl ai export-model --data-dir ./ai-data --checkpoint <checkpoint摘要>
```

摘要是 `ai-data/checkpoints/<摘要>.json` 的文件名去掉 `.json`，不是 learning state 或模型权重摘要。省略该参数仍导出 `latest.json` 指向的已提交状态。只完成数据采集、尚未完成任何模仿轮次或 PPO 批次时拒绝导出；已完成至少一轮模仿的 checkpoint 可以导出，并如实记录没有 PPO 报告。历史导出核验 checkpoint/权重、父模型、冻结实验及来源；纯模仿导出还从原始教师分片复核赛程、规则动作、回合/动作计数和训练组。损坏证据会拒绝，不补造元数据。

原生 `export-model` 及训练结束后的自动导出支持在历史来源核验时 Ctrl+C；取消核验不会发布候选，也不会修改已有训练指针或比赛分片。再次导出使用原目录即可，不需要重新训练。重复开局的历史采样核验可能较长，不能仅因暂时没有输出就启动第二个训练进程。

导出不修改 `latest.json`、优化器或正在进行的训练，不晋级模型；只能选择同一训练目录内按摘要保存的 checkpoint。产物仍是 candidate，验证使用原实验的 validation，不能借导出绕过测试集隔离。旧 CLI 会拒绝 `--checkpoint`，需要包含该入口的v0.2.10。

每场结束先发布不可变 gzip JSONL 与 manifest，再提交进度。模型/优化器按批次存储，小 checkpoint 引用大文件，避免每局重复保存权重。`latest.json` 以原子替换选择已提交 checkpoint；残留但未被 checkpoint 引用的临时产物不自动加入训练。进程级锁随退出释放，不依赖手动删除旧 PID 文件。

Ctrl+C 保留已提交比赛；正在运行的比赛舍弃，未提交的优化更新不改旧模型。恢复时使用 seed、game counter 派生各用途的随机源，并恢复历史对手池，原生引擎的规则摘要、平台和受控场景必须一致。已验证中断后恢复与不中断运行得到完全一致的模型和优化器，但不承诺跨平台或规则变更后逐位复现。

热身每场采集及每轮更新都保存进度，中断后继续剩余部分。新平衡赛程 checkpoint 为 schema 3；既有 schema 1/2 仍按原参数和四局赛程恢复，不补加热身或互换配点。导出模型的训练分片和配置组包含实际用于热身的数据，评估也会排除这些配置。模型引用的训练报告另存于 `reports/<摘要>.json`，包含 PPO 报告及启用热身时的教师/超参数/进度。

## 对战评估

```sh
sactl ai evaluate --environment ./environment.json --model ./ai-data/models/<摘要>.json --output ./evaluation.json --matches 1000 --opponent basic --opponent focus
```

上例用于未绑定实验的探索模型。场数按每个对手计，新评估须为 8 的倍数；`--opponent-model` 可比较另一个冻结网络。完整评估场景在对局前生成，排除所有待比较模型的训练组。它没有事先区分验证与最终测试，不能把多次探索后最好的成绩当独立最终测试。输出真实结算、正常完成局的平均得分和按配置组重采样的区间，截断另外计数；不足 20 个配置组不报告区间。胜率提升还需要预先固定足够规模的评测和完整竞技链路验收，少量测试或 loss 降低不能替代。

旧 `train/evaluate --database` 保持为 v1 SQLite 线性训练/预测入口，两种方法参数不混用。

## 固定战斗策略后搜索配点

`sactl ai build-search --environment ./environment.json --data-dir ./build-search-data --model ./model.json` 固定战斗模型，在同整数预算下搜索人物/宠物配点，使用独立验证与原生最终复验。评分器不读隐藏敌方配点，不修改线上角色；支持中断恢复并保存双方轨迹。完成后用 `build-pool --search-dir` 冻结多个队伍阵容，`experiment --build-pool --from-model` 声明下一轮，`train --from-model` 从父模型权重开始、使用新优化器训练。`export-model --data-dir` 可从已有 checkpoint 重新导出而不训练。来源隔离、恢复、搜索规模和报告区间见 [配点搜索说明](learned-build-search.md)。

`experiment --pool-train-groups N --train-groups T --build-pool ...` 可显式混合 N 个池内训练家族和 T−N 个新生成家族，要求 0 < N < T；省略时保持纯池训练。新建清单均匀交错两类训练家族，每个家族的八场配对保持完整；旧清单/恢复保持原顺序。清单独立保留验证/测试来源并采用新 schema，旧版本拒绝。该比例是家族数量，不是梯度权重，也不证明强度收益；与跨人数 `experiment-mix` 的区别及完整示例见配点搜索说明。

v0.2.10 `build-validate init/run/verify/compare` 可在同一补充清单中比较父子模型使用均衡和已选配点的效果。先冻结清单再观察子模型成绩，保留源搜索及双方报告的 `.data`；统计按完整对手阵容聚合，有截断不估计收益，小于二十个阵容不给区间。该报告不替代原实验的 validation/test，不自动晋级；详见 [配点与决策收益](learned-build-search.md#分开验证配点收益与决策收益)。

## 治疗与换宠教师

`sustain` 是本地训练/评估的固定规则教师，不是第五种在线 AI 策略。它以 focus 集火为基础，依据己方已知 HP、合法候选及已核实效果，安排单体/群体治疗或有限道具；队伍共享本回合的名义恢复计划，避免重复治疗。无法通过已计划治疗保住低血宠物时，选择已知可召唤、健康且有攻击技能的备用宠物。该回合的宠物指令仍属于原出战宠物。未知 HP、敌方、备用实体不会成为治疗目标；名义恢复量不代表引擎保证成功。

新实验默认五种热身教师和规则对手；可用 `--warmup-teacher sustain` 明确选择教师，并用重复的 `--rule-opponent sustain --rule-opponent focus` 选择 PPO 的规则对手池。这些参数随 checkpoint 固定，恢复时不能覆盖。历史 checkpoint 没有 rule_opponents 字段时继续原四种对手调度，旧教师身份和决策不变。评估与配点搜索通过 `--opponent sustain` 使用该对手，或 `--policy sustain` 固定搜索控制器；默认评估/搜索也包含第五种对手。已锁定的旧最终测试必须沿用原对手集合，不能用新的默认集合改写。

示例（v0.2.10，使用新的实验目录）：

```sh
sactl ai train --environment ./environment.json --data-dir ./sustain-training --reserve-pets 2 --healing-magic 20 --healing-items 6 --warmup-teacher sustain --rule-opponent sustain --rule-opponent focus --batches 10
```

这为热身和对手覆盖提供更多行为，不是 sustain 或 learned 的胜率认证。

## 可选控制教师

`control` 是可显式选择的训练教师/规则对手，在线策略仍为 basic、learned、llm、hybrid。它保留 sustain 的治疗、道具和换宠规划，优先集火未被硬控制的低绝对 HP 敌人；宠物尝试对其他目标施加石化/睡眠，或使用混乱/毒攻击。每个目标每回合最多安排一次状态尝试，已有公开异常状态、未知 HP、濒死目标不再安排控制。石化/睡眠不安排到已计划攻击的目标，避免加防或打醒；双方速度、成功率和状态持续时间仍由引擎结算，规划不保证控制成功。

只通过 `--warmup-teacher control`、`--rule-opponent control`、`evaluate --opponent control` 或 `build-search --policy control/--opponent control` 启用。默认教师、规则对手、评估和冠军门槛仍为原五种规则；已有 checkpoint、规则身份和冻结评测不变。可重复参数明确组合多个规则，不能在恢复时改池。比如在新目录开始一个实验：

```sh
sactl ai train --environment ./environment.json --data-dir ./control-training --reserve-pets 2 --healing-magic 20 --healing-items 2 --warmup-teacher control --warmup-teacher sustain --rule-opponent control --rule-opponent sustain --rule-opponent focus --batches 10
```

控制示范、实际状态生效和独立比赛优势是不同证据。此教师用于补充战术覆盖，不代表它或经它训练的模型更强。

## 主宠技能组合与忠犬

```sh
sactl ai experiment --environment ./environment-v8.json --output ./guardian-experiment.json --pet-skills 1,2,3,60,80,110,20 --reserve-pets 1 --train-groups 64 --validation-groups 20 --test-groups 20
sactl ai train --environment ./environment-v8.json --experiment ./guardian-experiment.json --data-dir ./guardian-training --batches 10
```

`--pet-skills` 为双方每只主宠固定技能集合；不传保持原七技能，最多七项，拒绝重复与未知 ID。20 是忠犬：降低名义攻击力、攻击所选目标并保护自己的主人，不是任选队友保护。其余已审计 ID 为 1 攻击、2 防御、3 破防、60 毒、80 石化、90 混乱、110 催眠。备用宠物仍带攻击、防御，加一项来自该集合的专长；没有专长时仅有攻击、防御。技能按原生固定词表顺序分配槽位，命令行顺序不改变实际集合。

该参数也用于新建 train、evaluate、build-search；绑定实验、继承配点池或恢复后不可覆盖。配点搜索固定主宠技能集，继续搜索人物/宠物配点及备用专长；含显式技能配置的备用评分输入保留第八个技能类别。模型建议不会修改线上宠物。训练工具包尚未发布，现有旧下载包会拒绝新参数，不得使用旧工作程序忽略该配置。
