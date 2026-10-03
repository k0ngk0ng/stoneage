# StoneAge Revival

基于 StoneAge 2.5 服务端，提供桌面与手机浏览器客户端、命令行客户端 `sactl`，以及本地运行的竞技场 AI 指挥与训练工具。

- [进入游戏](https://sa.ichenj.com)
- [石器百科](https://sa.ichenj.com/wiki/) · [客户端下载区](https://sa.ichenj.com/wiki/downloads)
- [正式版本与安装包](https://github.com/k0ngk0ng/stoneage/releases)

## 浏览器玩游戏

直接打开游戏网址，登录账号并选择角色。当前客户端统一维护在 `client/web`，桌面和手机共用同一套客户端；无需 Wine 或安装独立桌面程序。

Web 支持人物、地图、NPC、物品、宠物、战斗及竞技场，并保留面向玩家的自动任务、自动练级和自动战斗。竞技场支持 **1v1、2v2、3v3、4v4、5v5**；多人比赛由队长组织队伍，各成员准备后匹配。规则和流程见 [竞技场玩家说明](docs/arena-player-flow.md)。

v0.2.18 提供 HTTP `sactl quest` 入口。`marinas-pet-commission-a` 已通过 CLI → Web HTTP → 原版服务的完整验收，自动购买、捕获四只新宠、交付并核验 500 石币奖励。服务端需配置匹配的 [任务目录](ai/catalogs/README.md)，启动预算为 `--maximum-spend 146`。现有 11 个定义中，仅该委托已验证，其余保持未启用；全部 2.5 任务尚未完成。当前暂停交接与证据见 [自动任务进度](docs/auto-quests-25-progress.md)。

## sactl：命令行客户端

`sactl` 自己连接游戏并保持会话，不依赖浏览器。macOS 安装或升级：

```sh
brew install k0ngk0ng/tap/sactl
# 已安装时：
brew update
brew upgrade sactl
sactl version
```

Windows 可使用 Scoop；Linux 提供 tar、deb、rpm 包。百科下载区提供各平台安装入口和配套 Agent skill，详细说明见 [sactl 文档](docs/sactl.md)。

```sh
sactl login                 # 交互输入账号和密码，自动维护后台连接
sactl chars                 # 列出角色
sactl enter '角色名'
sactl status
sactl --json observe        # 供程序使用的结构化观察
sactl logout                # 默认原地登出
# sactl logout --record-point  # 明确要求回记录点登出时使用
```

普通登录无需创建配置文件，也无需手工执行 `serve`；默认连接正式游戏入口。交互登录的密码只保存在进程内存。`sactl init` 可保存连接偏好，`sactl --help` 查看命令和参数。

v0.2.18 提供 `sactl reconnect`：在后台仍保留登录凭据时恢复同一角色；`status` 会提示是否可恢复，无需把密码写入配置。已安装的旧后台须升级后才能使用。

多角色用命名会话隔离，名字由你自定：

```sh
sactl login --profile player1
sactl enter '角色一'
sactl login --profile player2
sactl enter '角色二'
sactl sessions
sactl --profile player1 status
sactl use player2
```

默认配置在 `~/.config/sactl/`，socket 和会话状态默认在 `~/.local/state/sactl/`；支持 XDG 目录设置。成功登录会切换当前会话，脚本应明确指定 `--profile`。同账号多角色同时在线仍受服务端规则限制。

普通玩家的竞技场命令是 `sactl arena`：

```sh
sactl arena create 1         # 1v1；人数模式为 1–5
sactl arena loadout 1        # 示例：登记第一个宠物槽位
sactl arena ready
sactl arena queue
sactl arena status
```

比赛后用 `arena result` 查看结果、`arena ack` 确认结算。多人组队及装备、宠物登记要求见 [竞技场流程](docs/arena-player-flow.md)。

## 本地 AI 指挥、模型和训练

`sactl ai` 是本地客户端功能。每支 AI 队伍由一个指挥官统一决定全队人物和宠物的动作，成员负责观察与执行。它不在 Web 或管理后台提供 AI 玩家入口。

| 策略 | 代号 | 决策方式 |
| --- | --- | --- |
| 顺序攻击 | `basic` | 默认策略，按敌方席位顺序攻击 |
| 经验指挥 | `learned` | 加载本地模型，生成全队行动计划 |
| 推演指挥 | `llm` | 将战斗上下文交给可配置的 Chat Completions 服务 |
| 联合参谋 | `hybrid` | 本地模型先建议，大模型据此定案 |

```sh
sactl login --profile bot
sactl --profile bot enter '角色名'
sactl ai run --profile bot --strategy basic --matches 1
# 使用外部模型：
sactl ai run --profile bot --strategy learned --matches 1
# 持续匹配：把 --matches 1 换为 --forever。
```

神经模型从 v0.2.16 起默认使用 **safetensors 二进制权重**，结构和训练来源保留为文件内 JSON 元数据。旧 JSON 可用 `sactl ai export-model --model ./model.json --output ./model.safetensors` 无损转换，无需重新训练。

v0.2.17 可自动查找当前目录、`./runtime/ai-models/`、`~/.local/share/sactl/models/`（尊重 XDG_DATA_HOME）的 safetensors；第一层只有一个可用模型即使用，多个则列出并要求 `--model` 选择。默认实时显示匹配、决策、出招和战报；脚本使用 `ai run ... --json`。排队后无需再开终端匹配。

**模型作为本地外部文件加载，不随客户端安装包、Release 附件或 Docker 镜像分发，也不部署到生产。** 使用 `--model` 指定模型路径，也兼容旧 `team.json` 的 `model` 字段；`learned` / `hybrid` 要求模型覆盖对应人数及观察/动作接口版本；训练引擎的 CPU 平台和程序摘要只是来源记录，不限制本地推理。安装或升级客户端不会自动生成强模型。模型权重、训练数据、检查点和评估证据保留在本地目录或本机 Docker volume。

训练与推理由 Go 实现；Mac 的原生战斗引擎环境通过独立 `ai-training` Docker 镜像提供。支持采集、模仿热身、PPO、断点恢复和独立对战评估。在线指挥及调用大模型不要求 Docker；训练镜像的使用方式、volume 持久化和模型导出见 [训练说明](docs/learned-training.md#环境文件)。本地模型可以对接不同 CPU 平台的游戏服，无需因此重新训练。

训练轨迹使用压缩分片，支持 `--stop-at-data-bytes` 停止阈值；阈值不是硬磁盘配额，须给镜像、单批写入和日志留余量。已有候选模型尚无稳定优于基线的强度结论，不能将训练完成或接口测试通过视为胜率保证。

- [本地指挥官配置与运行](docs/local-arena-agent.md)
- [模型架构设计](docs/learned-strategy-design.md) · [实现与验证边界](docs/learned-implementation.md)
- [采集、训练、恢复与评估](docs/learned-training.md)
- [模型验收与逐场选模](docs/learned-champion.md)
- [供 Agent 使用的 sactl skill](.agents/skills/sactl/SKILL.md)

v0.2.18 同步修复合击/宠物不服从战报、回合编号、用药默认目标和商店窗口等待；learned 显示模型实际候选状态。模型权重未更改，这些修复不代表策略已证明强于 basic。

## 部署与升级

生产部署需要 Linux amd64、Docker Engine 和 Compose v2。使用 GitHub Release 的 `stoneage-deploy-<版本>.tar.gz` 与对应 tag 的预构建镜像，服务器不编译源码。完整初装、配置和持久化说明见 [Docker Compose 部署文档](docs/docker-compose.md)。

服务包括 SAAC、GMSV、网关、Web、管理后台和服务控制器。配置在 `config/`，游戏数据在持久化目录或卷中。首次部署运行 `bin/stoneage init` 并准备文档要求的游戏资源；已有部署升级须保留配置和存档。

在部署目录更新程序入口及 `.env` 的 `STONEAGE_VERSION` 后，统一使用：

```sh
bin/stoneage check
bin/stoneage pull
bin/stoneage deploy --no-image-update
bin/stoneage status
```

先校验和拉取，再切换容器；镜像下载期间保持服务运行。仅更新程序时无需重新上传资源。不要删除数据卷或用发布包默认配置覆盖现有配置。

图片、地图、音频使用固定 CDN 路径和浏览器缓存，公开资源通过 `bin/stoneage sync-assets` 增量同步；不把游戏资源或玩家数据打入程序 Release。配置、账号密钥及角色存档也不应提交到仓库。

## 项目结构与发布

| 路径 | 用途 |
| --- | --- |
| `client/web/` | 当前浏览器客户端与 Go Web 后端 |
| `cmd/sactl/`、`internal/sacli/` | 命令行入口与会话管理 |
| `internal/aigame/` | 游戏协议与结构化观察/操作 |
| `internal/arenaagent/` | 本地 AI 队伍指挥、策略与持久化 |
| `server/legacy/` | 现用 2.5 服务端及适配 |
| `deploy/`、`bin/stoneage` | 容器与部署入口 |
| `.agents/skills/sactl/` | 随客户端发布的 Agent skill |
| `docs/` | 各功能、训练和运维文档 |
| `build/` | 本地构建、实验及验证产物，不随 Git 发布 |

推送 `v*` tag 触发 [Release workflow](.github/workflows/release.yml)：测试、跨平台客户端构建、镜像构建与训练工具包验证通过后发布 Release，再更新百科下载资源及包管理器清单。发布产物包括 sactl、配套 skill/安装入口、部署包及 GHCR 程序和训练工具镜像。

旧 Wine、Godot 和 legacy 客户端资料仅供历史协议与资源核查；它们不是当前玩家安装流程或开发目标。开发入口见 [开发文档](docs/development.md)。

管理后台的 **竞技场** 页面（`/arena`）每 3 秒读取游戏进程状态：匹配队伍、等待时间、开战倒计时、双方成员和当前回合。接口仅对已登录管理员开放；不可用时明确显示旧快照，不影响玩家匹配，也不承载本地 AI 控制。
