# 战斗记录与同点数 PK 数据生成

目标是学习 **相同总点数下的加点方案和 PK 决策**。本版交付数据采集、真实引擎批量对战和训练导出，尚不训练或部署模型。

## 生产记录

GMSV 统一记录 PVE、PVP（包括组队）；观战不重复记录。宿主机
`STONEAGE_BATTLE_RECORD_ROOT` 默认 `./data/battle-records`，独立挂载到
`/var/lib/stoneage/battle-records`，不会随容器更新或游戏资源同步被替换。

```text
battle-records/
  status.json                  # 写入器心跳、错误次数、剩余空间状态
  rulesets/<SHA-256>/           # 二进制摘要、战斗数据表及其摘要
  <UTC日期>/<随机进程ID>-<局号>/
    metadata.json              # 格式/规则/程序版本、PVE/PVP、双方人数、实验条件
    events.jsonl               # 递增序号的结构化回合轨迹
    result.json                # 服务端胜负、结束原因、完整性；完成后原子发布
```

文件权限 0600，目录 0700。只读取战斗所需的白名单数值字段及随机角色 ID；不记录姓名、账号、密码、IP、聊天或配置密钥。公开仓库和 Release 中不包含实战数据。

采集不调用游戏随机数，不执行额外战斗操作，不重复生成有副作用的 BC 报文。主线程只复制有界数据，独立线程写盘。队列满时保护游戏运行，受影响对局标记丢失；进程崩溃时缺少 `result.json` 的对局视为不完整。关闭对局前同步轨迹，再发布结果。默认可用空间不足 1 GiB 时暂停写入，记录错误，不自动删除数据。可通过 `STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES` 调整预留量；应纳入宿主机磁盘监控。`status.json` 每约 5 秒刷新。

完整对局同时满足：`trajectory_complete=true`、`storage_complete=true`、
`dropped_events=0`，且 metadata、events、result 的序号连续。导出器逐局重新验证这些条件，不能仅信任完整性标志。存储不可写时游戏继续运行，应检查 GMSV 日志及 status 心跳；规则归档失败时明确禁用本次进程的记录。

## 字段与训练语义

- `observation` / `visible_actor`：来自实际发给该玩家的 BP/BC，使用战斗槽位 `bid`；保留双方公开血量、等级、状态、骑乘信息，去除名称。
- `own_actor` / `allocation` / `item` / `pet_skill`：该玩家自身及携带宠物的属性、加点、装备/物品、技能候选。通过 `observation_id` 关联到同一观察，`observation_end` 表示观察完整。对手私有字段不会进入本方 observation。
- 加点顺序是体力、力量、耐力、敏捷；`raw_units_per_point=100`，`budget_raw=四项原始属性之和+未分配点数×100`。它代表当前实际属性预算，**不声称是历史手动加点次数**，因为转生、道具等也可能改变属性。最终攻防敏单独保存。
- `action_request`：提交动作的操作码和数值参数，关联决策时的 observation；区分客户端提交与原生默认回退。`action_dispatch_state` 保存该次处理后的选择状态，原生 void 分发器没有精确接受返回值，不能将其伪装成 accepted。
- `execution_command`：该回合进入结算前的人物/宠物选择，包含默认动作与服务端调整；`resolved_actor` 保存结算状态。这些是服务端标签，不能并入决策前输入，也不代表每个选择都成功生效（死亡、控制、目标失效会改变结果）。
- `turn` 在观察/提交中是已完成回合数；第 N 次结算的 `decision_turn=N-1`。结果中的 turn 是结算总回合数。
- 未知控制来源的 `policy_version=null`，不会猜测是人类还是某个模型；离线实验另存生成器和策略编号。未枚举完整合法动作掩码，`legal_action_mask=null`；技能/道具的 field、target、cost 是候选条件，不能当作无条件合法。原始随机状态未记录，生产轨迹不承诺逐位重放。

规则归档仅保存战斗表、程序摘要和白名单数值设置，不复制 setup.cf。更改编译版本或表内容会产生不同规则 ID。不同规则 ID 默认不应混作一个固定环境训练。

## 批量同点数 1V1

使用已发布且已拉取的 legacy-runtime 镜像；入口不下载或构建镜像：

```sh
bin/stoneage battle-dataset --output ./data/battle-training \
  --matches 1000 --points 120 --level 35 --seed 42 --repeats 4 --max-turns 200
```

程序通过专用 CLI 在独立进程初始化合成人物，调用真实 `BATTLE_Loop` 和原生指令分发、伤害/速度/胜负计算。容器没有网络、没有真实账号/角色挂载、文件系统只读，仅训练输出目录和临时目录可写；不会登录生产服或执行生产存档、经验奖励及排名结算。并行生成可使用不同输出目录和 seed，各自独立进程，不共享旧引擎全局状态。

当前基准场景是 **同等级、同属性总预算、无装备、无宠物、不骑乘、相同元素/运气/魅力、满血满蓝** 的人物 1V1。每项至少分配 1 点，生成均衡、偏科与随机组合；每组重复多次并交换两边位置。双方使用同一种基准策略，轮换：

| 策略 ID | 行为 |
|---|---|
| 0 | 始终普通攻击 |
| 1 | 80% 普攻、20% 防御 |
| 2 | 每四回合防御一次，其余普攻 |

这是隔离加点影响的第一套基准，不代表带宠物、装备、道具的完整竞技环境，也不是已经训练好的强 AI。训练后的策略仍需在更多对手和条件下评测。达到回合上限标为 `turn_limit`，无胜者，属于截断样本，不能伪装成败局或正常平局。

实验记录 seed、对局/组合编号、重复次数、交换位置、点数预算、等级、策略编号、生成器版本。给定相同二进制、规则表和完整参数可重新运行基准；跨 libc/架构不保证 rand 序列相同。

## 导出训练样本

```sh
# 加点方案 → 胜负标签：自动要求同点数、相同已记录外部条件的 1V1。
bin/stoneage battle-export ./data/battle-training --format builds > builds.jsonl

# 逐回合策略样本：本方观察、提交动作、进入结算的指令、下一状态、终局奖励。
bin/stoneage battle-export ./data/battle-training --equal-points > transitions.jsonl

# 生产 PVE 用于辅助学习，保留模式标签，不混同为 PK 胜率。
bin/stoneage battle-export ./data/battle-records --mode pve > pve-transitions.jsonl
```

导出器只依赖 Python 标准库，逐局处理，错误/不完整记录默认排除并在 stderr 输出统计。
默认排除逃跑、超时、异常离场；`--include-abnormal` 可以显式纳入，但这些数据仍须单独处理。
`builds` 包含双方初始配置和可比性检查；观察到条件相同仍不能证明真人比赛受控，因此保留
`experimental_control_verified=false`，离线可再核对 experiment 参数。

`transitions` 的 observation 不含对方隐藏加点/道具和未来动作；reward 只在有权威胜者的终局给予 +1/-1，中途为 0，回合上限使用 `truncated=true`。
样本附带 `split_group` 和建议训练/验证/测试划分：同场所有回合、同一生成组合的重复和换边都归于同一组，避免泄漏。同一真人跨比赛的泛化评测还应按角色 ID 分组。

生产记录与离线记录共享格式。保留 metadata/result，完成的 events.jsonl 可在离线归档流程压缩为 events.jsonl.gz，导出器支持读取；不要压缩正在进行的比赛。挂载不是备份，按实际存储量安排目录级备份/迁移。
