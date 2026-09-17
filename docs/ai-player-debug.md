# AI 玩家调试入口

本阶段优先交付 AI 玩家持续生活能力。自动练级与自动游戏任务的额外开发、长时验收暂缓；AI 可以通过提示词和已有工具安排活动。

## 当前验收状态

以下汇总截至 2026-09-17 的有效证据；后文按实施顺序保留各次测试及其范围，早期的“尚未验证”由后续对应测试补充。

| 要求 | 当前证据 | 验证边界 |
| --- | --- | --- |
| 官方 Codex、独立配置、固定 never / danger-full-access、DeepSeek 模型目录 | 真实模型与容器测试、配置及隔离回归 | 未读取或修改操作者配置 |
| DeepSeek、OpenAI、自定义地址，固定 Responses API | DeepSeek 实测；OpenAI/custom 本地 Responses 服务集成测试 | 后两者没有第三方端点实测 |
| 持续心跳、24 种活动、未完成任务优先 | 活动目录与 supervisor 回归；实际管理进程重启后自然心跳 | 短时连续运行，未验证长期自然社交 |
| 私人记忆、自建定时提醒及恢复 | 两轮真实 Codex、数据库重新打开、提醒到期投递；管理进程重启 | 提醒是唤醒决策，不是自动认定游戏操作完成 |
| 公屏与原生邮件 | 两个新 QA 角色双向聊天、交换名片及收信 | 协议驱动验证；未验证两模型长期自主交往 |
| 出生、随机、自定义人物与宠物等级，默认骑乘 | 真实管理员 HTTP 创建、独立原生存档、实际重新登录 | 随机计划只生成一次 |
| 独立容器运行 | 已发布 rc.4 镜像通过真实 DeepSeek/Codex、Factory/broker 与 QA 游戏完整链路；角色 1→2 级 | 短时联调通过，长期自主生活仍待观察 |

当前工作树普通回归已通过：`internal/ai...`、`internal/admin`、`cmd/stoneage-admin`、`cmd/stoneage-ai-runner` 和 `cmd/stoneage-game-mcp`。日志：`build/ai/delivery-current-regression.log`。这项回归默认跳过显式 opt-in 的真实模型/游戏测试，不能代替表中的实测记录。已发布并部署 `v0.1.47-rc.4` 候选版；下述新修复仍需单独发布。

部署包检查 `scripts/test-deployment-package.py` 也已通过，日志为 `build/ai/delivery-package-regression.log`；检查覆盖 AI Compose 文件随包分发、配置变量和项目发布入口的接线，使用模拟 Docker，不构建、拉取或验证实际镜像。

用本地真实 runner、MCP 和官方 Codex 执行 `scripts/ai-runtime-smoke.cjs`，已通过两轮合成 Responses 请求、原 thread 恢复、技能安装和固定配置检查，日志为 `build/ai/image-smoke-local-current.log`。脚本的 SSE 换行和正则转义核查正确，无需修改。此验证只访问本地模拟服务，不使用真实模型密钥，仍不替代 Linux 打包镜像验收。两个容器实测入口的 QA 二进制哈希已更新到 `initial-state-qa-20260916/manifest.json` 的已核对版本；源码模块、编译产物和运行中 QA 程序均匹配该来源记录。

## 已发布镜像联调（2026-09-17）

