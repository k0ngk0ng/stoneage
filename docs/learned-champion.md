# 本地模型晋级与回退（开发版）

`sactl ai champion` 管理受控原生环境的本地冠军。门槛先冻结，挑战使用候选实验的完整最终测试集，通过后自动更新本地冠军，失败则保留旧冠军。它不修改队伍配置，不给模型授予完整线上竞技场认证；产物仍是 candidate。队伍可显式配置 `champion_directory` 在每次新排队前选择通过该原生门槛的模型。真实联网操作、截止时间与完整规则覆盖还需要另外验收。

## 创建固定门槛

```sh
sactl ai champion init --directory ./champions --experiment ./experiment.json
sactl ai champion status --directory ./champions
```

创建时从 experiment 固定引擎规则/平台、人数、人物/宠物预算、等级、备用宠物、治疗条件及回合上限。门槛不能事后修改；下一位候选可以来自新的实验，但这些比赛条件必须相同。目录开始没有冠军，不把未经评估的初始模型默认设为冠军。

新实验默认每个对手至少 256 个独立配置家族，每组两 seed × 两种配点归属 × 左右场地共八场；五种规则对手为 basic/focus/guard-break/defensive/sustain，已有冠军时再加该冻结模型。因此默认首次完整挑战至少 10,240 场，后续至少 12,288 场。创建注册表不运行这些比赛，只有 challenge 才采集。

新注册表为 `native-champion-registry-v2`，conditions 固定 `pairing=roster-side-v1`；旧 v1 注册表和旧实验继续原四局赛程与场数。挑战实验必须匹配注册表的 pairing，不能把八局实验交给四局门槛或编辑旧注册表升级。新注册表的次数预算只适用于该预先声明的实验过程，不能反复建表来重试已经曝光的最终测试；已有跨实验的数据来源与测试曝光限制仍适用。

可在 init 时设置 `--min-groups`（20..4096）、`--alpha`（大于 0 且不高于 .05）、`--rule-score`（至少 .5）、`--champion-margin`（0...05）。这些参数用于预先规定实验，不应用已看到的测试结果反复调整。较小家族数会使置信下界更保守，不代表更容易通过。

## 选择候选并挑战

混合模型使用相同入口，改传 `--mixed-experiment`：

```sh
sactl ai champion init --directory ./mixed-champions --mixed-experiment ./mixed.json
sactl ai champion challenge --directory ./mixed-champions \
  --environment ./environment.json --mixed-experiment ./mixed.json --model ./candidate.json
```

新目录使用 `native-mixed-champion-registry-v1`，同时冻结全部声明模式的场景条件。规则对手与上述单模式门槛相同，每个模式都须满足最小配置家族数；默认两个模式的首次挑战是 20,480 场，五个模式是 51,200 场，有旧冠军时还须逐模式与该冠军交手。创建目录不运行挑战，不因某个模式通过就采用部分结果。

全部模式共享一次挑战编号和 alpha：先按 `1/(n*(n+1))` 分配本次预算，再均分到模式及其对手。结果的 `assessment.bounds[].mode` 标明人数；所有下界都须通过，任一模式含截断、证据不完整或不达标，整个候选均不晋级。不能用平均成绩掩盖某个模式退步，也不能将每模式的普通配对区间当作联合晋级结论。

报告按模式保存在 `evaluations/<attempt>-mode-N.json` 及其 `.data`。中断后恢复同一挑战，已完成的模式直接核验，尚未完成的模式保留旧分片并按原配置重新评估；全部报告核验结束后才原子提交一次事件。失败与放弃保留所有模式的测试曝光，回退不重置挑战次数。最终选择约束与 `evaluate --mixed-experiment --split test` 共用：整个混合实验固定一个候选，各模式对手分别固定。

同一混合冠军目录可供其声明人数的不同队伍配置使用，全部加载同一产物；每场仍固定模型，恢复依赖队伍自身 state_dir 内的副本。不同队伍须使用不同 state_dir。旧客户端拒绝新登记册；单模式登记册保持原有格式和行为。该门槛仍只说明受控原生测试通过，不等于真实玩家或任意装备场景认证。

先用 validation 选择候选。候选固定后执行：

```sh
sactl ai champion challenge --directory ./champions \
  --environment ./environment.json --experiment ./experiment.json --model ./candidate.json
```

挑战冻结候选、旧冠军和完整测试配置，并复用 `<experiment>.test-selection.json`，与普通 evaluate 的最终选择约束一致。不能已经用一位模型看过同一实验的最终测试，再换另一位模型或对手集合。

每场保存双方原始轨迹；完成后复核赛程、来源、模型与策略动作，再计算晋级结论。终端最终事件 `champion_assessed` 中的 `result.assessment.passed` 表示是否通过。正常完成但未通过时命令可以成功退出，自动化程序必须读该字段，不能把退出码 0 当作晋级。没有通过的新候选不会替换旧冠军。

