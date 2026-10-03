# 战斗观察与指令

## 数据来源

```sh
sactl --json observe
sactl --json battle-log
```

`observe.data.Battle`：`Active`、`Turn`、`CommandReady`、`PlayerSubmitted`、`PetSubmitted`、`MyNo`、`MyNoKnown`、`MyMP`、`Participants` 等控制/观察字段。不要只凭 `Phase` 判断可以发指令；也不要把日志中的回合号当成命令就绪信号。

支持魔法身份扩展的版本中，`Magic[].IDKnown` 为 true 才可使用 `ID` 识别魔法；ID 与 MP 消耗、目标规则来自同一条 J 状态。`battle-state` 候选对应 `MagicID/MagicIDKnown/MPCost`。旧服务器缺少 ID 时不能凭装备图号或旧缓存猜测；卸下装备的短 J 状态会清除旧魔法。战报恢复量是原生电影报告值，实际 HP 可能受上限限制，以后续观察为准。

宠物 `Slot` 是持有位置（0～4），`SkillSlots/SkillSlotsKnown` 才是原生技能容量；`Transmigration/TransmigrationKnown` 表示转生。两项均已知且未转生时，超出容量的技能不会进入候选，直接 battle 命令也会拒绝；转生宠物仍受 0～6 的技能索引上限约束。未知容量或转生不能按真实零值处理，W 技能条目存在本身也不证明服务器会执行。完整 K 刷新可能表示替换宠物，会清除旧身份和技能，等待新 W/AI 状态；不要用旧技能缓存补齐。

支持 `CombatStatsKnown` 的版本中，`Player.CombatStatsKnown` 与 `Pets[].CombatStatsKnown` 表示当前人物/宠物已收到完整有效的属性状态前缀。字段缺失或为 false 时，不把默认零值当成已知攻击、防御、敏捷等属性；`HasStatus=true` 也可能只收到部分更新。重新选角、宠物槽位替换或不完整状态可能使标记失效。此标记不证明敌方隐藏属性已知，也不替代当前回合的 `Battle.Participants` 血量；宠物协议中的 MP 字段不代表存在可消耗的宠物气力玩法。

开发版竞技场战斗还提供 `Battle.LadderID`。竞技场使用 `arena result` 查看结算、`arena ack`
确认结算，不使用普通 `battle-end`。重连先读取 `arena status` 和 `observe`，已提交动作
以恢复后的 `PlayerSubmitted`、`PetSubmitted` 为准，不能重放断线前的动作。完整流程及
当前验收范围见 [竞技场玩家流程](../../../../docs/arena-player-flow.md)。

支持离场观察的版本中，`sactl --json battle-state` 的 `withdrawn=true` 表示人物已从竞技场参战表移出，仍在等待整场结算；候选列表为空。继续读取 `battle-events` 观察存活队友，不发送 N、宠物指令或 `battle-end`，也不把该状态当成断线。HP=0 但仍在参战表中的人物与离场不同，应以实际候选和就绪状态判断。Web 使用相同公共投影保留观战、关闭指令。

`battle-log.data.battles` 是从新到旧的战斗历史；这里的字段使用小写 JSON 名称，与 `observe` 不同：

- `id`、`started`（Unix 毫秒）、`turn`、`myNo`（未获知为 null）、`ended`、`result`、`trimmed`。
- `roster`：`id`、`name`、`level`、`hp`、`maxHp`、`player`、可选 `mp`/`maxMp`、`ride`、`petName`、`petHp`、`petMaxHp`。
- `logs`：`turn`、`kind`、`actor`、`target`、`damage`、`petDamage`、`flags`、可选 `hit`/`hits`、`text`、可选 `raw`、`recipient`/`guardian`。

支持承伤者投影的版本中，`attack`/`counter` 的 `target` 仍是原指令目标，`recipient` 是该次伤害或吸收恢复实际作用的战场槽位，`guardian` 是报文确认的保护者槽位；范围均为 0～19，不是持久编号。反射时 recipient 指向该次攻击者（反击也按反击者计算），同时存在保护与反射时仍保留 guardian。字段缺失表示未确认或不适用，零是有效槽位；闪避、伤害无效化不提供 recipient。旧客户端/旧记录可能没有这些字段，不能默认用 target 补齐。`battle-events` 的 `effects` 和 Web 结构化战报共用这些字段；这不改变普通 Web 界面。吸收仍须结合 flags 判断正负，不能只看 recipient 就扣血。

