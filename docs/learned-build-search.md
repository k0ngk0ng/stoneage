# 本地配点搜索（开发版）

`sactl ai build-search` 在隔离原生引擎中比较同预算整数配点，产出结构化建议，不修改线上角色。它是 learned 完整方案的配点部分，当前仍使用受控人物/宠物的攻击、防御、破防、状态、有限治疗/道具及换宠场景；不能视为完整游戏装备、成长和技能环境的最优配点。尚未随正式客户端下载发布。

## 两个模型的职责

- 战斗模型：已有 `commander-policy-v2`，逐回合输出整个队伍的行动计划。搜索期间固定权重，也可以固定使用 basic 等规则策略。
- 配点评分模型：`build-score-mlp-v1`，输入包含己方每名人物、初始宠物的四项点数比例；每只备用宠物另有四项比例及七个技能类别位，两个 64 宽度隐藏层，输出 0～1 的场景得分预测。规则、等级、预算、双方战斗策略及对手分布固定在搜索清单中。它不读取对手本局隐藏配点，也不是经过校准的胜率预测器。

评分器使用真实原生对战的平均得分拟合，帮助筛选待实测的候选。每代从已完成的搜索组重新拟合，验证和最终测试结果不进入评分器；不会拿预测分数直接宣布某个配点最强。

## 实战来源的间接影响

开发版支持示范模型及其原生后代参与搜索。必须匹配实际规则、平台、人数和特征要求；纯示范模型不需要伪造原生场景标签。比如用实战模型作对手、basic 作固定指挥：

```sh
sactl ai build-search --environment ./environment.json --data-dir ./recorded-search \
  --mode 5 --policy basic --opponent-model ./recorded-model.json
sactl ai build-pool --search-dir ./recorded-search
sactl ai experiment --environment ./environment.json \
  --build-pool ./recorded-search/build-pool.json --output ./selected-experiment.json
sactl ai train --environment ./environment.json \
  --experiment ./selected-experiment.json --data-dir ./selected-training
```

示例省略 --from-model，从零开始训练作战网络，但配点仍受到实战模型影响。`recorded_selection_artifacts` 在搜索、池、实验和子模型中保留这些来源摘要，包括指挥与对手以及它们继承的间接来源；CLI 的完成事件也返回此字段。搜索恢复、报告与导出池会从 search-policies 的模型副本重建来源检查，不能仅凭一份摘要表消除标记。

这类子模型采用文件 schema 5，直接梯度继承仍单独保存在 recorded_training，不能把配点影响写成示范训练或虚构父模型。下一代训练和配点搜索继续保留两类来源。模型评估的 unverified_source_artifacts 包含有这类影响的候选/对手，因此不能宣称已经排除未知实战源配置重叠，也不能自动晋级。旧纯原生模型和搜索产物不补写该字段，原有哈希不变；旧客户端不支持的新字段/格式会拒绝。

## 备用宠物

`--reserve-pets 1` 或 `2` 为每人加入额外宠物。搜索保持各宠物自己的点数预算，整数变异可改变备用宠物配点及一种专长技能（攻击/防御始终保留）。评分器不读取敌方隐藏阵容。导出的配点池和后续实验保留每只备用宠物的完整配置。默认 0 保持单宠实验。

新 v8 环境可用 `--pet-skills 1,2,20` 固定主宠技能集合（此例为攻击、防御、忠犬），专长从这个集合中抽取，仍保留备用宠物的攻击/防御。每只宠物最多七槽，支持的技能编号及实际忠犬语义见 [训练入口](learned-training.md)。搜索不改变已固定的主宠技能集合。显式集合的评分器每只备用宠物使用 12 列（四项配点、八个技能标记），5v5 两只备用宠物时最多 160 列；省略参数保持旧版 11 列及默认行为。恢复、配点池和后续实验继承这项配置，不允许覆盖。

## 运行

先准备 [训练入口](learned-training.md) 所述的原生环境文件，再选固定战斗策略：

```sh
# 固定 learned 模型，默认与五个规则对手比较。
sactl ai build-search --environment ./environment.json --data-dir ./build-search-data --model ./model.json

# 也可固定规则策略，先检验配点本身的作用。
sactl ai build-search --environment ./environment.json --data-dir ./build-search-basic --policy basic --opponent basic

# 中断后不再指定模型、点数或搜索设置，复用固定副本。
sactl ai build-search --environment ./environment.json --data-dir ./build-search-data --resume
```