统计使用配置家族的平均得分（胜 1、平 .5、负 0），不把组内八场（旧赛程四场）当独立样本。对每个对手使用单侧 Hoeffding 下界，并在同一注册表的第 n 次挑战分配总 alpha 的 `1/(n*(n+1))`，再分摊到各对手，控制多次尝试和多个比较带来的误判。默认各规则对手的下界必须超过 .5，对旧冠军必须超过 .45。该保证以声明实验分布中独立采样的配置家族为前提，不代表对所有真人、装备或战术都有相同胜率。

任一采集截断/不完整比赛都会拒绝晋级，对该对手不显示伪造的完整平均得分或下界。不把超时算成负局，不丢弃失败/截断场次来挑结果。

## 中断、放弃和回退

中断后运行同一条 challenge。已经完成的报告直接核验后提交；尚未完成的评估按相同冻结配置重新比赛，保留旧原始分片。尚未提交的挑战会占用注册表的 pending 位置，不能用另一个候选顶替它。

```sh
sactl ai champion abandon --directory ./champions --reason "停止这个实验"
sactl ai champion status --directory ./champions
sactl ai champion rollback --directory ./champions --to <此前通过的事件摘要> --reason "恢复此前模型"
```

放弃也保留测试曝光记录并消耗一次挑战编号。后续挑战不能重用已完成或已放弃挑战中的测试配置家族；换 seed、左右换边或换文件名不会使它重新独立。失败、放弃和回退都不重置统计预算。

status 的 `events` 与 `event_ids` 顺序对应。回退只能选择当前历史中已经通过门槛、且模型与当前不同的挑战事件，不能指向任意模型文件或未通过的候选。有 pending 挑战时先恢复或放弃，才能回退。该操作只更新本地原生冠军指针；已开始的比赛不换权重。

## 数据与核验

- `registry.json`：固定条件、规则策略身份和门槛。
- `models/`：本地模型副本，不依赖原路径。
- `attempts/`、`reservations/`、`selections/`：冻结挑战、待处理位置和最终选择。
- `evaluations/`：最终测试报告及对应 `.data` 原始证据。
- `events/`：不可变挑战/失败/放弃/回退事件；`champion.json` 是原子更新的当前事件指针。

保留整个目录。status、挑战和回退会重验已提交报告与原始策略轨迹，耗时随历史增长，支持 Ctrl+C。写入操作通过同一注册表文件锁串行化；不要删除锁文件强行并发。未提交的孤立事件不能成为冠军。完整性核验不等于外部服务端签名，不防止人工重造整套实验记录。

## 队伍逐场选模

在现有 team.json 中保留成员、state_dir 等设置，改以下字段，并删除原 `model` 字段：

```json
{
  "schema_version": 2,
  "strategy": "learned",
  "champion_directory": "./champions"
}
```

这是字段片段，不是完整队伍配置。路径相对 team.json 所在目录；`hybrid` 同样支持，继续使用原 `llm` 配置。固定 `model` 文件的旧 schema_version=1 配置仍可用。新选模配置必须使用 schema_version=2，旧客户端会在连接成员前拒绝，不能把版本改成 1 来绕过。

`sactl ai check --config ./team.json` 只读核验登记册及模型，输出 `selection`，不登录或排队。正常使用仍是 `sactl ai run --config ./team.json --forever`。登记册为空、证据损坏、人数不符时拒绝；check 不核验实际服务器，run 在排队前还会查询每名成员所连服务器的规则和平台。

每次全队空闲并准备好后，程序重验登记册的已提交历史，读取当前冠军，包括已提交的回退结果。验证可能耗时，发生在排队前。晋级评估尚在进行时，使用此前已通过的冠军；首次挑战未完成、尚无冠军时不能排队。运行中不改配置文件，也不在排队、倒计时或比赛中换模型。

排队前，将模型完整副本和选择信息原子保存到 `state_dir/arena.sqlite3` 的 `commander_models` / `commander_selection`；同一模型只保存一次。`queue_model` 和 `match_model` 记录包含模型摘要、登记册事件、原生评估条件和 `arena_certified=false`。保留此数据库及其正常 SQLite 文件，不要只保存 team.json。

重启优先恢复排队前的本地副本，不依赖登记册和原模型文件仍可访问；hybrid 同时核对 LLM 配置版本。已有比赛必须沿用固定策略版本，排队请求结果不明时也保留原选择并重试原请求。没有已保存选择时，不接管既有排队或比赛。恢复时更改策略、登记册路径或 hybrid 的 LLM 配置会被拒绝，应先用原配置完成当前会话；另起配置时使用独立 state_dir，不能删日志来绕过比赛版本检查。

下一次新排队时若登记册不可用、模型或服务器不兼容，程序报错停止，不悄悄沿用旧冠军或 basic 排队。场内单次推理的既有回退机制仍独立记录。这里的“已通过”仅指登记册声明的受控原生场景；其等级、预算、技能等条件随 selection 输出，不能据此声称线上所有阵容、技能或人数已认证。完整线上执行认证与真实晋级模型的端到端选模验收仍需完成。