目前 `kind` 包含 `attack`、`counter`、`wait`、`pet_recall`、`pet_summon`、`start`、`end`、`unknown` 和部分原始标记（例如 `BD`、`BM`）。伤害字段不是统一的有符号体力差：治疗、吸收、闪避等不能仅按 `damage` 扣体力；未知动作的零值也不代表没有伤害。换宠事件的 actor 是主人战场槽位，target 是其宠物战场槽位，不是持有槽位或稳定 ID。不依赖中文 `text` 推导缺失语义，必要时使用后续快照；需要更完整的机器语义时报告接口差距。

`roster` 为最近服务端快照，不由日志推算即时体力。日志按结算顺序到达，可能早于动画。仅保留当前会话最近 20 场、每场 300 条，重新登录清空；无法读取另一个浏览器/sactl 会话的历史。

## 手动指令

CLI 的 `battle` 参数沿用原版协议命令，需要 shell 引号保护 `|`。例如默认目标攻击：

```sh
sactl --json battle 'H|FF'
```

只在最新观察允许玩家提交且 `PlayerSubmitted=false` 时执行。`H|FF` 使用默认目标，不代表指定敌人；指定目标和技能参数须以实际 CLI 帮助、已验证协议为准，不能猜编号。宠物提交是独立状态，只对当前可行动宠物发指令。

支持换宠校验的开发版中，`battle 'S|0'`～`'S|4'` 使用十进制持有槽位，`battle 'S|-1'` 收回当前宠物。优先使用最新 `battle-state` 的 `switch_pet` 候选；直接指令也要求已确认的出战槽位、待机掩码、可召唤掩码及骑乘状态，目标必须存活、同时在待机与可召唤掩码内且不是当前出战或骑乘宠物。缺信息时先刷新观察，不能用 `S|FF` 等猜测默认槽位；旧原生处理器可能把错误槽位解释为收宠。

换宠在回合结算时执行。提交人物的 S 指令后，本回合 W 技能仍来自原出战宠物；不要提前改用备用宠物的技能。下一回合以 `BattlePetSlot/BattlePetSlotKnown` 和参战表确认实际结果，收宠后未再次出战时没有宠物指令。`StandbyPetMask/StandbyPetMaskKnown` 可由自身 AI 观察的可选 `standby_pet_mask` 刷新；旧服务端省略该字段时不伪造已知掩码。`SummonPetMask/SummonPetMaskKnown` 来自 `summon_pet_mask`，对应独立的 PETST 可召唤条件；SPET 成功不代表 PETST 已设置。修改这两类选择后，客户端先使旧证据失效，再读取 AI 状态确认，不能把写入当作成功回执。当前原生引擎换宠不扣气力，但候选可提交不等于一定成功，控制状态等仍由服务器结算。

当一回合的指令已被服务端接受，等待事件并重新观察。`battle-end` 发送原版 EO 确认，不是“强制赢得/结束战斗”；应配合实际回合/电影状态使用，避免无条件循环发送。超时未知时先核对提交标志，防止同回合重复动作。

## 自动战斗

```sh
sactl --json auto-battle on stay
sactl --json auto-battle status
sactl --json auto-battle off
```

`stay` 不主动走动找敌；`walk` 会在战斗间移动触发遭遇，仅在用户需要时选择。它是既有固定策略，不是执行任意自然语言目标的接口。自动策略运行时不要再并行提交手动战斗指令；切换手动前停止策略并重新观察。

结构化战斗候选的道具身份由 `item_template_id` / `item_template_id_known` 给出，背包整体身份有效性为 `inventory_known`。收到物品更新或消耗后旧模板证据失效，等待新的自身 AI 观察；不要用名称、图号或旧槽位编号替代模板身份。普通 Web 背包仍保持原有展示。

## 共享战报修复（v0.2.18）

`BY` 合击按实际攻击者逐段输出 `kind="attack"`，保留 `actor`、`target`、`recipient`、`damage`、`petDamage`、`flags`、`hit/hits` 与完整 raw；反射、保护和伤害回显去重沿用普通攻击规则。`BX` 输出 `kind="disobedience"`，其后攻击单独解析，不能把不服从提示当作伤害或吞掉后续攻击。格式不完整仍保留 unknown，不猜测字段。