`--model` 与 `--policy` 互斥，不指定时使用 basic。`--policy` / `--opponent` 支持 basic、focus、guard-break、defensive、sustain，以及可选的 control；默认对手仍为原五种，control 必须显式指定。`--opponent` 和 `--opponent-model` 可重复。网络的规则摘要、平台和人数模式必须匹配引擎。模型文件以内容摘要保存到搜索目录，恢复不重新读取原始模型路径，换策略须新建搜索。

默认 1v1、人物与宠物各 120 点、等级 35，每项最少 1 点，`--pet-points 0` 禁用宠物。可用 `--mode 2`～`5` 搜索整个队伍，每名人物和宠物各自满足预算，不能在队员之间转移点数。这里是受控引擎的初始条件；与普通新建角色的 20 点、单项允许 0 的规则不同，不可直接作为线上角色创建参数。

默认先实测 16 个配点，随后 3 代，每代筛选 128 个候选、实测 4 个；最终保留 4 个配点进入验证。每个对手配置组包含两个战斗 seed 和双方换边，共 4 场。默认搜索/验证/最终测试分别使用 4/20/40 个互不重叠的对手阵容；五个规则对手时最多约 5,440 场原生对战，实际数取决于最终是否保留均衡配点。

先做小规模通路验证可显式缩小参数，例如：

```sh
sactl ai build-search --environment ./environment.json --data-dir ./build-search-smoke --policy basic --opponent basic --points 20 --pet-points 0 --level 10 --max-turns 300 --initial-candidates 5 --generations 1 --proposals 8 --native-candidates 1 --finalists 2 --fit-epochs 2 --search-groups 1 --validation-groups 1 --test-groups 1
```

该规模只用于检查执行链路，不足以得出稳定的配点优劣。参数意义以 `sactl ai build-search --help` 为准；上限限制单次搜索至多 200,000 场。

## 搜索与复验约束

1. 初始候选包含均衡、四种偏科和随机阵容。后续通过同一行动者两个属性间的整数转移生成候选，并保留随机探索；无需四舍五入或事后补点。
2. 评分器从预测较好的候选中筛选，多个实测名额时保留一个探索名额。所有筛选结果都要经过真实引擎对战，才能成为下一代的训练标签。
3. 验证阵容在开始搜索时固定，与搜索阵容完全分开。验证候选保留均衡对照、实测最好的方案及尽量有差异的配置；得分完全相同时优先均衡，不为显示“优化”强行换配点。
4. 验证结束后固定一个最终候选，再在保留测试阵容中同时测试候选和均衡对照。测试结果不能改变已选方案。双方使用相同的对手、种子及换边计划；不同动作仍会改变引擎 RNG 消耗，不承诺相同随机事件。
5. 已知战斗模型训练过的完整配置组不能进入保留验证/测试；遇到重叠时报错，不替换失败配置来凑分数。对手阵容的分布是实验假设，不代表真实竞技场玩家分布。
6. 胜=1、真正平局=.5、负=0；若发生采集上限截断，保留事实并停止这次搜索，不把截断计成输赢、拟合标签或建议。需在新搜索中提高 `--max-turns`，不能修改原实验后继续冒充同一个结果。

## 数据、恢复与报告

每场先发布双方 gzip 轨迹及校验 manifest，再提交进度；这些是实际规则/贪心网络行为数据，不能直接混进当前采样策略的 PPO 更新。目录包括：

- `shards/`：双方完整观察、候选、动作、历史和终局。
- `search-policies/`：固定的战斗模型副本。
- `search-objects/`：不可变规则/配置清单、单场结果、结果列表和每代评分器；按摘要复用。
- `search-states/`、`search-latest.json`：小型进度与当前原子指针，不每场重复保存全部历史或模型权重。
- `build-report.json`：最终建议、验证/测试的原始赛程与统计、评分器训练来源、相对均衡方案的得分差及区间。

同一目录只允许一个写入进程，进程退出自动释放锁；不要手动删除锁文件并发写入。Ctrl+C 保留已完成的场次和既定候选，正在进行的比赛舍弃。恢复校验环境、策略、完整配置、对象摘要和原始轨迹，不能凭一份汇总分数续跑。

