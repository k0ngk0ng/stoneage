# 战斗观察与指令

## 数据来源

```sh
sactl --json observe
sactl --json battle-log
```

`observe.data.Battle`：`Active`、`Turn`、`CommandReady`、`PlayerSubmitted`、`PetSubmitted`、`MyNo`、`MyNoKnown`、`MyMP`、`Participants` 等控制/观察字段。不要只凭 `Phase` 判断可以发指令；也不要把日志中的回合号当成命令就绪信号。

`battle-log.data.battles` 是从新到旧的战斗历史；这里的字段使用小写 JSON 名称，与 `observe` 不同：

- `id`、`started`（Unix 毫秒）、`turn`、`myNo`（未获知为 null）、`ended`、`result`、`trimmed`。
- `roster`：`id`、`name`、`level`、`hp`、`maxHp`、`player`、可选 `mp`/`maxMp`、`ride`、`petName`、`petHp`、`petMaxHp`。
- `logs`：`turn`、`kind`、`actor`、`target`、`damage`、`petDamage`、`flags`、可选 `hit`/`hits`、`text`、可选 `raw`。

目前 `kind` 包含 `attack`、`counter`、`start`、`end`、`unknown` 和部分原始标记（例如 `BD`、`BM`）。伤害字段不是统一的有符号体力差：治疗、吸收、闪避等不能仅按 `damage` 扣体力；未知动作的零值也不代表没有伤害。不依赖中文 `text` 推导缺失语义，必要时使用后续快照；需要更完整的机器语义时报告接口差距。

`roster` 为最近服务端快照，不由日志推算即时体力。日志按结算顺序到达，可能早于动画。仅保留当前会话最近 20 场、每场 300 条，重新登录清空；无法读取另一个浏览器/sactl 会话的历史。

## 手动指令

CLI 的 `battle` 参数沿用原版协议命令，需要 shell 引号保护 `|`。例如默认目标攻击：

```sh
sactl --json battle 'H|FF'
```

只在最新观察允许玩家提交且 `PlayerSubmitted=false` 时执行。`H|FF` 使用默认目标，不代表指定敌人；指定目标和技能参数须以实际 CLI 帮助、已验证协议为准，不能猜编号。宠物提交是独立状态，只对当前可行动宠物发指令。

当一回合的指令已被服务端接受，等待事件并重新观察。`battle-end` 发送原版 EO 确认，不是“强制赢得/结束战斗”；应配合实际回合/电影状态使用，避免无条件循环发送。超时未知时先核对提交标志，防止同回合重复动作。

## 自动战斗

```sh
sactl --json auto-battle on stay
sactl --json auto-battle status
sactl --json auto-battle off
```

`stay` 不主动走动找敌；`walk` 会在战斗间移动触发遭遇，仅在用户需要时选择。它是既有固定策略，不是执行任意自然语言目标的接口。自动策略运行时不要再并行提交手动战斗指令；切换手动前停止策略并重新观察。