`battle-log` 的 turn 与所有面向人的回合说明从 1 开始，结束时排除服务端额外发出的下一回合菜单。`battle-state`、`battle-act`、`battle-events` 顶层 turn 和 `ai run --json` 顶层 turn 保持原始协议的 0 起算；effects 内 turn 属于战报的 1 起算。不要用展示编号构造动作。

`ai run` 可读战报优先使用 effect 自带的已结算回合号；开始、结算和离场事件的外层 turn 可能仍是初始化值、下一菜单或重置值，不能简单加一作为战报回合。缺少有效 effect 回合的旧记录才使用外层 turn 加一。

旧原始数据和模型不自动改写。修复前缺失的效果需从原始包重新解析，在新的派生产物中注明解析版本；不能把旧 PPO 轨迹的观察替换后仍声称对应旧行为概率。

`encounters` 使用本地 `map_directory` 的公共知识加载器，与 Web 自动导航和百科采用相同规则：读取同级 `setup.cf` 的 `groupfile` 设置，缺少设置时用 `group.txt`；明确指定的表缺失时报错。资料副本须与所连服务一致；不要为复制该设置而复制服务端凭据。资料中的缺失敌群仍报告为未知，不等于没有战斗风险。

公共任务观察中的 `event:<编号>` 表示收到的 NOWEV 或 ENDEV 旗标为真，适用于判断事件是否已经领取或完成；最终交付仍看 `end:<编号>` 和任务要求的物品变化。公共执行器已支持受目录约束的分页对话和可见角色占格绕行；源码新增的 HTTP `sactl quest` 入口见[自动任务](quests.md)，需服务端支持。

公共任务执行器的 `battle.finish` 仅接管已经开始的单人 PvE，包含原版 NPC 守关战（类型 6）和 DP 遭遇（类型 5）；`max_turns` 必须为 1–100，不消耗道具、不使用治疗魔法。拒绝玩家 PK、竞技场、观战及其他人类参战；完成战斗不等于任务成功，后续必须核验旗标、奖励或实际通行。观察中已确认图号为 0 的 NPCEnemy 守关者不再阻挡寻路；未知图号、复活守卫和其他角色仍保留占格检查。此能力尚未成为独立的 sactl 命令。

自动任务的跨图赶路区分长途进展与原地反复遇敌：每个服务端确认的位置最多恢复 8 次，单次移动任务总计最多 64 次；单场逃跑次数、生命状态、补给和任务时限仍独立限制。不会因普通世界状态刷新无限重试动作。内置 NPC 对话若明确在发送前因版本过期被拒绝，任务执行器最多重新观察两次，重新校验人物、NPC 和窗口；已确认翻页不重复发送，未知写入或等待超时仍停止。直接动作接口的版本检查保持严格。

已核验目录可用 `passage` 指定守关 NPC 的挑战窗口、等级/生命条件和回合上限。公共移动执行器先绕行，无法绕行时才按该契约挑战；需要确认同一守卫图号归零后继续，不因战斗结束就假定通路开放。当前斧头目录仍禁用，不能因已有 quest 入口而绕过任务审核。

开发版修复 `item use <slot> [target]` 的默认目标：省略时为 0（自己），1–5 指宠物槽位 0–4，6–10 指队伍槽位 0–4，均不是角色或宠物的持久编号。调用前读取当前物品槽位；消耗结果未知时先核对库存和体力，不重发。

自动战斗等待未关闭的对话框或竞技场准备时，状态明确显示等待原因，不再误报正在原地走动。完成购物后须正常退出商店；购买成功的 242 窗口仍可继续买东西，不能当作已经关闭。收到战斗终局后成功发送 EO，HTTP 共享观察与 sactl 同步回到 world；未结束战斗及竞技场不能借 EO 提前退出。

开发版自身 AI 观察新增可选 `EncounterPolicy`（公共任务投影为 `encounter_policy`），由游戏服务报告实际中止遇敌的表内行序号及遇敌表兼容校验值。共享寻路只有在本地表一致、策略版本已知时使用；不能手工拼造该值、把资料缺失当无怪，或用区域 ID 替代行序号。旧服务端/字段缺失/校验不符保持原有保守策略。字段供结构化接口和公共执行器使用，未给普通 Web 页面增加内部参数展示。