报告 `status=candidate-advice`，不会晋级战斗模型或给线上角色加点。`scores` 按对手分别记录胜负平、回合和配置组统计；最终 `delta_from_balanced` 比较平均得分，至少 20 个测试阵容才报告配对组 bootstrap 95% 区间。区间跨 0 时不能声称有稳定提升；一个实验中领先也不代表存在万能配点。

## 冻结配点池与下一轮训练

完成搜索后，导出进入验证阶段的多个完整队伍阵容；不只保留测试得分最高的一个，也不把旧排名作为新战斗策略的监督标签：

```sh
sactl ai build-pool --search-dir ./build-search-data
sactl ai experiment --environment ./environment.json --build-pool ./build-search-data/build-pool.json --from-model ./parent.json --output ./next-experiment.json --validation-groups 256 --test-groups 256
sactl ai train --environment ./environment.json --experiment ./next-experiment.json --from-model ./parent.json --data-dir ./next-training --batch-matches 32 --batches 10
```

`build-pool` 先验证完成状态、原始轨迹和来源摘要，再写不可变 `build-pool.json`；`--output` 可指定新路径。池中每项是完整队伍的人物/宠物配点，预算仍属于各自行动者。池固定人数、点数和等级，experiment 不允许覆盖这些参数；默认沿用采集回合上限，也可显式另设 `--max-turns`。

训练默认覆盖池中所有不重复的阵容对（含镜像），n 个阵容对应 n×(n+1)/2 个训练家族。`--train-groups` 可选其中较小的子集，不能凭增加该参数虚构新阵容。验证/测试双方使用池外的新阵容，并排除祖先训练、配点选择及历史保留评测家族；每个家族仍有两个种子和双方换边。

若希望同时保留更广泛的配点分布，显式设置 `--pool-train-groups`，并用 `--train-groups` 指定训练家族总数。例如三个池内阵容共有六个阵容对，以下命令冻结六个池内家族及二十六个新生成家族：

```sh
sactl ai experiment --environment ./environment.json --build-pool ./build-search-data/build-pool.json --from-model ./parent.json --pool-train-groups 6 --train-groups 32 --validation-groups 256 --test-groups 256 --output ./mixed-build-experiment.json
```

池内数量须大于零、小于训练总数且不超过池内可用阵容对；省略该参数时保留原来的纯池训练行为。新生成训练家族的双方均在池外，并排除历史训练/选择来源；验证和测试继续使用独立的新家族。新建混合清单会均匀交错两类训练家族，保留各类内部顺序及每个家族完整的八场配对。例如 6/32 家族、每批 128 场时，每批含 24 场池内对战及 104 场新配点对战，避免隔批完全没有池内样本。旧清单及其恢复严格沿原顺序执行，不自动重排；新顺序参与实验摘要。

比例表示家族数量，不是梯度权重或学习视角比例；不同比赛的长度和对手策略会改变实际学习回合数。很小的批次仍可能只含一类，批次数太少也可能尚未覆盖所有家族。交错排列不保证胜率提升。

混合清单使用 `commander-pool-mix-experiment-v1` 和 `pool_training_groups`，旧读取器须拒绝，不要删除字段或改写 schema 绕过。该命令在本地直接执行，不通过游戏后台进程；恢复仍使用原清单和训练检查点。它与跨人数的 `experiment-mix` 是不同维度：前者混合配点来源，后者组合人数模式。

`--from-model` 初始化父模型权重，创建新的 Adam 优化器；默认关闭模仿热身，显式 `--warmup-matches` 才重新启用。父模型不修改，新训练目录保存其摘要命名副本。后续 `train --resume` 从已保存权重、优化器和来源继续，不再传 `--from-model` 或 `--experiment`。也支持只用配点池从零训练，或只用父模型在新实验上微调。

训练初始化允许父模型来自另一人数模式；目标人数取自新实验或配点池，子模型只声明该目标人数，仍须独立评估。这个迁移许可不适用于直接 `build-search --model` 或对战评估：固定控制器/对手仍必须已经声明对应人数的训练覆盖。

候选模型保留祖先训练分片、训练家族、配点选择家族、父模型摘要和本实验的保留评测家族。本实验的保留家族可用于自己的验证/测试；进入下一代时转为预留来源，不能再作为下一代独立评测。来源是保守的污染边界，不意味着每个家族都参与过梯度更新。旧绑定实验的模型缺少保留家族时，需从原训练目录重新导出：

```sh
sactl ai export-model --data-dir ./parent-training
```