候选版：[v0.1.47-rc.4](https://github.com/k0ngk0ng/stoneage/releases/tag/v0.1.47-rc.4)，发布提交 `59b8fc0364f67ee009c504f08ffab34869b4c395`。GitHub Actions 全部检查及三组镜像发布成功，临时公开后已恢复仓库私有；后续已部署到生产。

AI 镜像固定引用：

```text
ghcr.io/k0ngk0ng/stoneage/ai-runtime:v0.1.47-rc.4@sha256:9172c015106364a6ba90fa89fbb66db292556641efb3e13f26eb71c44845f0a6
```

`TestLiveFactoryBrokerCodexLeveling` 使用上述实际发布镜像，通过真实 DeepSeek Flash、官方 Codex、Factory/broker、原生 MCP 和独立 QA 游戏完成新角色 1→2 级，任务状态 `confirmed`，耗时 63.43 秒。验证了实际 UID 10001、只读根目录、独立 profile 卷、能力移除、同一请求重放不创建第二个容器，以及关闭后的游戏能力撤销。没有在本机构建镜像。

证据：`build/ai/rc4-factory-broker-live.log`、`build/ai/container-runtime-qa/factory-broker/`。测试自身只验证已加载镜像；另由 `build/release-v0.1.47-rc.4/ai-release-verification.json` 将通过的测试证据、实际镜像和 Release 的 `IMAGE-DIGESTS`/`SHA256SUMS` 关联，确认发布来源。

本轮修复了无末尾斜杠的 API 根地址被容器错误拒绝的问题；模型配置保存会移除尾斜杠，因此该修复覆盖 DeepSeek 和自定义根地址。测试另修正了预绑定 QA 后端遗漏 `ProfileID` 的问题，未改变发布镜像的生产逻辑。

macOS OrbStack 的 Docker CLI 是按程序名分发的多命令二进制；本测试会解析可执行文件符号链接。若解析后名为 `docker-tools`，需将本机真实二进制复制为项目 `build/ai/docker-cli/docker` 并在测试的 `PATH` 中优先使用。该本机测试文件不能提交或分发。

## 管理端操作

1. 进入 `/ai/models`，配置 DeepSeek `deepseek-flash`、OpenAI 或自定义 Base URL / 模型。统一使用 Responses API。保存密钥后点击测试。当前实现直接向配置的 Responses API 发送 `hi`，不会启动 Codex、Docker 容器或游戏玩家；会产生少量模型用量。测试最长默认 60 秒，模型配置的更短超时优先生效，同一管理进程同时只运行一个连接测试。测试成功说明接口可用，不代表游戏 Agent 全链路已经通过。rc.4 没有容器模式测试按钮；rc.5 曾通过 Codex 测试，新版已改为直接 HTTP 请求。
2. 进入 `/ai/profiles` 创建 AI 玩家，绑定模型，填写人格和生活目标。默认目标为“自主生活”，调试时可把心跳间隔设为 60 秒，正常默认 300 秒。
3. 选择出生、随机或自定义初始状态。人物等级、配点、出生村、宠物种类和宠物等级均属于创建时配置，重启不会重新生成。默认开启骑宠和无限游戏资金。
4. 初始化成功后点击启动。观察“运行时”的当前活动、活动期限、下次心跳和会话状态；点击“查看”读取私人笔记、定时提醒及审计事件。笔记显示最近 50 条，提醒优先显示待处理项，超出时明确提示；这只是读取，不会启动或完成活动。仅把配置设成 `active` 不代表运行时已启动。
5. 调试结束使用暂停或停止。关闭浏览器不会停止 AI 的后台心跳。

可先用这个生活目标观察基础行为：

> 在出生村附近生活，性格友善但不刷屏。优先处理未完成事项，空闲时可以发呆、休息、闲逛或与附近的人聊天。记住交谈中的约定，给未来要做的事情建立定时提醒。游戏操作后读取结果，不把一句“完成了”当作游戏已经成功。

## 心跳与恢复

单次 Codex 回合或某个游戏目标完成后，自主生活继续运行。没有待执行任务时，运行时从预设活动中随机选择；发呆和休息不必移动或发言。正在执行的游戏任务会先等待进度，不重复启动。当前活动与下次心跳保存在运行时数据库中。

管理进程启动时会后台恢复持久状态为“启用”的 AI 玩家；暂停、停用的玩家保持原状。管理界面在恢复期间可用，单个玩家恢复失败不会阻止其他玩家；应结合实际运行时状态判断是否成功。用户在登录恢复过程中暂停玩家时，该次恢复会关闭登录会话，不会重新启用玩家。收到 `SIGTERM` 或中断信号后，管理端停止接收请求、关闭 AI 运行时并保留持久状态，供下次启动恢复。

普通协议流量仅增加观察 `revision` 时不会额外调用模型；实际提示词仍保留最新版本供游戏操作使用。聊天、任务结果和角色状态变化仍可在下次空闲心跳前唤醒决策。此行为已由 `TestLifeRevisionOnlyTrafficWaitsButIncomingChatWakes` 验证，竞态日志：`build/ai/life-revision-race.log`。

默认提供 [24 种预设活动](ai-activities.md)，每种都有执行内容、完成条件及前提不足时的处理方式。运行时选出的活动说明会进入实际 Codex 提示词，管理端显示对应中文名称。

定时任务用于到期提醒 AI 决策，投递成功不等于游戏操作完成。日 token 限额、显式暂停和人类接管仍生效；无限资金仅指游戏内资金，不是无限模型额度。

## 通信与记录

公屏发言使用 `game_action` 的 `chat` 动作。游戏邮件使用同一工具的 `mail` 动作：`command=list` 请求名片簿，随后观察 `address_book`；`command=add` 向观察到的附近玩家交换名片；`command=send` 使用当前名片槽位 `index` 和正文 `text`。每次动作均带最新观察版本 `expected_revision`，收到的邮件进入后续观察和事件记录。这里接入原生游戏邮件协议，不是外部邮箱。

名片槽位可能复用，只用于当前会话，不代表永久玩家身份。发出数据包也不等于对方已收到；结果不明确时不能反复发送。

`mail list` 提交后使用 `game_task_status({"handle":"<返回的回执 handle>"})` 查询同一操作；只有请求后的新完整地址簿才能确认，缓存不能替代结果。公屏发言和普通邮件缺少可靠发送方回执时保持送达未知，不重发、不无限轮询，但心跳和其他无关活动仍继续。配点等状态变更仍须先核实结果，才能继续依赖该结果的操作。

运行时保留服务端确认的历史观察；AI 自己的计划和回忆另存为私人笔记，不能变成“已确认的游戏事实”。定时提醒与笔记按 AI 玩家隔离，重启后保留。提醒的投递与对应模型回合一起持久结算，回合结果未知时保留原记录，避免重复唤醒执行。

模型通过 `game_memory_write/list/delete` 管理私人笔记，通过 `game_schedule_create/list/cancel` 管理定时提醒。每轮决策自动带入有界的近期笔记、历史观察和已到期提醒。

## 运行环境

已有玩家采用新版 `stoneage-play` / `stoneage-social` 技能时，先暂停，在管理端编辑并保存以更新技能版本与摘要，再启动。完整未修改的已知旧版安装会升级到当前目录；自行修改过的技能目录会保留并报告冲突，不会被覆盖。

本地 Go 调试程序可编译到项目内（不构建或下载容器镜像）：

```sh
mkdir -p build/ai/debug-bin build/ai/go-cache build/ai/tmp
GOCACHE="$PWD/build/ai/go-cache" GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" \
  go build -mod=readonly -o ./build/ai/debug-bin/ \
  ./cmd/stoneage-admin ./cmd/stoneage-game-mcp ./cmd/stoneage-ai-runner
```

这只是生成程序；启动仍须使用实际的管理数据库、游戏网关和项目独立运行目录，不能直接沿用操作者的 Codex 环境。

运行时接线与容器配置见 [AI for StoneAge](ai-for-stoneage.md#linux-镜像与进程隔离)。每个 AI profile 使用独立持久目录或卷，官方 Codex CLI 在其独立环境中运行。生成配置固定使用 `approval_policy = "never"` 与 `sandbox_mode = "danger-full-access"`，不会读取或修改操作者当前 `.codex/config.toml`。

目前这里是调试步骤，不是完整线上验收报告。代码测试、真实模型调用、真实游戏通信和已发布镜像的验证须分别记录；不能把单次模型连通当作整个 AI 玩家验收通过。

2026-09-16 本轮验证：运行时、游戏协议、MCP、service、supervisor、管理端及三个命令入口的 Go 竞态检查通过，管理端三组 JavaScript 测试通过。覆盖持续心跳、活动/提醒恢复、精确回合投递、笔记隔离和网关能力撤销；通信测试使用协议测试连接，尚未完成双 AI 的真实游戏邮件与长期生活联调。日志：`build/ai/ai-player-final-race.log`。

同日补充验证：24 项活动目录与实际心跳提示词、新旧技能安装、通信结果未知时继续生活，以及新完整地址簿查询确认的竞态测试和静态检查通过。日志：`build/ai/preset-communication-race.log`、`build/ai/mail-reconciliation-race.log`、`build/ai/mail-reconciliation-focused.log`。本地三个调试程序已重新编译；上述测试仍不代表真实双 AI 联调完成。

本地真实 QA 补充验证：`TestLiveMailListFreshAI` 在独立 QA 服务中新建角色，连续两次通过 `GameAction` 查询地址簿，并通过同一回执的 `TaskStatus` 确认新的完整 AB 表，两次均通过。日志：`build/ai/mail-list-live.log`。该测试不调用模型、不发送聊天或邮件；它证明地址簿查询链路，不能替代双玩家通信验收。另已验证：即使查询后积累 130 条未知聊天回执，明确查询旧 handle 仍能完成确认（`build/ai/mail-long-history-race.log`）。

仅在本地 QA 游戏端口 `127.0.0.1:29065` 就绪时运行：

```sh
STONEAGE_MAIL_LIST_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveMailListFreshAI$' -count=1 -v
```

测试会创建持久的 QA 游戏角色，临时网关和本地测试数据库在退出时清理，不使用操作者游戏账号。

双玩家真实 QA：`TestLiveSocialTwoFreshAI` 已通过，日志 `build/ai/social-two-live.log`。两个随机新角色在出生地图经已验证路线移动到相邻位置、确认面向及交换名片设置，完成双向公屏聊天、原生 AAB 名片交换、双方新的完整 AB 查询和双向 MSG 邮件接收。每次送达均由对方会话收到的唯一正文确认。移动通过只读 `S c` 确认，未因结果未知重发 `W`；公屏断言兼容原版服务端添加的名字/称号。

```sh
STONEAGE_SOCIAL_TWO_FRESH_AI_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveSocialTwoFreshAI$' -count=1 -v
```

该测试会在独立 QA 创建两个持久游戏角色、移动及发送测试聊天/邮件。它验证原生通信能力，尚不代表两个真实模型能自主维持长期社交。

真实模型心跳 QA：`TestLiveCodexLifeHeartbeatFreshAI` 已通过，日志 `build/ai/life-codex-live.log`。使用官方 Codex CLI、DeepSeek Flash Responses API 和新建 QA 角色：首轮实际调用工具写入私人笔记与定时提醒；关闭并重建 supervisor/Factory 后，第二轮恢复同一 Codex thread，实际调用工具读取两项持久数据，且生活目标继续等待下次心跳。每阶段最多放行一次模型调用，测试 profile 模型额度有界；游戏写操作被测试后端拒绝。操作者的 Codex 配置未读取或修改。

```sh
STONEAGE_LIFE_CODEX_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveCodexLifeHeartbeatFreshAI$' -count=1 -v
```

该测试消费真实模型额度，默认读取 `vendor/deepseek/key`，不打印密钥；仅在独立 QA 上新建游戏角色。它推进测试时钟触发第二轮，底层游戏连接在 supervisor 重建期间保留。已验证的是两个真实模型回合及 supervisor/Factory 恢复，尚未验证长时自然运行与真实管理进程/游戏连接重启。

到期提醒的真实模型验证也已通过：`TestLiveCodexLifeReminderFreshAI` 在首轮由 Codex 创建提醒后关闭并重新打开 SQLite 数据库，再重建 supervisor/Factory。它把时钟推进到提醒到期、但仍早于下一次普通心跳的时刻；第二轮实际提示词包含原提醒 ID，恢复原 Codex thread 并调用工具读取笔记和提醒，完成后同一提醒持久标记为 `delivered`。日志：`build/ai/life-codex-reminder-live.log`。

```sh
STONEAGE_LIFE_CODEX_REMINDER_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveCodexLifeReminderFreshAI$' -count=1 -v
```

这个用例同样消费两次真实模型回合、限制游戏操作，并保留原游戏连接；它证明提醒唤醒和重新打开数据库后的持久恢复，不等同于管理进程或游戏连接重启验收。

启动恢复接线验证：`build/ai/admin-recovery-regression.log` 覆盖启用玩家恢复、暂停/停用跳过、登录期间暂停、失败继续、恢复时管理 HTTP 可用及退出清理。实际 `stoneage-admin serve` 还在独立临时数据库上完成了两次启动、HTTP 就绪、`SIGTERM` 正常退出，第二次复用第一次的数据库；证据 `build/ai/admin-signal-smoke.json`。该进程起停用例未配置 AI runtime，因此仍不能替代“带真实 AI 会话的管理进程重启”验收。

真实管理进程重启与自然心跳现已通过：`TestLiveStoneAgeAdminProcessRestart` 构建并运行实际 `stoneage-admin`，使用 fresh QA 角色、官方 Codex CLI、DeepSeek Responses API 和独立配置。首个管理进程完成模型回合、保存记忆与提醒后，通过 `SIGTERM` 正常退出；第二个进程复用数据库和运行目录，无需调用 `/start` 就自动恢复玩家，在相同 Codex thread 上完成新回合，再等待真实时钟越过下一次心跳时间并完成后续回合。最后确认私人笔记与待处理提醒仍在、提醒没有重复、模型回合全部结算。日志：`build/ai/admin-process-live.log`。

本次运行约 96 秒，完成 5 个模型回合（含初始状态同步引发的唤醒），没有失败或超额回合。测试总时限 7 分钟、profile 额度 300 万 token；Codex 会累计内部工具循环的输入，包含缓存上下文，本次记录总量约 124 万 token。这是短时持续运行和真实进程/游戏连接恢复的证据，尚不代表长时社交或管理端自定义人物/宠物状态的真实创建验收。测试目标仅要求观察、记忆与提醒；与前述带只读后端的 supervisor 测试不同，这里使用实际管理程序的游戏工具链。

```sh
STONEAGE_ADMIN_PROCESS_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveStoneAgeAdminProcessRestart$' -count=1 -v
```

实测同时修复了本地运行模式的接线错误：空的 `*Broker` 放入接口后被当作已配置容器调度器，导致本地 Codex 玩家启动后暂停。现在只在容器模式赋值；本地模式会正确校验并运行 Codex/MCP 可执行文件。相关竞态回归见 `build/ai/admin-process-regression.log`。管理端创建接口对自定义宠物模板、宠物等级及骑乘开关的转发断言也已补齐并通过。

管理端真实创建验收也已通过：`TestLiveAdminInitialStates` 启动实际管理程序，使用独立管理员登录，通过带 CSRF 的 `/api/ai/profiles/provision` 创建出生、自定义和随机三种角色。测试从原生服务查询可骑宠物模板，不硬编码宠物编号；请求省略 `mount`，验证管理端默认开启骑乘。分别检查发布记录、独立读取的 SAAC 存档及重新登录后的原生状态，核对人物等级、配点比例、出生地图/坐标、宠物模板/等级及骑乘。

本次结果：出生角色 1 级、村庄 0、1 只骑宠；自定义角色 35 级、村庄 2、宠物 12 级；随机角色在人物 20–25 级、宠物 5–8 级、1–2 只宠物和四个村庄候选中生成（本次人物 22 级、村庄 3）。三种方式均保持骑乘，重新登录不重新随机。该测试不调用模型，日志为 `build/ai/admin-initial-live.log`。

```sh
STONEAGE_ADMIN_INITIAL_LIVE_TEST=1 GOCACHE="$PWD/build/ai/go-cache" \
  GOTMPDIR="$PWD/build/ai/tmp" TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/aiservice -run '^TestLiveAdminInitialStates$' -count=1 -v
```

此测试还依赖独立 QA 的 `build/player-integration/queues` 原生管理队列。旧 QA 二进制不含初始化接口，本轮使用已存在的 GCC 镜像离线编译当前模块与补丁 0020–0025，并仅更新 `stoneage-player-qa-gmsv-1`；没有下载或构建 Docker 镜像。当前 QA 程序来源及 SHA-256 见 `build/ai/initial-state-qa-20260916/manifest.json`。更新后普通角色重新登录、双向公屏与邮件也重新通过，日志分别为 `build/ai/provision-current-native-live.log`、`build/ai/social-current-native-live.log`。初始化相关 Go 竞态检查和原生解析/配点/回滚/保存测试通过，Go 日志为 `build/ai/admin-initial-regression.log`。

## 启动卡顿与模型连接修复（待发布）

生产只读采样发现：双核主机上，GMSV 约占 86% CPU、SAAC 约占 50%，当时等待模型的 AI 容器接近 0%。SAAC 的文件队列在三秒内重复执行约 12,000 次 mkdir、chmod、lstat；这是已确认的持续负载来源，不能据此认定已捕获启动瞬间的全部峰值。

本次修复将 SAAC 目录初始化缓存，并让 SAAC/GMSV 文件队列最多每 25ms 扫描一次；使用单调时钟，SAAC 目录故障每秒重试。AI 游戏回合及模型测试容器默认限制为 0.5 CPU、512 MiB 内存、无额外 swap、128 个进程。这些是单容器上限，多玩家总量仍需依据主机容量控制。

游戏模型回合现在使用所选模型的超时配置；连接不通时不再仅依赖外层默认 30 分钟期限。未知结果仍保留恢复检查，不通过立即重复提交来重试游戏操作。模型地址必须能从 AI 容器访问，容器内的 127.0.0.1 指向容器自身；地址由管理员配置。

审计事件显示中文时间线，包含执行者、动作、结果和原因，原始 JSON 可展开查看。模型测试返回耗时及固定的中文错误分类，不向界面返回供应商原始错误或密钥。

上述变更在工作树中实现，启动峰值和 CPU 改善仍须用发布镜像实测；旧版生产容器不会自动获得新资源限制。

本次独立验证：8 个相关 Go 包的完整竞态测试通过（`build/ai/incident-regression.log`）；新增 probe 测试后 admin、airunner、管理入口通过，broker 测试的模拟失败配置纠正后完整竞态回归通过（`build/ai/incident-probe-final.log`、`build/ai/incident-broker-final.log`）。管理端 6 组 JS 测试及 SAAC native harness 通过。真实本机 Codex 与本地合成 Responses 服务完成纯模型 probe、两轮恢复与技能安装检查（`build/ai/incident-image-smoke.log`）；部署包检查通过（`build/ai/incident-package.log`）。这些结果不等于 Linux 发布镜像或生产启动峰值验收。

## 再次自动暂停与直接接口测试修复（rc.6）

生产 rc.5 上一次已确认的缺陷：管理员确认旧未知回合后，外层 broker 和 supervisor 已允许新回合，profile 卷里的 Codex `thread.json` 却仍处于未完成状态。新容器因此返回 `checkpoint_recovery_required`，程序再次自动把玩家设为 paused；不是用户手动暂停，也不是这次模型请求超时。

修复让明确核查过的旧请求成为新检查点目录的依据。broker 必须核实同一玩家、旧请求确为 unknown 且已有人工确认记录；新回合及后续恢复沿用该独立目录。旧检查点、工作区、Codex 历史和长期记忆均保留，未确认的旧回合仍不能被绕过。

模型测试改为直接发送 Responses 请求 `{model: <已保存模型>, input: "hi", stream: false}`，验证返回的完成状态和文本；同时兼容合法 SSE 响应。测试无 Codex 或容器依赖，保留鉴权、并发限制、超时、响应大小限制和不会泄漏密钥的错误分类。已使用生产保存的地址直接验证 HTTP 200/completed，耗时 1.28 秒；这项前置验证没有启动 Codex，也没有修改生产配置。

独立回归已通过：broker、airunner、aiservice 完整竞态测试（`build/ai/rc6-recovery-final.log`）。真实本机 Codex + 合成 Responses 服务复现旧 unknown 检查点：未确认的新回合拒绝且不调用模型，确认后新建不同 thread，随后精确恢复同一新 thread；旧检查点逐字保留（`build/ai/recovery-image-smoke.log`）。同一流程也加入发布镜像 smoke，以在 Linux 两种架构上验证。

“异常核查”改名为“恢复确认”；“查看”以只读方式展示自动暂停原因、恢复准备状态、遗留容器状态和旧回合时间，原始错误可折叠查看。游戏状态仅来自已记录的 `game.observation`，标明观测时间及非实时性质，没有证据的字段显示无记录；查看不会自动确认或启动玩家。

管理端和管理入口完整竞态回归通过（`build/ai/rc6-admin-final.log`），6 组管理端 JS 测试通过（`build/ai/rc6-js-final.log`）；相关包 `go vet` 通过。移动端查看面板已检查，保留列表无横向滚动布局。

## rc.6 生产验证与容器超时补充修复

rc.6 已发布并部署，六个服务健康且对应发布 digest。管理端直接 Responses 测试成功，耗时 1.425 秒，测试期间未创建 AI 容器。生产旧 unknown 检查点恢复后，容器进入新的 reviewed 检查点目录并建立新的 Codex thread，已产生真实模型调用；旧检查点重复拦截的缺陷修复得到验证。

随后暴露另一项缺陷：模型超时仅作用于外层等待，容器没有携带同样的绝对截止时间。主机内存/IO 压力使 Docker 停止和 journal 写入超过期限后，外层进入 paused，但 Codex 容器仍继续执行并访问已经撤销的游戏能力。该遗留容器已停止，未重复启动第二个玩家回合。此状态不是用户手动暂停。

现场证据：主机约 1.63 GiB RAM、无 swap。运行 Codex 时内存 PSI some 约 50%、IO some 约 95%；停止遗留容器后均降至不足 1%。AI 容器限制已实际生效（0.5 CPU、512 MiB、128 PID），采样内存约 95 MiB，未 OOM；不能把压力误说成该容器突破内存限制。当前主机总工作集和其他服务使 Codex 启动出现明显回收抖动，稳定运行仍需资源容量调整。

rc.7 补充修复：服务端把绝对 `turn_deadline_unix_ms` 随请求传入容器并持久保存，取模型期限和更早的调用方期限；过期请求不得启动 Codex，运行中的回合由容器内 context 驱动进程组终止。外层为结果写入保留 10 秒清理时间。既有请求过期后仍可读取原结果，恢复不会修改原 deadline 或重复执行旧请求。

独立验证：6 个相关包完整竞态测试通过（`build/ai/rc7-final-regression.log`），相关包 vet 通过；真实 Codex smoke 验证过期请求未调用模型，并继续覆盖 reviewed 恢复、精确 thread resume 和独立配置（`build/ai/rc7-image-smoke.log`）。程序超时边界的修复不等于当前生产内存容量问题已解决。
