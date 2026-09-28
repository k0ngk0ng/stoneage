# 本地竞技场 AI 指挥官

从 **v0.2.1** 起，竞技场 AI 指挥功能统一为 **`sactl ai`** 子命令，不再单独安装 `arena-agent`。
在石器百科的 **下载区** 下载安装包，或升级包管理器中的 `sactl` 即可使用。
在线登录、组队、匹配、作战、数据记录、训练和评估均不需要 Python、Go 开发环境或 Docker。
只有本地启动隔离原生游戏服进行批量模拟采集时需要 Docker。

每队只有一个指挥官，统一制定人物与宠物的联合计划；成员只观察和执行。
游戏服拥有全部战斗、匹配、资源和积分规则。本工具不恢复旧 Codex 服务、管理入口或 Web worker。

## 安装和启动

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
退出时只关闭本程序启动的 sactl 会话，已存在的用户会话保留。
接管前应停止旧指挥官；同一 socket 和同一服务/账号/角色都有文件锁，未完成比赛固定策略版本。
不要删除 SQLite 检查点来强行重发未知结果。

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

| 名称 | 代号 | 行为 |
| --- | --- | --- |
| 顺序攻击 | `basic` | 默认；按敌方席位顺序攻击，宠物优先普通攻击，否则防御/等待 |
| 经验指挥 | `learned` | 本地模型评价整队动作组合，有限宽度搜索产生联合计划 |
| 推演指挥 | `llm` | 将全队观察、历史和候选动作交给兼容 Chat Completions 的大模型 |
| 联合参谋 | `hybrid` | 本地模型先建议，大模型再定案；失败时尝试本地建议，最终回退 basic |

在 `team.json` 修改 `strategy`。`learned` / `hybrid` 还需 `model` 文件路径。
模型未覆盖该人数模式、规则版本不匹配时拒绝启动；现场动作超出训练覆盖则回退。
每场固定模型内容哈希；训练可在另一进程进行，评估后等当前比赛结束并重启以切换模型。

`llm` / `hybrid` 使用 JSON 的 `llm` 配置，例如：

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

密钥从指定环境变量读取，不写进战斗数据库。远端要求 HTTPS，本机可用 HTTP。
`response_format` 支持 `json_schema`、`json_object`、`none`；所有返回计划仍须通过同样的严格校验。
HTTP 请求由 Go 的 context 限时取消，禁止重定向转发密钥。超出上下文预算时保留最新过程和明确标识的早期摘要，
完整原始事件留在 SQLite；当前全队观察本身过大时回退，不悄悄删掉队员数据。

## 数据、训练和评估

默认数据位置是队伍目录的 `data/arena.sqlite3`，即配置里的 `state_dir/arena.sqlite3`。
使用 WAL 和事务保存事件、整队观察、联合计划、策略/模型版本、提交意图及回执、终局和积分。
网络断线、事件游标缺口和未知写入结果都有明确标识；未知结果不重复提交。
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

当前训练器是**实验性的整队线性胜负价值基线**，并非成熟的强化学习模型，也没有稳定提高胜率的证据。
训练和推理共用特征，只使用己方/公开信息，不使用敌方隐藏属性和未来结果。
仅纳入正常战斗终局、无缺口且联合计划有写入回执的数据；回执不等于服务端保证技能生效。
同一阵容的重复比赛和双方视角归入同一划分，避免训练/评估泄漏。
`evaluate` 排除训练阵容组，报告价值预测指标，不将它当作胜率。

旧的 10,000 条裸人物同点数 1v1 加点样本不是出招策略模型，不能直接用于本训练接口。
开发烟测模型只覆盖 1v1 和 5v5，不能当作 2v2–4v4 模型；也不随安装包作为强策略发布。

## 隔离批量采集

此高级入口需要仓库及预先编译的原生工具，在已有 Docker 镜像里启动临时账号服、原生游戏服、网关和两队客户端。
所有对局通过正常竞技场完成，使用真实 C 战斗引擎；容器内同样不需要 Python。
`--network none` 隔离生产，既不拉取也不构建镜像，不覆盖已有采集目录。

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
`--reconnect` 检查实际进程断线恢复；`commander-passed.json` 保存对局统计和验证证据。
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