默认生成新的摘要命名模型，恢复已保存的实验来源，不训练、不改变权重、不覆盖旧文件。只有推理模型而没有原 checkpoint/实验的旧产物，不能靠手改 JSON 补来源。祖先分片只保留摘要引用，尚未复制到子目录；须保留父训练目录、源搜索目录及其原始轨迹，不能在清理时当缓存删除。

新模型仍先在对应 validation 上选择，再固定候选执行 test；之后可用这个模型开一个新 `build-search` 目录，继续下一轮。这里提供显式交替入口，没有自动共同进化、冠军晋级或线上认证。每一轮的规则、动作覆盖和胜率都需要独立验收。

## 分开验证配点收益与决策收益

开发版 `sactl ai build-validate` 对同一批新对手比较“父模型/子模型 × 均衡配点/已选配点”。它是额外的验证分析，不替代原训练实验的验证/最终测试，也不自动晋级。清单绑定完整源搜索、配点池与父子训练实验；已选配点只取源搜索的验证阶段选择，不按源最终成绩重新挑选。父模型必须支持该人数模式。

先冻结训练实验，再在观察子模型成绩前创建补充清单。以下接续上面的混合配点示例：

```sh
sactl ai build-validate init --search-dir ./build-search-data --experiment ./mixed-build-experiment.json --from-model ./parent.json --groups 32 --seed 2701 --output ./allocation-validation.json
sactl ai build-validate run --search-dir ./build-search-data --manifest ./allocation-validation.json --model ./parent.json --environment ./environment.json --output ./parent-allocation.json
sactl ai train --environment ./environment.json --experiment ./mixed-build-experiment.json --from-model ./parent.json --data-dir ./next-training --batch-matches 32 --batches 10
sactl ai export-model --data-dir ./next-training --output ./child.json
sactl ai build-validate run --search-dir ./build-search-data --manifest ./allocation-validation.json --model ./child.json --environment ./environment.json --output ./child-allocation.json
sactl ai build-validate verify --search-dir ./build-search-data --report ./child-allocation.json
sactl ai build-validate compare --search-dir ./build-search-data --baseline ./parent-allocation.json --candidate ./child-allocation.json
```

两模型面对相同的对手阵容、规则、种子和场地；左右换边时各自保留配点，不交换配点归属。每个模型的计划场次为 `groups × 对手规则数 × 8`，包含两种己方配点。新对手避开源搜索已知阵容及新实验的全部阵容，组合家族排除祖先来源和已保留评测家族。数量是独立对手阵容数，不能把重复种子、换边或双方视角当成额外独立样本。

`run` 中断后重复完全相同的命令并加 `--resume`；保留报告旁的 `.data` 目录与源搜索目录。恢复先校验两分支全部已有数据，再补记写入中断时的有效分片索引和继续采集；不允许改模型、清单或场景。实际对战使用保存后重新加载的模型快照。`verify` 和 `compare` 只读，不启动战斗引擎，但会读取原始分片核对来源、动作、价值和概率，较大数据集需要一定时间。

比较固定输出以下五项差值，正值表示前者得分较高：

| 字段 | 定义 |
| --- | --- |
| `joint_gain` | 子模型选中配点 − 父模型均衡配点 |
| `policy_on_balanced` | 子模型均衡配点 − 父模型均衡配点 |
| `policy_on_selected` | 子模型选中配点 − 父模型选中配点 |
| `allocation_under_parent` | 父模型选中配点 − 父模型均衡配点 |
| `allocation_under_child` | 子模型选中配点 − 子模型均衡配点 |

以完整对手阵容聚合全部规则、种子和场地，固定 10,000 次配对 bootstrap、seed 8461，报告名义 95% 区间及五项共同 Bonferroni 校正区间。少于 20 个对手阵容时仅给差值，不给区间；任一分支存在截断时，五项差值及区间均不输出，原因标为 `censored_outcomes`。不能根据单个未校正区间宣布提升，配点收益也不等于决策模型提升。存在 `unverified_source_artifacts` 时，保留实战来源无法完全核验独立性的限制。

此功能在本地直接执行，不通过游戏 daemon，不修改线上角色。报告使用独立 schema，普通 `verify-evaluation`/冠军入口不接受；应使用 `build-validate verify`。旧版不认识该命令时应更新客户端，不能改写报告 schema 绕过。当前为开发版能力，是否已安装以 `sactl ai build-validate --help` 为准。
