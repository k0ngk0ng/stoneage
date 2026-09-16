# AI for StoneAge

## Accepted scope

### Product priority: human players first (2026-09-16)

Automatic leveling and quest completion are general gameplay capabilities,
primarily for human players. They must be usable directly from `client/web`
without an AI profile, model configuration/API key, Codex process or AI runtime
container. Shared deterministic services own task compilation, guide-backed
readiness checks, owned-pet binding, execution, budgets and recovery. The Web
session and the optional AI adapter call those same services; character and pet
identity come from the authenticated game session, never from an AI profile as
a prerequisite for human automation. Human players retain finite funds and
explicit start, pause, resume, stop and manual takeover controls.

AI players are an optional consumer of these capabilities. Their product goal
is to live in the game like a human player: sustained preferences and memory,
daily activity choices, social interaction and responses to the surrounding
world. Leveling and quests are available activities, not their sole objective.
Acceptance must therefore cover continuity and contextual behavior as well as
task success; maximizing levels or completed quests alone is insufficient.

The current delivery priority is a runnable, debuggable AI player. Further
dedicated auto-leveling/quest development and acceptance are deferred; AI
players can use prompts and existing tools to arrange these activities.
Human automation remains an independent capability, but must not block this
AI-player delivery. Required foundations are continuous heartbeats, persistent
memory, game mail/public chat, and durable self-scheduled tasks.

Life profiles default to a continuous goal. Completing a Codex turn or reaching
a gameplay target does not stop their heartbeat. When no native task is active,
the runtime selects a persisted activity from the configured pool, with 24
default presets covering exploration, social life, pets, inventory, memory,
planning and growth (see [activity catalog](ai-activities.md)). An activity retains its
deadline across restart. Idle/rest require no movement or public message.
Pending native tasks keep their assignment and prevent duplicate dispatch.
The admin runtime status shows the activity and next heartbeat. Component tests
cover the complete catalog, prompt injection, continuity and recovery. Live QA
also covers game chat/mail, durable notes and reminders, and an actual admin
process restart followed by natural heartbeat/model turns. Current evidence and
remaining validation limits are recorded in [the debugging guide](ai-player-debug.md);
these bounded checks do not establish long-duration natural social behavior.

### AI creation: configurable initial state (requested 2026-09-16)

AI players must not all start as newborn characters. Creation must offer
birth defaults, bounded random initial state, and explicitly configured initial
state. This is an AI provisioning capability and must not become a dependency
of human automation. Initial implementation scope is character level,
attribute allocation, hometown and pets; equipment/items and quest-history
initialization need explicit scope and verified data.

Resolve random choices once per creation and retain both the requested bounds
and actual generated state. Reopening the profile or restarting its runtime
must never reroll, regrant items/pets or reapply initial levels. The native
character state must be internally consistent: changing only the displayed
level without matching growth, allocation, experience and health is not a
complete implementation. Apply and verify initialization before publishing
an AI profile that can be started. Failed/uncertain initialization must not
expose a runnable partially initialized profile or silently retry grants.

The admin creation form now supports all three modes. Custom mode accepts a
character level (1–140), hometown (0–3), four allocation weights (0–100,
at least one positive), and zero to five pets with exact template IDs and
levels. Random mode accepts character/pet level ranges, a pet count range,
and a catalog-backed species pool; it draws the hometown and positive
allocation weights as well. Duplicate pet species are allowed. Each candidate
pet template must support the full requested level range; its native
`E_T_LIMITLEVEL` cap is checked before account creation, including the upper
bound of random ranges. The requested
configuration, resolved plan and verified initial snapshot are kept separately
in `ai_initial_states`; the profile detail displays the original state.

The journal reserves the resolved plan before account/game side effects.
Provisioning sets the hometown through normal character creation and sends
one private `initialize_ai` operation before publishing the AI profile. The
manager requires the resulting archive hash and a matching authoritative
snapshot. Failed or unconfirmed attempts disable their account and prevent
same-ID reroll/reapplication, including after a provisioner restart. Pending
records left by a process crash are reconciled at admin runtime startup:
unconfirmed accounts are disabled, while confirmed initialization with a saved
publication draft can be explicitly verified and published from the console.
No initialization mutation is automatically replayed. Human automation has no
dependency on this initialization service.

Native allocation consumes the full budget of 20 birth points plus the
configured level-up points (`getSkup`, or the stock three per level), using
largest remainders with native attribute order as the tie-breaker. Allocated
points are not left spendable. Pet creation uses a checked explicit-level
entry point with the original template growth formula, and replaces the
birth pets only after all requested pets have been created. The ordinary
random-level pet entry point retains its legacy level-limit behavior.

Validation includes Go component/race tests, admin form tests, native payload
and point-budget tests, and an executable native transaction harness that
forces the second pet creation to fail and checks full character restoration,
new-pet cleanup and preservation of the original pet. Zero-pet and successful
replacement paths check that the old pet is released exactly once. Live admin
HTTP creation now covers birth, custom and random states, including character
and pet levels and default mounting, with independent native archive checks and
actual game relogin. See [the debugging guide](ai-player-debug.md) for the test
and evidence; this does not establish acceptance of a published runtime image.

Source review confirmed `CHAR_makeCharFromOptionAtCreate` stores base
attributes at 100x scale, `CHAR_getInitElderPosition` accepts hometown 0–3,
`CHAR_LevelUpCheck` and battle settlement divide level/experience/skill-point
work, and `CHAR_PetLevelUp` uses packed growth and native randomness.
`CHAR_complianceParameter` recalculates derived health but does not refill it.
These are distinct responsibilities for the initialization transaction.
Pet template TempNo and encounter IDs are different namespaces and must not
be mixed. Configured birth-level limits and `CHAR_MAXUPLEVEL` also differ;
the implementation must use a verified supported cap, not copy a display
editor's bounds or assume that setting `lv` performs normal growth.

### Human automation reconnect recovery

Disconnect revokes the old control lease, drains execution and retains a
paused checkpoint instead of cancelling the task. A newly authenticated Web
session can explicitly resume or cancel that task. Offers require the same
account, canonical character slot, character name and configured game line;
missing or ambiguous character identity does not authorize recovery.

Recovery constructs new gameplay controllers bound to the new session and
resumes the original checkpoint, retaining its plan and spending history.
It does not start another plan or replenish its budget. Prepared/submitted
operations with unresolved delivery remain subject to the existing receipt
reconciliation rules; an unconfirmed write is not blindly replayed. Failed
recovery preserves the offer. An available offer must be resumed or explicitly
cancelled before starting a new task. Human automation requires no model,
Codex process or AI account for this flow.

The live recovery registry expires after 24 hours and holds no old sockets or
gameplay controllers. On patched servers with a confirmed loaded persistent
player ID, new runs also persist their owner/config metadata with the first
checkpoint, before task execution can start. Service startup reconstructs the
registry from these records; older unscoped checkpoints remain unaffiliated.
There is no offline execution or automatic reconnect/resume. This feature
still requires consolidated live reconnect/restart acceptance.

During disconnect cleanup, a pending registry entry blocks recovery and new
starts until the old runner has drained. A bounded drain timeout leaves that
entry occupied while background cleanup finishes. Component tests cover
explicit HTTP recovery/cancellation, identity and line checks, the handoff
window, fresh backend construction, terminal activation, unknown-delivery
preservation with no socket write, and rejection of old leases/callbacks.
Web and shared execution tests pass with the race detector; frontend tests
also cover stale connection responses and retention of quest mode.

### Optional prerequisite chains for human quests

The Web quest form offers an explicit `include_dependencies` option, off by
default. Its preview lists the reviewed task order and conservative full-chain
budget. The HTTP preview and start paths use the same compiler as the optional
MCP adapter. Without this option, the original single-task budget and direct
prerequisite checks remain unchanged. No AI profile, model or Codex process is
needed for a human to select and execute the chain.

`aiplanner.BuildChain` compiles each prerequisite once in dependency order,
validates every node's evidence, execution review and preparation, binds the
same selected pet, and sums finite cost bounds with overflow checks. Reserved
gold is counted once. The explicit time and death limits apply to the whole
chain; default time is the sum of reviewed step timeouts. Budgets conservatively
include even nodes that may already be complete; skipping does not reset or
replenish spending authorization.

Each stage has its own entry and authoritative completion conditions. Direct
prerequisites are checked when that stage is entered, not all at departure.
The deterministic engine persists stage position alongside prepared/submitted
step state. Completing a step is not proof of completing its task. Uncertain
submission cannot be turned into a retry by crossing a stage boundary. Human
preview now shares execution's server-observed inventory, progress flags and
verified NPC-window aliases instead of projecting empty inventory/flags.

This adds one-handle chains within the existing task lifecycle. It does not
add offline or cross-process restoration to Web sessions, populate missing
quest guides, or constitute live quest acceptance. The existing native quest
Skill's separate-node orchestration remains supported; clients choosing a
single chain must not also launch separate handles for its prerequisites.

Independent verification for prerequisite chains passed the automation,
planner, service, MCP and Web component suites, their race-enabled suites,
Go vet and 9 Web JavaScript fixture tests. Evidence is retained in
`build/ai/quest-chain-components.log`, `quest-chain-race.log`,
`quest-chain-vet.log` and `quest-chain-js.log`. The compiler-to-engine test
closes and reopens SQLite between stages; additional regressions cover
whole-run limits while awaiting results and persisting a reconciled stage
while refusing an under-level next stage. No live game, model request,
container build or deployment was used for this change.

### Optional character allocation for human leveling

Human players can opt into character allocation in the Web leveling form.
`character_build` contains four relative base-attribute weights (0–100, at
least one positive) and `reserve_points` (0–1000). Omitted/null leaves automatic
allocation disabled; quest automation rejects this leveling-only setting.
The common `internal/characterbuild.Policy` owns the deterministic choice;
`airuntime.CharacterBuild` remains a compatibility alias for existing profiles.
Web passes an explicit policy into the shared gameplay builder without an AI
profile, model or funding callback. A profile cannot implicitly enable it for
an ordinary leveling session.

The selected policy is cloned for the run and remains fixed across pause and
resume. The form locks allocation inputs until manual takeover. Each point
uses the existing revision/ownership fence and durable uncertain receipt, and
waits for server confirmation before another allocation. Old unknown actions
block automatic allocation across control generations. Human automatic
allocation requires a uniquely confirmed character-list slot so a reconnect
cannot switch receipt identity between the name fallback and slot identity.
Unknown point counts request fresh own-state observation rather than inventing
available points. Completion requires both the leveling target and settlement
of the configured allocation; preview does not report completion while points
or earlier uncertain actions remain unresolved.

Web offline continuation remains unavailable: this change preserves a policy
on the active handle and its pause/resume lifecycle, not a new cross-process
human automation recovery feature. Component fixtures verify behavior without
allocating any real player's points; real game acceptance remains deferred.

Main-agent verification passed for the complete characterbuild/airuntime/
aiservice/Web component suites, focused preparation/Web race tests, admin
configuration compatibility and vet. Frontend fixture tests cover opt-in,
exact weights, invalid inputs and locking the running policy. Evidence:
`build/ai/human-build-components.log`, `human-build-race.log`,
`human-build-admin-compat.log`, `human-build-vet.log` and `human-build-js.log`.
The worker's earlier broad run failed opening a temporary broker journal with
“journal path is not isolated”; the main-agent complete run with workspace
cache/temp paths passed, including that test. No journal isolation rule was
changed or bypassed.

### Human-facing task catalog

The Web automation panel uses a server-supplied task selector rather than a
free-form task ID. Its session-scoped read-only `GET automation/tasks` response
contains a knowledge revision and presentation fields: task name, description,
preparation notes, requirements, prerequisite task names, selected-pet need and
review blockers. It excludes executor actions and source-file evidence. Tasks
awaiting review remain visible with their blockers so an empty or unavailable
catalog does not imply that the player should guess an ID or route.

Catalog review does not establish readiness for the current character. The
existing preview and start paths still compile against the server knowledge
and enforce identity, level and budget conditions. No model, AI profile or AI
container is needed. The browser caches the catalog only for the current game
session, supports explicit refresh, discards late responses from old sessions,
and never treats catalog metadata as a control-generation update. A failed
refresh clears selectable tasks until the directory can be loaded again.

The main agent independently checked the backend projection and HTTP routing,
ran the Web component suite, the final catalog race tests, and the frontend
behavior tests. The latter use a DOM fixture and verify request deduplication,
selection retention, session changes, blocked starts and failed refreshes;
they are not a visual browser or live gameplay acceptance test. Evidence:
`build/ai/task-catalog-web.log`, `task-catalog-race.log`, `task-catalog-js.log`
and `task-catalog-vet.log` in that directory. This UI exposes existing review
gaps; it does not mark any quest or guide as verified.

### Shared quest preflight for human previews and execution

The Web quest preview uses `automation.EvaluatePreflight`, the same evaluator
called by the deterministic engine after observation. It checks the actual
installed NPC, item-stock, healing and movement validators supplied by the
gameplay builder, in addition to finite funds, entry conditions and bound
character/pet level requirements. Missing contracts therefore appear before
the player starts a task. Displayed quote checks also share the Web start
path's rules; a stale quote cannot produce a ready preview.

Preview passes the already-read authoritative observation to the evaluator.
It does not call the backend's own-state refresh, send packets, acquire a new
control lease, create checkpoints or launch task controllers. Start and each
subsequent game write continue to recheck current state; a ready preview is not
a promise that conditions will remain unchanged. Completed tasks retain the
same connected/readiness/character identity checks.

Component coverage includes missing NPC/stock contracts, character and bound
pet level diagnostics, chain budget/contract consistency, outdated displayed
quotes, cancellation, and unchanged control/checkpoint/socket state during a
human preview. Synthetic test contracts are not evidence that an embedded
real-game task is execution-verified.

### Shared quest pet selection

Reviewed `pet_level` conditions may use `$selected_pet` as their reusable ID.
Human players select a synced owned pet in the Web quest form; the HTTP field
and optional MCP task parameter are both `selected_pet_id`. Preview and start
validate ownership against the current character observation. The shared
compiler resolves the reference in task/step conditions and dependencies into
that exact stable ID without modifying reviewed level thresholds or source
knowledge. The persisted plan keeps the resolved ID across resume. Missing
bindings, missing pets and ambiguous identities block execution; a pet slot or
name is not a fallback. Literal stable-ID contracts remain supported.

This does not make unreviewed quests executable or establish live quest
acceptance. The native quest Skill is version 1.2.0 and explains the same
binding for the optional AI caller.

Component verification: the planner race tests cover every condition location,
dependency guards, source immutability, persisted identity, and pause/resume
with a missing, replaced or underleveled pet. Service/Web tests cover owned,
unknown, foreign and duplicate identities plus HTTP request propagation and
preview rejection. The JavaScript behavior test covers slot reordering and
clearing a disappeared selection. Evidence: `build/ai/quest-pet-components.log`,
`quest-pet-planner-race.log`, `quest-pet-entrances-race.log` and
`quest-pet-skill.log` in the same directory. Native Skill verification/install
checks passed; the generic Python Skill validator was unavailable because
PyYAML is absent. No model calls or live gameplay were used for these checks.

### Contextual recall of confirmed experiences

The supervisor now recalls up to four confirmed memories for each of the
current durable goal target and the profile character before filling the
prompt with recent events. This keeps earlier experiences with the selected
pet available after chat traffic has pushed them beyond the recent 32 rows.
Exact subject matching stays within one profile; repeated subjects and memory
IDs are deduplicated. A subject/confirmation index supports bounded lookup
without loading the complete history.

The total remains at most 32 records and 16 KiB of serialized memory. Recalled
history uses at most half that byte budget, leaving room for recent events.
Prompts explicitly distinguish historical facts from current authoritative
observations and treat quoted player text as data. Transient chat/world IDs
are not used as durable social identities. This supplies contextual recall;
it does not yet establish persistent relationship identity or complete
believable-life acceptance.

Component tests cover history surviving database reopen and unrelated recent
traffic, cross-profile isolation, exact matching, count/byte caps, duplicated
retrieval and large historical entries. No live model calls were required.

### Autonomous life-mode decision cadence

An AI profile can explicitly select `goal.kind = "life"` in the admin editor.
Its `goal.life.decision_interval_seconds` controls reconsideration of idle
activities: omitted or zero means 300 seconds; explicit values must be between
60 and 3600 seconds. Other goal kinds do not enable the timer. The editor
preserves this policy across edits and removes it when switching to another
goal kind. Store and admin validation reject invalid or misplaced policies.

This schedule wakes the existing Codex decision loop; it does not randomly
start a leveling or quest controller. The model still chooses activities from
the installed native Skills using personality, confirmed historical memories
and current authoritative observations. Idle time and rest are legitimate
choices. Tasks already running, game readiness, unresolved model/game outcomes
and token limits retain their existing authority. In life mode a completed
gameplay target does not terminate the player; explicit pause, stop, disable
and human takeover still do.

The next idle decision boundary is persisted with the supervisor checkpoint
and exposed through runtime status. A restart must not replay a backlog of
missed timer ticks. This supplies continuity in a quiet game world; it does
not by itself establish long-term relationships or certify believable-life
behavior in a live game.

Changed authoritative observations and task completion can trigger an earlier
decision; repeated identical wake notifications do not bypass the idle timer.
A recovered completed model turn sets a fresh interval from the current time.
Intentional idle waiting does not consume the no-progress diagnosis allowance;
active tasks retain their existing no-progress checks.

Validation covers policy persistence and bounds, admin form round trips,
quiet-world timer wakeup, deadline persistence/restart, exact-attempt recovery,
event-driven early decisions, both prompt builders, and priority of readiness,
active tasks, continuous life after goal completion and token budgets. Runtime, supervisor, admin and
game-service race tests and vet passed (`build/ai/life-final-race.log`,
`build/ai/life-final-vet.log`); admin JavaScript tests passed
(`build/ai/life-admin-js.log`). These tests use controlled clocks and fake model
runners; there were no new real-model calls or live gameplay acceptance.

### Current work order (2026-09-16)

Latest user direction: prioritize a runnable AI player for debugging. Finish
continuous heartbeat/activity selection, persistent memory, game mail/public
chat, and self-scheduled tasks, then verify their integration. Defer additional
dedicated auto-leveling/quest implementation and long-running quest acceptance.
Existing shared capabilities remain available through prompts and game tools.

Preserve reviewed character/pet prerequisites and existing completed features,
including the fixed pet-training UI. Do not use live quest trial-and-error to
discover requirements or let missing quest guides block the AI foundation.
This priority change authorizes no image build/pull, release or deployment.

This implementation covers three modes sharing a game execution core:

1. Player quest automation with exclusive control, preflight conditions and a spending budget.
2. Player/identified-pet leveling to server-confirmed targets, maximum supported presentation speed, resupply and recovery.
3. Persistent, headless AI players created by administrators, with a model, personality, goals, versioned skills, relationships, party/social/duel behavior and optional unlimited funding (enabled by default for AI templates).

An AI player uses the authenticated game gateway and normal game actions. Model output cannot change permissions, funding eligibility or installed skills. Unlimited funding is a server capability, not an overflowing gold value or a prompt instruction. The capability is enabled for AI profiles by default, the visible character gold balance remains a real bounded value, and every NPC charge is recorded by the legacy server. Player automation continues to use finite player funds.

The user explicitly selected the official Codex runtime rather than a custom Go model/tool loop. Each AI player has an isolated Codex session and real installed Skills; the Go service supervises game tasks and Codex lifecycle. DeepSeek `deepseek-flash` is the first tested preset, using its Responses endpoint and embedded official model catalog. It is not the only allowed provider: OpenAI and operator-defined OpenAI-compatible API URLs/model names are also required. Codex and the game MCP integration use the Responses API. The runtime configuration must contain `approval_policy = "never"` and `sandbox_mode = "danger-full-access"`. These apply only to the project-managed game-agent environment, not the operator's personal Codex configuration.

## 管理端配置与 Agent 选型

管理员登录后从顶部导航进入“AI 模型”（`/ai/models`）。这里可以新增、编辑、删除模型配置，设置默认模型、推理级别、超时和 token 日限额，并在提交 API key 后发起一次明确的连通性测试。DeepSeek 是预置选项，不是唯一 provider。配置支持 OpenAI、DeepSeek 与自定义 OpenAI 兼容服务，允许管理员填写 API Base URL 和模型标识；runtime 固定使用 Responses API。API key 只保存到服务端的私有 secret 目录，页面和审计记录只显示是否已配置。

AI Agent 已确定使用官方 Codex CLI 作为 Agent runtime，首个模型使用 DeepSeek 的 `deepseek-flash`。每个 AI 玩家拥有独立的 Codex home、工作区和 session，由 `aisupervisor` 管理启动、暂停、继续和停止；游戏操作通过项目的 MCP server 和已安装 Skill 执行，GMSV/SAAC 仍负责最终状态与权限校验。模型配置本身不会自动启动 AI 玩家，管理员还需在“AI 玩家”页面创建 profile 并启动它。

本地开发时可把 key 放在被 Git 忽略的 `vendor/deepseek/key`，该文件必须是私有普通文件：

```sh
mkdir -p vendor/deepseek
touch vendor/deepseek/key
chmod 600 vendor/deepseek/key
$EDITOR vendor/deepseek/key

go run -mod=mod ./cmd/stoneage-admin serve \
  -ai-model-key-file vendor/deepseek/key \
  -ai-codex-binary "$(command -v codex)"
```

生产环境应通过 `STONEAGE_AI_MODEL_KEY_FILE`、`STONEAGE_AI_CODEX_BINARY` 及对应的私有数据目录参数注入路径；不要把 key 写进环境样例、仓库或日志。

## Linux 镜像与进程隔离

Linux 发布会同时构建两个控制面相关镜像目标：`control-plane` 继续承载
gateway、admin、operator、Web 和资源同步工具；`ai-runtime` 单独承载官方
Codex CLI `@openai/codex@0.154.0`、`stoneage-game-mcp` 以及仓库内 hash 固定的
`ai/skills`。`ai-runtime` 构建时会执行 `codex --version` 精确校验
`codex-cli 0.154.0`，并发布 `linux/amd64` 与 `linux/arm64` 两种架构；旧版
`legacy-runtime` 仍只发布 `linux/amd64`，因为其中包含 2.5 原生服务端程序。

`ai-runtime` 使用专用的非 root `stoneage-ai` 身份，默认不挂载 Docker socket、
管理端认证数据库、operator Unix socket、GMSV/SAAC 数据或资金策略目录。Codex
只应获得对应 profile 的私有 workspace、Codex home、状态目录和服务端签发的
角色能力令牌，游戏写入仍经过私有 MCP 网关和 GMSV/SAAC 权限校验。当前
`docker-compose.ai.yml` 已提供显式启用的镜像拉取目标和管理端 broker 接线，默认部署
不启用容器模式。broker 按 profile 启动独立模型回合容器，保留上述挂载边界。

生成的 Codex 配置固定包含 `approval_policy = "never"` 和
`sandbox_mode = "danger-full-access"`。这两个设置只属于隔离的项目 AI 运行环境，
不修改操作者自己的 Codex 配置；它们也不授予游戏、资金或管理权限。

运行器不会读取、复制或覆盖当前会话的 `.codex/config.toml`。启动前会保护继承的
`CODEX_HOME`、操作者 home 下的 `.codex`，以及项目当前目录和祖先目录的
`.codex`；配置生成目录、工作区或状态目录指向这些位置（包括符号链接）时，
会在写入前失败。游戏子进程的 `HOME`、`CODEX_HOME`、临时目录和 XDG 目录由
服务端固定到该 profile 的独立目录，额外环境变量不能覆盖它们。独立工作区还会
建立自己的 Git 根目录，避免把项目开发指令当作游戏 Agent 指令。

这些路径检查防止运行器误用操作者配置；完整的进程访问隔离仍依赖独立容器，
不能把同一系统用户下的不同目录视作完整隔离。

`stoneage-ai-runner` 已作为 AI 镜像的固定入口实现。它一次读取一个有大小限制的
JSON 请求，校验 profile、角色身份及原生 Skill 目录，再执行一次官方 Codex
回合并返回脱敏结果。恢复必须指定准确 thread ID；请求不能指定二进制路径、
文件目录或修改上述两项策略。容器内的独立卷布局为：

```text
/var/lib/stoneage-ai/              # 仅此 profile 的持久卷
  codex/config.toml               # 生成给游戏 Codex 的配置
  codex/models.json               # DeepSeek Flash 模型描述（选用时）
  workspaces/<profile-id>/        # 独立 Git 根和 .agents/skills
  state/<profile-id>/             # thread checkpoint、进程 HOME、临时目录
```

runner 的竞态测试已覆盖取消、精确 thread 恢复、身份不匹配、严格 JSON 解码和
输出脱敏；镜像入口做了静态校验。`internal/aibroker` 已实现每 profile 独立持久卷、
每模型回合一个容器，以及持久 journal 中的单玩家并发限制和请求幂等。
Docker 使用 `--interactive` 从标准输入接收私有请求，使用 `--pull never` 要求部署
预先拉取镜像；输出超限时及时终止进程并停止对应容器。

Factory 的容器路径已接入 broker，测试确认无需宿主机 Codex/MCP 二进制，
不创建宿主机 Codex 工作目录，角色能力在会话关闭时撤销。上述证据来自 fake Docker
和进程适配测试，并不证明真实 AI 镜像已经运行。管理端已接入容器 broker；
任意容器专属配置出现即选择容器模式，缺项会禁用运行时并列出缺少的配置，
不会退回宿主机 Codex。容器模式也不会实例化宿主机 CLI 的模型连接测试器。
管理端竞态测试覆盖 broker 构造、Gateway 地址校验及失败后的资源关闭。
部署入口及完整恢复的整合验收仍在进行。真实 Docker 的固定程序容器已通过
broker 进程崩溃恢复测试；正式 AI 镜像中的 Codex/模型回合和生产部署尚未验证。

使用 `JournalPath` 创建 broker 时，进程持有独占 journal 锁，关闭和初始化失败
时释放。重启只核查启动前遗留的 running 请求，不检查本进程刚创建、尚在启动
容器的请求。旧容器仍活跃或处于 removing 时保留 running；容器 exited 后读取
退出码和有界日志，只有明确完成、玩家与请求 ID 精确匹配的响应才以条件更新
恢复 completed。缺失、dead 或无有效完成响应时转为 unknown；Docker 查询失败
保留 running，后续查询可重试读取结果，不推断容器已退出或重新调用模型。
unknown 请求不会自动重试，并继续阻止该玩家的新回合，直到结果得到明确处理。

容器不再使用 `--rm` 自动删除，固定使用 local 日志驱动（20m、1 个文件、
`compress=false`）。正常完成和恢复完成都先持久化结果，再不带 force 地清理
已终止容器；清理失败不撤销完成结果，后续查询重试清理。没有有效结果的 unknown
容器继续保留。管理员通过下述异常核查流程明确接受未知结果后，才可解除玩家
占用；原结果仍为 unknown，不能通过删除 journal 或修改数据库来宣称已恢复。

主代理运行 `TestLiveDockerBrokerRecovery`，使用本机已有 Alpine 镜像、禁用网络，
验证了完成结果重放、跨进程 journal 独占、SIGKILL 后保留活跃容器，以及容器退出
后的 unknown 转换和禁止重复执行。扩展测试还验证了 broker SIGKILL 后遗留容器
正常完成、重启时从真实 Docker 日志恢复原响应、完成容器清理后继续重放结果。
实际容器检查确认日志容量配置、UID 10001、只读根目录、
撤销 capabilities、no-new-privileges、单玩家卷，测试凭据未进入容器环境或命令。
证据：`build/ai/docker-broker-live-evidence.json`；日志：
`build/ai/docker-broker-live.log`。测试后独立确认没有遗留 QA 容器或卷。
真实 broker 与 ContainerRunner 的衔接测试也确认原请求恢复后保留会话及 token
用量，不再调用 Docker.Run。主代理对 broker、service、supervisor、admin 四个包
运行 race 测试及 vet，通过记录见 `build/ai/broker-recovery-race.log`；衔接测试
单独日志为 `build/ai/container-adapter-recovery.log`。这些证据不代表真实模型
回合、完整游戏任务或长期运行验收完成。

持久卷中的原生 Skills 会按管理端选择进行核对：安装选中的固定目录，只删除与
目录摘要一致的已取消选中 Skill；遇到内容被修改或符号链接时失败并保留内容。
该行为已有竞态测试，避免持久卷静默保留已取消的 Skill。

## Implementation and acceptance ledger

Received NPC windows now carry a `submitted` flag for a successfully written
local WN response. This flag is exposed through game observation; the original
`open` field is preserved and no server close/result acknowledgement is
invented. Protocol and NPC validation reject a second reply to the same
received window, and actionable automation window predicates exclude it.
A newly received WN is actionable even when all its fields match the previous
one. Submission marking uses the exact window instance, so an incoming new
window between packet write and state update cannot be consumed by the old
write's completion. Failed writes do not claim successful submission.

Three-package full race tests (`aigame`, `aiservice`, `aimcp`) and vet passed;
protocol coverage includes duplicate suppression, identical replacement
windows, replacement during completion, and failed writes. The real QA pickup
race test confirmed STARTMSG 231 submission, rejected a second OK before any
write, recovered the single flower checkpoint and retained the item after
relogin. Logs: `build/ai/window-submission-regression.log` and
`build/ai/window-submission-live.log`. This session-local flag does not settle
an unknown paid operation or certify the complete gift quest.
The real QA stock race test also passed under the new window rules: the native
240/242 shop flow bought five meats, recorded one 60-stone ledger charge and
retained the items after relogin (`build/ai/window-submission-stock-live.log`).

The complete gift QA journey now loads the embedded preparation action before
pickup, uses the reviewed stock/healing catalogs and actual server funding
policy, and installs `TravelItemRecovery` for the delivery route. It targets
the currently reviewed QA binary
`51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19`.
The supplied journey failed after 131 seconds: seven escapes succeeded,
one meat was consumed, and the eighth encounter killed the level-one
character against level-six/eight enemies. The last position was floor 100
(370,477), with one flower, no shell and four meats. Battle state reported
character HP 0/dead while the own-status HP had already become 1. The existing
battle and healing limits were not raised. Supplies and half-health travel
readiness do not establish survival on this route; character preparation and
encounter-aware travel risk remain required before certifying this task.
Evidence: `build/ai/gift-exchange-supplied-live.log`,
`build/ai/gift-exchange-evidence-3220286681.json`, and
`build/ai/travel-diagnostic-1628243266.json`.

Review also found that the pickup QA previously reconnected with STARTMSG
231 still open. It now explicitly submits `231/OK=1` before reconnecting and
records `start_message_ok_submitted`, without claiming a server close
acknowledgement. The reviewed `npc_exchangeman.c` STARTMSG OK branch sends no
such acknowledgement. The embedded task still needs this informational
window-submission step and appropriate client-state semantics; an existing
flower/now-event predicate alone would skip an otherwise redundant confirm
step. The full task remains unverified.
The corrected pickup passed the real QA race run in 18.63 seconds, including
single-request checkpoint recovery and inventory persistence after relogin;
`aiservice` vet passed. Log: `build/ai/gift-start-message-live.log`.

Gift task preparation is now explicit in the embedded task: stock five small
meats with two reserved backpack slots, at most 60 stone and 600 seconds.
`item.stock` rechecks the reserved capacity before purchasing; the
`backpack_free_slots` success condition also prevents an already-stocked but
full backpack from skipping preparation. Capacity uses the authoritative
inventory flag and occupied-slot count, never display item names. Other tasks
do not automatically acquire a stocking step. The full gift task remains
`unverified` with `execution_verified=false`; five meats are an initial task
policy, not a verified survival guarantee.

`TestLiveGiftSuppliesFreshAI` passed against the isolated QA server using the
embedded preparation action and all three packaged catalogs: five meats,
one server ledger charge of 60, unchanged funded visible gold, and five meats
with ten free slots after relogin and funding revocation. Source hashes for
the shop and item table were unchanged. Task JSON participates in the knowledge
fingerprint, so the catalogs now bind to
`4244c8871551d59232ad249299d3998bf38b2791afc5a74d4e2218e018458177`.
Evidence: `build/ai/gift-supplies-live-evidence.json` and
`build/ai/gift-supplies-live.log`. This test executes the preparation skill;
it does not bypass task verification to execute the full quest or invoke a model.
Full race suites for `aiservice`, `aiknowledge`, `aiplanner`, and `automation`
and their vet checks passed (`build/ai/quest-supply-regression.log`). Coverage
includes unknown or invalid capacity, inventory already meeting the target,
and inventory changing after the shop window opens but before purchase.

The `ai-runtime` Docker target now executes `scripts/ai-runtime-smoke.cjs`
after selecting its final non-root user. BuildKit disables external networking
for this step. A loopback-only synthetic Responses endpoint drives two actual
runner/Codex processes, checks exact-thread resume and the retained original
prompt, and verifies installed native Skill content plus `never`,
`danger-full-access`, and Responses configuration. The script removes its
private temporary config/thread state and is bind-mounted only for the build
step. No real model key or game server is involved.

The script passed with the current native Go binaries/local Codex and with
Linux arm64 binaries in an existing local Node/Git image, using UID 10001,
network none, a read-only root, dropped capabilities and temporary state.
Logs: `build/ai/image-smoke-local.log`, `build/ai/image-smoke-linux.log`.
The Linux QA container was checked absent afterwards. An initial attempt on
an existing image without Git correctly failed startup; Git was not made
optional. These tests validate the gate and runner/CLI compatibility; the
final Docker target and its amd64 variant have not yet been built or verified.
This does not establish model reasoning, native Skill execution, MCP game
actions, or broker/Factory integration with the released runtime image.

Broker terminal-state publication now rejects updates to an already completed
outcome through both `Update` and `UpdateIfState`, in SQLite and memory journals.
A regression first demonstrated that `expected=completed` could previously
change a completed row back to running; it now checks all three target states
and preservation of the original response/error. Race tests for `internal/aibroker`
and `internal/aiservice` passed.

Container-mode unknown turns now have an authenticated administrative review
flow at `GET/POST /api/ai/profiles/{id}/recovery` and an “异常核查” panel. POST
requires administrator role, CSRF, current profile/checkpoint/attempt/broker
identities, and the fixed `accept_uncertain_outcome` acknowledgement; actor
identity comes from the authenticated session. It does not start the player.
The supervisor requires the previous run goroutine to have exited; the factory
requires its old game lease to be gone; the broker requires the old container
to be absent/exited/dead before releasing its profile claim.

The recovery GET also checks the actual Docker state before enabling review.
An idle supervisor alone is insufficient: created/running/paused/restarting/
removing containers remain ineligible, and daemon inspection failures fail
closed. This preflight does not remove containers, release claims, or change
the journal. POST independently rechecks container state so a stale readiness
response cannot authorize review of a live container.

Readiness verification: four-package `Unknown|Recovery` race tests and vet
passed, as did the admin JavaScript tests. The real Docker recovery test also
confirmed that readiness for an exited unknown container preserves both the
container and the profile claim until explicit review. Logs:
`build/ai/unknown-readiness-race.log` and
`build/ai/unknown-readiness-docker-live.log`. The QA containers and volumes
were independently checked absent after the run.

Review preserves the original broker request and unknown outcome. The adapter
allows only a new caller ID with a fresh conversation afterwards. A single AI
store transaction conservatively moves the reserved token charge into charged
budget, without inventing measured token usage or a provider result, retains
the old attempt as unknown, writes the operator audit, and clears stale
supervisor turn/thread/task snapshot state. Partial transport commits and lost
HTTP responses are retryable by the same disposition. Ordinary replay of the
old request still returns unknown. Startup and review participate in the
supervisor shutdown wait; a shutdown prevents their late publication.

Verification: Memory/SQLite migration, CAS, immutable review and restart tests;
real broker + SQLite + factory/adapter tests with a daemon fixture; supervisor
partial-commit/retry/fresh-turn and shutdown tests; authenticated HTTP role,
CSRF and actor-spoof rejection; browser desktop/mobile panel verification and
JavaScript acknowledgement/retry/stale-response tests. The real Docker smoke
also reviewed a killed unknown container, reopened the broker, completed a new
request for the same profile and confirmed that the old request remained
unknown (`build/ai/unknown-review-docker-live.log` and
`build/ai/docker-broker-live-evidence.json`). This live test uses an existing
local Alpine image, not Codex or a model provider. UI screenshots under
`build/ai/recovery-ui/` use the real admin page with a synthetic runtime and do
not prove a production game/model review.

The isolated QA GMSV now uses `build/ai/qa-funding.override.yml` to read funding
policies from `/qa-tmp/ai-funding/policies` and append charges to
`/qa-tmp/ai-funding/ledger.log`. Both paths resolve through the existing QA tmp
bind mount to `build/player-integration/tmp/ai-funding/` in this workspace.
The running binary remains SHA-256
`95fd6cb85005061cc3427fa84b5d1606d162e8e5c9494f0659aa55c49e001ab1`.
After the local QA configuration change, the real fresh-character flower
pickup/checkpoint-recovery/relogin smoke passed with the race detector; log:
`build/ai/qa-funding-config-smoke.log`.

Main-agent `TestLiveFundingPurchaseFreshAI` passed with the race detector:
a fresh zero-gold QA character reached the village weapon shop (floor 1001,
NPC 17,13, player 15,13), opened windows 240/242, and purchased one item 4 for
105 through `NPCSkill`. The server appended exactly one new matching NPC
charge of 105; the test checks the pre-purchase ledger prefix, identity, slot,
amount and timestamp. After revoking the funding policy and relogging into
the same character, the item remained and authoritative gold was still zero.
The test pins the actual running binary hash and compares reviewed NPC/item
data to the QA files. Evidence: `build/ai/funding-purchase-live-evidence.json`;
log: `build/ai/funding-purchase-live.log`. This proves this real purchase,
accounting and revocation path, not every spending path or a model-driven turn.
The broader funding audit still needs to ensure each NPC grants its reward
only after a successful charge and handles ledger-write failures consistently;
this successful transaction does not establish failure atomicity.

The QA default grants new characters 1,000,000 gold. This test used a private
temporary setup with initial gold zero; existing characters were not edited.
The original QA setup was restored afterwards, and the main agent separately
verified the ledger and absence of remaining test policies.

Testing also exposed two implementation defects. Cross-map search now rejects
blocked destinations before enumerating warps (`build/ai/cross-map-blocked-target.log`).
Actor direction decoding incorrectly added three to the native server value:
a real L(2) became direction 5 (`build/ai/look-direction-offset.log`). After
fixing the decoding, `TestLiveLookFreshAI` confirmed all eight directions on
the real QA server without moving (`build/ai/look-live.log`). S:c contains
floor and position but no facing; it is not used as standalone proof of a turn.

Admin and Web accept an optional server-owned NPC contract file through
`-ai-npc-registry` or `STONEAGE_AI_NPC_REGISTRY`; Web also supports
`[automation].npc_registry` in its TOML configuration. The optional AI Compose
overlay forwards the environment setting to both services. Use a path inside
the existing read-only game-data mount, such as
`/game/gmsv/data/ai/npc-registry.json`. No catalog is enabled by default.
The version-1 JSON envelope contains `knowledge_fingerprint` and `npcs`; both
the envelope fingerprint and each NPC's `source_fingerprint` must match the
loaded knowledge. Entries use snake_case fields and must pass the reviewed
NPC contract checks. Loading a contract does not change a task's verification
status or establish evidence of live quest completion.

Admin and Web also accept `-ai-healing-items` or
`STONEAGE_AI_HEALING_ITEMS`; Web TOML uses `[automation].healing_items`.
The control-plane image includes the reviewed catalog at
`/opt/stoneage/ai/catalogs/healing-items-2.5.json`. Set the environment variable
to that path to enable its `small-meat` alias when the loaded game-data
fingerprint matches. The catalog is optional and disabled by default.
Its version-1 JSON envelope has `knowledge_fingerprint`, `itemset_sha256`, and `items`; each
item contains `alias`, `template_id`, `base_hp`, and `verified: true`.
Unknown fields, duplicate aliases, unreviewed entries, invalid values, and
fingerprint mismatches fail startup before opening automation stores. The runtime
also hashes its effective `itemset.txt`, because that table is not included in
the general knowledge fingerprint. A changed item table invalidates the catalog.
The loaded contracts are supplied to `item.heal` in both gameplay runtimes.
When this catalog is enabled, movement also uses confirmed backpack items
when its encounter-area health check requires recovery, including after an
observed battle escape. It rechecks actual HP before resuming and never
retries an uncertain item-use result. Safe town movement does not consume
supplies solely because HP is low. A movement action allows at most 15 item
recovery attempts, independently of the existing eight-battle recovery limit.
Missing supplies stop movement; automatic purchasing is still unfinished.

Reviewed stock offers are loaded through `-ai-stock-items` or
`STONEAGE_AI_STOCK_ITEMS`, and Web TOML `[automation].stock_items`.
`ai/catalogs/README.md` lists the three packaged paths for the meat shop,
healing item, and stock offer catalogs. The stock file's version-1 envelope
contains `knowledge_fingerprint` and `offers`. Each offer has an `alias`,
`item` and `npc` reference, plus `shop_index`, `unit_price`, `x`, and `y`.
Both referenced catalogs must already be loaded and verified for the same
game data. Missing dependencies fail startup before stores are opened.
The runtime supplies the resulting contracts to `item.stock`; callers can
choose an offer and `target_count`, but cannot supply a shop or price.
Quest planning still needs to insert the stocking action at the appropriate
point; enabling the catalog alone does not start a purchase.

Unchecked items are outstanding work, not claims of support. A complete implementation requires all behavior below to be integrated and verified against an isolated real 2.5 server in addition to unit tests.

- [ ] Exclusive role control, generation fencing, immediate human takeover, cancellation of queued/late actions, multi-device arbitration.
- [ ] Server-owned session; browser observation/manual control; explicit offline continuation; reconnect and resynchronization.
- [ ] Headless login/character lifecycle; authoritative player, pet, inventory, window, world, party and battle state.
- [ ] Legal navigation/warp/NPC/window/combat/item/pet/party/chat/trade/duel skills with result confirmation and bounded recovery.
- [ ] Versioned knowledge from effective 2.5 data/configuration and executable task definitions with provenance and coverage reporting.
- [ ] Quest preconditions, dependencies, branching, completion predicates, checkpoints and recovery from uncertain delivery.
- [ ] Character/pet target identity and level limits; leveling route evaluation, combat, resupply and server-confirmed stopping.
- [ ] Preflight minimum/expected/reserve/maximum funds and time ranges with uncertainty; no invented cost estimates.
- [ ] Browser controls, locked gameplay inputs, pause/takeover/resume, progress, errors, restored animation speed and mobile layout.
- [ ] AI profiles, isolated persistent plans/memory, model tool calls, installed-skill validation, activity/budget limits and failure recovery.
- [ ] Official Codex process adapter, exact-session recovery, native Skills discovery, MCP tool execution and game-event-driven continuation.
- [ ] Embedded DeepSeek-Flash model catalog, configured reasoning capabilities, unattended policies and real provider smoke verification.
- [ ] AI account/character provisioning, profile/skill management, status, observation and audit in the authenticated admin console.
- [ ] Server-enforced unlimited funding across supported spending paths, bounded external transfers, accounting and revocation.
- [ ] Packaging/configuration/operator integration and secret isolation; no production deployment without the release workflow.
- [ ] Concurrency, crash recovery, stale state, model failure, exhausted budgets, mixed parties and long-running integration tests.

## Runtime boundaries

### NPC purchase funding commit

The native item-shop handler now reserves empty backpack slots and creates
detached items before committing one charge. Allocation failure, insufficient
slots or rejected funding destroys the prepared items without publishing them
or paying family tax. After a successful charge it attaches the items using
the native ownership fields and publishes the completed inventory. Quantity,
price and multiplication bounds are checked before allocating anything.
Finite charges also check the full quoted balance before calling the legacy
gold helper, which otherwise clamps oversized requests to the carry limit.

Patch `0015-ai-funding-transactions.patch` also moves pet-skill charges before
the skill assignment in three server handlers; this does not change the
already-fixed client training UI. The scripted free-skill handler can still
consume event ingredients before charging. This is not a general transaction
layer for scripted events, storage or all NPC rewards, and ledger writes and
character saves are not crash-atomic across files.

Patch `0016-ai-pet-funding.patch` charges pet storage before changing the
selected/default pet or transferring ownership. Lost-pet retrieval creates a
detached pet first, charges before assigning it to the player, and destroys
the detached pet if charging fails. Parse/allocation/charge failure preserves
the original recovery record. The legacy lost-pet backup still uses an
unchecked truncating file rewrite; a backup failure or process crash is not
transactionally reconciled with the funding ledger and character save.
`test-ai-pet-funding.py` compiles the actual patched storage handler and
retrieval block and verifies success and these failure boundaries.

NPC item-shop and riding-school family-tax requests now carry `npc_tax` as
their origin. The AC server preserves this value on a failed treasury update;
GMSV then suppresses the player cash refund. This applies to finite and funded
purchases alike: the service has already been delivered, so a treasury failure
is not a failed player deposit. Actual family-bank deposit refunds and
withdrawals retain their existing behavior. No decision depends on whether
the player still has an AI funding policy when the asynchronous reply arrives.
`test-npc-tax-refund.py` compiles the patched gold callback branch and checks
tax failure, deposit failure, successful deposits and withdrawals.

Bounded checks: `python3 server/legacy/modern/tests/test-ai-funding.py` and
`python3 server/legacy/modern/tests/test-ai-purchase.py` pass. The latter applies
the real patches without fuzz, compiles the resulting purchase handler and
checks funded/finite success, allocation and charge failures, inventory space,
ownership and publication order, and invalid quantities/prices. The GMSV
patch sequence also applies without fuzz to workspace copies using the build
script's existing integration guards. No image build or live-game validation
was performed for this change.
Component output is saved in `build/ai/funding-module-basic.log`,
`build/ai/funding-purchase-basic.log`, `build/ai/funding-pet-basic.log`,
`build/ai/funding-tax-basic.log` and `build/ai/funding-patch-stack.log`.

The remaining source audit distinguishes direct charging coverage from
transaction consistency. `npc_windowhealer.c` charges before HP/MP/full
recovery; `npc_healer.c` and `npc_fmhealer.c` are free. Item storage charges
before transfer. Scripted exchange (`NPC_EventAdd`, `NPC_AcceptDel`) can still
consume items/pets or grant rewards before charging, and warp scripts can
execute event actions before the direct transport fee. These require an
explicit multi-asset transaction design; merely replacing `CHAR_DelGold`
does not make them complete. The server-funding acceptance item stays open.

Trade quota accounting now uses `StoneAge_AIFundingRecordTradePair` through
`0018-ai-trade-pair-funding.patch`. It checks both real balances, rejects
duplicate character/policy identities, and checks both funded quotas under
the common exclusive ledger lock before writing either debit. A same-directory
temporary ledger preserves all existing six-field records, adds both debits,
syncs the file and publishes it with one rename. Existing ledger readers and
single-action writers retain their format and share the same lock. Ordinary
participants need real gold; unlimited NPC funding never creates trade gold.

Quota rejection, partial temporary writes, file-sync and rename failures
before publication preserve both quotas. The parent directory is synced
before and after publication. A failure after rename is reported as uncertain:
both records are visible together, and callers must not infer that no quota
was consumed or replay the trade. Native asset transfer happens only after
the function reports success. This does not make ledger publication and the
subsequent item/pet/gold changes or character saves crash-atomic; reconciliation
of that boundary remains unfinished. Copying the ledger makes each pair commit
linear in ledger size; compaction is not implemented.

The actual C module passes the funding harness with two funded participants,
mixed ordinary/funded participants, limits, invalid identities, zero transfers,
malformed ledger tails, changes to balance/policy while acquiring the lock,
competing processes sharing a quota and injected I/O failures. The real trade patch stack
applies without fuzz and places one pair call before native exchange.
Its funding-failure branch now initializes the client message explicitly and
asks for accounting review before another attempt. The build also refreshes
the observation module on reused source trees, so newer optional observation
fields are not skipped by the one-time integration-patch guard.
Evidence: `build/ai/trade-pair-funding-basic.log` and
`build/ai/trade-pair-patch.log`. These are component checks, not live trade or
full server crash-recovery acceptance; no image or running server was changed.

Ground-gold drops now use `0019-ai-drop-funding.patch`. The actual
`CHAR_DropMoneyFXY` helper checks the chosen cell and pile capacity before
recording quota. Adding to an existing pile records first, then changes the
pile and purse. Creating a pile reserves the native map/object slot with zero
gold, records quota, then fills the pile and deducts cash. Allocation failure
does not record quota; recording failure removes the empty reservation and
returns a terminal failure, so the outer drop loop cannot retry another cell.
The previous record call after cash/world mutation has been removed.

`test-ai-drop-funding.py` applies the real patch stack without fuzz and compiles
the patched native helper with warnings as errors. It covers new/existing pile
success, funding failure preserving purse and ground gold, reservation cleanup,
allocation failure, blocked/full/item-occupied cells and invalid amounts.
Evidence: `build/ai/drop-funding-basic.log`. This is an in-process commit-order
repair. A crash after ledger persistence and before world/character persistence,
or an uncertain ledger write, still needs the broader reconciliation work;
the helper does not claim rollback of a possibly persisted quota record.

The reviewed adult-ceremony judge contract now bypasses the generic sequential
item mutation path through `0020-adult-item-exchange.patch`. Its dedicated
module prepares the helmet before consuming any of the fifteen ritual items,
reuses a consumed backpack slot, sets native ownership, then publishes the
completed inventory change. Exact script matching prevents this path from
silently skipping gold, pets or other effects in a changed script. Failed
preparation returns false to the existing caller before it sets completion
flags. The actual-script C harness and relevant patch-stack checks pass in
`build/ai/adult-item-exchange-basic.log`. The same module now also prepares the
messenger's complete fifteen-item grant before binding any inventory slots,
with cleanup on allocation/input failure and publication only after all slot
writes. Native eligibility checks retain the active-event requirement, and
granting the items does not complete the ceremony. Details and remaining
preparation and crash-recovery gaps are recorded in `ai-quest-sources.md`.
The independently authored grant harness, reviewed and rerun by the main
agent, passes all fifteen allocation-failure positions and the related
inventory/ownership boundaries in `build/ai/adult-item-grant-basic.log`.

### Quest dependencies

Task definitions now accept explicit `dependencies` IDs. Knowledge loading and
the planner reject missing/ambiguous references, cycles and repeated edges;
shared prerequisites are visited once. A task cannot be advertised executable
when its reachable prerequisites lack verification or preparation review.
The planner verifies every dependency's own evidence and adds direct
completion predicates to the requested task's entry conditions. Startup
rechecks those entry conditions against its second observation.

Codex's native quest Skill (1.1.0) orchestrates each node through ordinary task
handles and checkpoints. The requested task keeps its own steps and budget;
prerequisite costs are separate and must be included when quoting a chain.
Ancestor reward-item predicates are not flattened into descendants, since an
intermediate task may consume them. Dependency completion cannot contain the
connection-local `window_submitted` marker; authors must supply observable
server state. Existing embedded tasks are unchanged and remain unverified.

Bounded knowledge/planner/automation/service/MCP/admin checks are recorded in
`build/ai/task-dependencies-basic.log`, including dependency discovery,
transitive verification, graph validation, entry-state changes and native Skill
installation/hash checks. The generic Skill validator remains unavailable
because the local Python environment lacks PyYAML; no package was installed.
No full quest or model run was attempted. Real guide-mapped task content and
concentrated integration acceptance remain outstanding.

### Profile personality and level targets

The default supervisor turn now includes the persisted personality name,
prompt, traits and values alongside the goal and profile-scoped confirmed
memories. Personality guides roleplay and decisions; it cannot grant game
permissions. Previously the default turn omitted personality entirely.
Custom prompt builders continue to own their supplied prompt content.

The admin profile editor preserves existing personality traits/values and
goal metadata when changing visible fields. It exposes the leveling target
kind (`character` or `pet`), target identity and stop-on-completion option.
For pets, copy the stable pet ID from the observation; names and collection
slots are not identities. Empty character targets bind to the current
character. Opening a new profile clears the prior profile's target and
preferences. This fixes editing that previously discarded the pet identity
and metadata and forcibly enabled stop-on-completion.

Supervisor/admin component tests passed, including persisted-personality
prompt isolation and the profile edit/provision form round trip. Evidence:
`build/ai/profile-config-basic.log`, `internal/admin/ai_test.js` and
`internal/admin/ai_recovery_test.js`. The provisioning assertion now compares
the returned Skill version, digest and path with the current fixed catalog,
instead of expecting all Skills to remain at 1.0.0. No real-model or browser
visual acceptance is claimed. Character-build configuration and single-point
confirmation have since been implemented as described below.

### Social settings and native trade

The typed `social-setting` action changes one of `party`, `duel`, `party-chat`,
`trade-card`, or `trade`, using `value: 0/1`. It requires observed FS flags,
preserves unrelated bits, and waits for a new server FS response before another
change. Observation distinguishes unknown preferences from known disabled ones.

The typed `trade` action covers `request`, `offer-item`, `offer-gold`,
`offer-pet`, `lock`, `confirm`, and `cancel`. Item/gold `index` is display slot
0..1; item `value` is backpack slot 5..19, gold `value` is a positive amount,
and `pet_slot` is 0..4. The request targets a visible player; connection file
descriptors and raw TD payloads are not model inputs. Locking and final
confirmation are separate stages of the preserved 2.5 protocol.

The MCP trade observation exposes both offers and peer item/pet display data.
Own offer/lock/final markers describe local submission, while peer stages come
from server packets. A W close packet never certifies a completed transfer:
the caller must reconcile inventory, pets and gold. Unknown writes, human TD
traffic and changes to locked peer offers prevent further confirmation.
Other inventory, pet and movement actions are fenced while a trade is pending
or active. Before locking/final confirmation, local item records, pet identity
and offered balance must still match the current observation. Visible actors
expose native `char_type` (1 for players), and `look` is advertised in the MCP
action schema so the Agent can face the intended player before requesting.
Typed Web writes bypass the manual observer to preserve this
distinction without reentering the session state lock.

The native social Skill is updated to version 1.1.0. Unlimited NPC funding does
not remove server external-transfer limits. Component checks and concentrated
integration acceptance are tracked separately; no real trade or model run is
claimed by this implementation entry.
The four-package component checks (`aigame`, `aimcp`, `aiservice`, `client/web`)
and focused social/trade race checks passed. Evidence:
`build/ai/social-trade-basic.log` and
`build/ai/social-trade-focused-race.log`. Coverage includes request-target
ambiguity, incoming trades, CP936 names, two-stage confirmation, six offer
groups, changed inventory, manual takeover, cancellation/reopen, lifecycle
invalidation and the shared Web connection writer. No real gameplay was run.

### Character stat allocation

The headless state now records native `SKUP` remaining points, with an explicit
known flag. `game_observe` exposes authoritative `own_progress.stat_points`
(including zero), `flags["stat_points:known"]`, and positive observed
`attribute_vital`, `attribute_strength`, `attribute_toughness`,
`attribute_dexterity` values. Missing values are unknown, not free points.

`game_action` accepts `kind: "allocate-stat"` with the latest
`expected_revision` and `index` 0=vital, 1=strength, 2=toughness, 3=dexterity.
It sends one normal SKUP request. No amount or batch count is accepted. The
character must be alive, outside battle, with known positive remaining points.
After a write, allocation pauses until a server point observation refreshes the
state. The action receipt remains unknown until the caller observes the actual
effect; packet submission is not proof of a stat increase, and unknown writes
are not automatically replayed.

Original 2.5 `CHAR_SkillUp` subtracts one point, adds 100 internal units to the
selected stat and sends the displayed attribute/derived-stat update. It does
not send SKUP afterward. The read-only modern `S("AI")` response therefore adds
the optional `stat_points` field from the bound character's `CHAR_SKILLUPPOINT`.
Existing background own-state queries can refresh it without inventing a
client-side decrement. This server module change requires the later normal
build/release; existing QA/production binaries have not been replaced. Legacy
servers without the field cannot refresh this count after every allocation.
Character build selection now uses optional `goal.character_build` with
`weights` (`vital`, `strength`, `toughness`, `dexterity`, each 0..100) and
`reserve_points` (0..1000). At least one weight must be positive. Existing
profiles without a build leave points unspent; there is no default combat
build. The admin form supports enabling, editing and clearing the policy,
which persists in the goal JSON and appears in the Codex prompt. A running
session holds an independent policy snapshot; profile-version changes use the
existing pause/restart boundary.

The service selects the positive-weight attribute with the smallest observed
base-attribute/weight ratio; ties use native index order. All four attributes
must be known positive values. Zero-weight attributes receive no points.
`flags["build:allocation_allowed"]` and `own_progress["build_next_stat"]`
expose an eligible single point. The Agent-owned action boundary rejects a
different index, insufficient unreserved points, unknown state or an absent
policy. Player-controlled allocation retains its native constraints.

Before sending a point, the service durably saves its observed baseline and
refuses another allocation while any action receipt is unknown. Only the same
live backend can reconcile the point: a newer S:AI observation must show one
fewer available point, exactly one more selected base attribute and unchanged
other base attributes. It then persists a confirmed receipt with before/after
evidence. Querying the same handle or observing can reconcile; neither retries
the write. A recreated backend leaves old unknown receipts for review rather
than inferring delivery from matching numbers.
Reusing a backend under a new ownership generation discards its old in-memory
confirmation candidates while retaining durable unknown receipts. The
allocation gate uses an existence query across the character's entire unknown
receipt history; the separately bounded UI list is not used for that decision.

The native play Skill uses this policy between tasks. During an active leveling
handle, the coordinator applies the same configured policy between acknowledged
actions, while the living character is in world state outside battle and trade.
It keeps the original leveling targets and submits at most one point per tick,
waiting for the durable receipt's authoritative confirmation before continuing.
An old unknown receipt pauses the task instead of replaying an allocation.
Manual MCP allocation is blocked while a task is active. Reaching a target at
Start, Resume or Tick does not bypass configured preparation; supervisor goal
completion also waits for the handle and `build:settled` observation.

A level change invalidates the previous available-point count. The adapter
refreshes unknown counts read-only; read-only waiting does not reset progress
deadlines. Ownership and cancellation are checked before and after preparation.
The native leveling Skill is 1.2.0 (tree digest
`efa295cb1e767ed92de8476d583407d50e10a93e74aedf175c82f792e7895bab`).
Component checks for aigame, aileveling, aiservice and aimcp passed in
`build/ai/leveling-build-components.log`; focused coordinator/service race checks
and vet passed in `build/ai/leveling-build-focused-race.log` and
`build/ai/leveling-build-vet.log`. Full real-model/leveling validation remains
deferred to the consolidated integration phase.
The Web status display also consumes this optional point count, including zero;
older responses without the field preserve the existing count. Invalid or
duplicate point fields are rejected. Basic Go component checks, the C read-only
observation harness and `client/web/ai_observation_test.js` passed. Logs are
`build/ai/stat-allocation-basic.log` and `build/ai/stat-observation-c-basic.log`.
The new policy/store, service receipt, admin, prompt and Skill-install checks
passed in `build/ai/character-build-basic.log`; focused policy/receipt/builder
race checks passed in `build/ai/character-build-focused-race.log`.
The final native play Skill is 1.1.0 (tree digest
`f0bc11b4ca6af46e8f4b4673d522695303a5080b6afa5a5ad43ed85f44f731d7`);
installation/hash checks passed in `build/ai/character-build-skill-check.log`.
`go vet` also passes for the runtime and gameplay service packages. No live
game, model request, image build or deployment was used for these changes.

- `internal/aicontrol`: control ownership, generation fencing and cancellation.
- `internal/aigame`: headless game protocol, observable state and validated actions.
- `internal/aiknowledge`: versioned game facts, task definitions and route/budget evidence.
- `internal/airuntime`: AI profiles, memory, execution records and model configuration.
- `internal/aimodels`: embedded DeepSeek-Flash metadata and private Codex configuration generation.
- `internal/aicodex`: official Codex process and session lifecycle.
- `internal/aimcp`: character-scoped MCP tools and native game Skill installation.
- `internal/aiservice`: private MCP capability gateway, authenticated game-state projection, fenced submissions, durable action receipts and deterministic task lifecycle.
- `internal/aiplanner`: verified task-to-plan compilation and a time-gated warp graph. Warp connectivity alone is not tile navigation.
- `internal/aisupervisor`: Codex turn lifecycle and continuation from observed game events.
- Automation execution joins these packages; the Web client and admin expose their respective user flows.
- GMSV retains authoritative gameplay; SAAC retains character persistence. AI state does not replace either.

Adult-ceremony source mapping identified a solo-entry requirement in addition
to the level gate. The optional own-state field `party_mode` now reads the
caller's `CHAR_WORKPARTYMODE` (0 solo, 1 leader, 2 member). Headless parsing
rejects invalid/duplicate modes, treats omitted fields as unknown, and
invalidates old mode evidence on login/logout, party rows, acknowledgements
and outgoing party requests, including uncertain writes and manual Web traffic.
Connected world observations expose `party:known` and `party:solo`; task authors
can use the existing `flag_set` predicate for the latter. An empty party list
is never substituted for server evidence. This adds no automatic party change.

The C observation harness, aigame/aiservice component suite, focused race checks
and vet passed (`build/ai/party-observation-{c,components,go,vet}.log`), with
manual/failed-delivery checks in `build/ai/party-observation-delivery.log`.
The adult task itself remains unverified pending inventory/route/pet preparation
and later integration. No server binary was replaced for this extension.

## Model integration reference

Integration references:

- https://developers.openai.com/codex/noninteractive
- https://developers.openai.com/codex/skills
- https://developers.openai.com/codex/mcp
- https://developers.openai.com/codex/config-reference
- https://api-docs.deepseek.com/quick_start/agent_integrations/codex

The DeepSeek-Flash catalog is embedded with source and SHA-256 provenance in `internal/aimodels`; its declared minimum Codex version is `0.144.0`. Only the selected Flash entry is included. Its default reasoning effort is `high`, with `low` and `max` also supported. The official 1,048,576-token context and text/image capabilities are retained.

The model is configured by the operator. API keys and game login secrets must never appear in prompts, browser responses, repository files or audit text. Codex adapter tests use an isolated fake executable; real provider verification uses the user-provided, ignored `vendor/deepseek/key` through private runtime configuration without printing its content.

## Verified evidence (2026-09-15)

- Main-agent reviewed and independently applied `0014-safe-enemy-group-bounds.patch`, ran the patched lookup-guard regression, compiled the actual `char/enemy.c` and statically linked GMSV using the existing local compiler image without pulls or image builds. Missing group/enemy references fail explicitly; legitimate empty slots remain allowed. The regression is wired into release CI. Isolated QA now runs SHA-256 `95fd6cb85005061cc3427fa84b5d1606d162e8e5c9494f0659aa55c49e001ab1`, independently checked against `/proc/1/exe`. Live pickup/checkpoint recovery/relogin passed again on that binary (`build/ai/enemy-bounds-qa-20260915/gift-smoke.log`). The original 2.5 data was not modified, missing enemy groups remain unresolved, and no affected encounter route was exercised. This is defensive code and normal-gameplay smoke evidence, not completed combat or full exchange verification.
- Extended main-agent live pickup QA passed after an injected post-submission checkpoint-write failure, actual game-session close/relogin and SQLite close/reopen. The new session confirmed one flower 2415 and `now:2`; the engine reconciled the durable `prepared` checkpoint and retained exactly one NPC submission. Evidence fields `checkpoint_recovery_verified` and `relogin_pickup_verified` are true in `build/ai/gift-pickup-live-evidence.json`; log: `build/ai/gift-checkpoint-live.log`. Race tests and vet passed. This does not establish operating-system crash recovery, model-container recovery or the full multi-NPC exchange.
- Main-agent `TestLiveGiftPickupFreshAI` passed with the race detector after bounded recovery for explicitly unsent stale moves and invalidating old visible actors on floor changes. The freshly provisioned character legally reached `1000 (56,124)`, talked to 日美子 at `(57,124)`, selected YES 4 in request window 234 (buttons 12), and received flower template 2415, `now:2` and window 231. The two NPC source files match the actual QA data mount. Evidence: `build/ai/gift-pickup-live-evidence.json`; log: `build/ai/gift-pickup-live.log`. Main-agent review confirmed one flower and the task flag, and independently confirmed gateway cleanup. Related race tests and vet passed (`build/ai/gift-remediation-tests.log`). This verifies pickup and nonempty inventory observation, not the complete exchange, live leveling, or model-container execution.
- Main-agent review and independent race tests passed for complete leveling-route risk checks, constrained safe detours, and warp-destination validation across `ainavigation`, `aiservice`, `aileveling`, `aiknowledge` and `aiplanner` (`build/ai/route-risk-integration.log`). The route is checked beyond the first submitted movement packet. Positive-probability encounters with incomplete evidence remain unavailable; zero-probability rows do not require enemy groups. This establishes the route guard, not a live safe path from hometown 0 to a leveling area or sustained combat.
- Main-agent whole-repository `go test -mod=readonly -race -p 2 ./...` passed; output is retained in `build/ai/race-integration.log`. The Docker CLI tests also passed three consecutive isolated runs. The earlier stdin-forwarding timeout did not reproduce at this bounded package concurrency; no production timeout was relaxed. This run predates the subsequent leveling pending-action confirmation fix and is not evidence of sustained gameplay or complete quests.
- The embedded riding task now distinguishes the player's movement destination `(60,45)` from the NPC identity coordinates `(61,45)` in `npc.talk`. Main-agent race tests passed for the task shape and its actual NPC argument validator; the former definition would be rejected before sending a conversation. This corrects the static execution contract but does not change the task's `unverified` status.
- Main-agent review and independent race tests passed for leveling pending-action confirmation. Intermediate positions and unrelated character/pet updates do not acknowledge a move or reset its deadline; a known warp requires its destination, and battle-end waits for the server to leave battle. Navigation-to-coordinator integration and real gameplay remain separate verification work.
- `Knowledge.EncounterAt` follows the effective 2.5 encounter selection rule (highest positive ZOrder, first source row on ties), with independently run race tests. A static 482-step route on floor 100 from `(637,491)` to area 28 at `(155,567)` crosses effective areas containing enemies up to level 10. A low-level destination alone does not make its approach suitable for a new character; the route guard now checks that approach, while live verification of a safe reachable leveling area remains outstanding.
- Main-agent execution of `TestLiveMovementCrossMapFreshAI` passed with the race detector against the real isolated QA server. A freshly provisioned character moved from `1006 (15,22)` to the known source `1006 (10,20)`, submitted native `EV`, and received authoritative destination `1000 (98,44)` at revision 41 on the latest extended-observation QA run. The test hashes mapwarp/mapset and both floor files against the actual QA data mount. Main-agent Docker inspection confirmed that mount, and an independent connection check confirmed that the temporary gateway closed. Evidence: `build/ai/cross-map-live-evidence.json`, log: `build/ai/cross-map-live.log`. This verifies one real warp through MovementSkill, not sustained leveling, quest completion, an actual unlimited-funding charge, or the AI model container. Leveling's two-stage map-event integration remains separate work.
- Savepoint observation projection and Go protocol parsing have independently passed race tests for signed bit 31, an explicit zero mask, missing fields and malformed/duplicate values. The independent C observation harness passed its read-only and scope checks, including `CHAR_SAVEPOINT` output. Quest conditions use `savepoint:<bit>` from the optional authenticated own-state response. This is the persisted `CHAR_SAVEPOINT` bitmask used by `NPC_SavePointCheck`, not an inferred hometown or current map. Main-agent QA cutover and live verification passed for binary SHA-256 `6a8b3c6adab325b41ec832a757e18a66b602818488b2b16798a85c7c52562bff`; the actual running `/proc/1/exe` hash matches. The real fresh character reported savepoints `1` and a known empty backpack after an explicit `S("AI")` request. Both source files match the current observation module; compile evidence is under `build/ai/observation-qa-20260915/`.
- Main-agent gameplay integration tests passed with the race detector across `aigame`, `aiservice`, `aileveling` and `aiknowledge` (outputs: `build/ai/gameplay-integration.log`, `build/ai/observation-ev-tests.log`). Template-based inventory conditions use the reserved `item:<id>` namespace; tests cover duplicate templates, renamed display items, an explicit empty backpack, missing observations and display-name collisions. Main-agent C harness and protocol reviews also verified the optional `items` field, scoped to the authenticated character's backpack; the real QA server has now reported a known empty backpack, with nonempty flower inventory subsequently verified by the gift-pickup QA above; full quest execution remains pending. The final integration run includes the leveling coordinator's separate EV submission and destination-confirmation stages.
- `internal/aicontrol`: race-enabled tests verified stale-action fencing, competing start requests, takeover ordering and closed-session rejection.
- `internal/automation`: race-enabled tests verified durable uncertain-delivery checkpoints, no blind retry, identified-pet level stopping, budget boundaries and checkpoint CAS.
- `internal/airuntime`: main-agent independent race-enabled tests passed for profiles, model configuration, secrets, memory and token accounting. Full runtime integration is still outstanding.
- `internal/aimodels`: embedded metadata digest/capabilities, private configuration files, exact unattended policies, unsupported reasoning levels and minimum CLI version checks passed.
- Opt-in `TestLiveDeepSeekCodex` passed with the actual local `codex-cli 0.154.0` and `deepseek-flash`. It verified thread-start, turn-completion and an exact expected answer through generated project configuration; reported usage was 9,123 input tokens and 11 output tokens. This was a model connectivity check, not a game/Skills/MCP acceptance test. All temporary state stayed below `build/ai`; no credentials were printed.
- Main-agent independent race-enabled tests passed for `internal/aigame`, `internal/aicodex`, `internal/aimcp`, `internal/aiservice`, `internal/admin` and `internal/aiplanner`. These are module tests and do not establish the complete gameplay acceptance ledger.
- `internal/aiservice` tests cover immutable character binding, takeover rejection, revision revalidation, no replay on receipt polling, unknown action persistence across database reopen, and exact pet identity projection. A packet write does not produce a confirmed game receipt.
- Deterministic completion checkpoints now retain the authoritative observation that satisfied the plan. Older completion records without observation evidence are reported as unknown by the service until reconciled.
- The live MCP fixture and Factory integration tests exercised actual DeepSeek/Codex turns calling the stdio `game_observe` tool against a local fixture. Both returned a random marker available only in the installed native Skill, verifying skill reading as well as MCP execution. Evidence is recorded privately under `build/ai/mcp-live-evidence.json` and `build/ai/factory-mcp-live-evidence.json`; these tests do not establish behavior against a real game server.
- Main-agent independent race tests passed for operator-path rejection, symlink aliases, generated configuration and the Codex runner. Connection-test paths are checked before creation too; tests use synthetic operator homes and never inspect the actual operator configuration.
- The isolated real QA game test `TestLiveGameSessionMovementAndOwnState` passed through the named-protocol gateway on loopback port 39065. GMSV returned own-state revision 30 with three stable pet identities, then confirmed movement on floor 1006 from (14,19) to (15,19). Port 29065 is the raw numeric GMSV endpoint, not the named-protocol endpoint. This verifies state reading and one legal movement, not sustained leveling or quest completion.
- That QA run used the existing local QA stack with a statically linked GMSV binary whose SHA-256 is `631e949cb402ed8fd1bebe91b0045ea0d4acf048c941086ad9b76d447e59faca`. Only the isolated QA GMSV container was recreated; no production deployment was performed.
- Main-agent execution of `TestLiveCodexFactoryRealGame` passed with actual Codex CLI 0.154.0 and DeepSeek Flash: the installed native Skill's private marker was returned, and the MCP call order was `observe → start_leveling → task_status`. The bound QA character was level 1 on floor 1006 at (15,19); the level-1 target produced a durable completed receipt containing real character/pet observations. Only `S("AI")` reached the game action boundary. The evidence is `build/ai/real-game-factory-evidence.json`. This verifies an already-reached target, not combat leveling. Its unlimited-funding lookup is a test fixture, not evidence of a GMSV charge or funding authorization.
- Browser QA with a separate admin database confirmed saving the DeepSeek preset and a custom Responses URL/model, preserving the default model, and disabling character creation when runtime dependencies are unavailable. No real provider credential or model connection test was used in that browser session. Screenshots are under `build/ai/admin-ui.psiYdw/`.
- Main-agent independent execution of `TestLiveAIProvisionCreateBindOwnStateRelogin` passed with the race detector against the isolated real QA server. It created a dedicated random account and character, checked the persisted profile binding and private credential file, opened the character, closed its lease, and reopened the same identity. Both sessions received authoritative `S("AI")` state in the connected world phase. Evidence is `build/ai/provision-live-evidence.json`. The test stopped its temporary named-protocol gateway; it made no model request and does not prove a live unlimited-funding charge.
- ContainerRunner race tests passed for durable request identity, exact-thread recovery and cancellable locking. A separate child-process test confirmed that a held profile lock excludes another process and becomes available after release. Supervisor tests cover recorded-result recovery, settlement before checkpoint persistence, original-request recovery through the container transport, and retaining unknown reservations. The pause/continue test opens a new game session while reconciling the original request ID and prompt, with one model dispatch and one settlement. These simulated failure boundaries do not yet establish recovery after killing a real running AI container.
- Token-store tests reopen SQLite around dispatch, result recording and settlement. Unknown requests block replacement reservations even on the next UTC day, and later confirmed outcomes can settle the original reservation. Beginning a new attempt checks recorded actual token usage as well as charged reservations, so a process restart cannot bypass an already exhausted daily budget.
- NPC window contracts now support deriving the window object ID from the uniquely matching visible NPC. Legacy source confirms that riderman WN windows and visible C actor records use the same object index. Independent race tests verify the matching actor path and reject a substituted object, a disappeared NPC, ambiguous matching actors, and a model-supplied object ID. Fixed-object contracts remain supported. The production NPC registry is still empty; this adapter verification does not establish a live riding quest.
- Main-agent rendering of the base and optional AI Compose configurations verified gateway routing, matching broker/pull-target images, the broker journal location, Web read-only gameplay data, and Docker socket mount boundaries. No image was built, pulled or started for this check.

Custom provider configuration generation now preserves the operator's URL and model name. The reserved Codex provider ID `openai` is mapped to the private provider ID `stoneage_openai` so its configured API URL/key are honored. Only `deepseek-flash` receives the embedded DeepSeek catalog; generic models use Codex's own model information and optional explicit context settings. The HTTP(S) URL must not embed credentials, query parameters or fragments. Both unattended policies remain fixed for every provider. Generic Responses provider configuration and end-to-end tests cover custom endpoints, model names, provider IDs and empty catalogs.

The main agent independently ran `TestGenericRuntimeWithLocalResponses` with the actual local Codex CLI against a local Responses SSE fixture. Both custom and OpenAI provider configurations sent the configured model and dummy bearer key to `/v1/responses`, completed the turn and returned the expected text. This verifies Codex configuration and protocol wiring without making an OpenAI service availability claim.

Travel movement now has bounded solo-PvE escape recovery: an explicitly
observed encounter may trigger at most five player turns within 30 seconds,
with at most eight recovered battles per movement action. It rejects duels,
mixed parties/allied players, dead characters and lost control. Only a terminal
server result allows EO; uncertain movement writes and position timeouts do
not trigger automatic resubmission. The live gift journey verified eight
successful escapes and continued movement, then stopped at its battle limit
with HP 1. Healing, resupply, suitable route selection and full exchange remain
unverified; see `docs/ai-quest-sources.md` and
`build/ai/gift-exchange-evidence-3120637279.json`.

The MCP battle vocabulary is now translated before protocol submission:
`attack` uses the observed battle target, `defend`/`guard`, `escape` and player
`wait` require no index, while `pet`, `item` and character-magic `skill` use
separate zero-based `index` and `target_id` fields. Pet default uses both fields
set to 255. The observation exposes the local battle slot, menu flags and
separate player/pet readiness. Player and pet submissions remain separately
locked until a new authoritative BP; a BC refresh cannot replay the player
command. Raw action receipts still require game-specific outcome reconciliation;
these protocol fixes and deterministic travel tests do not establish sustained
model-directed combat or completed final runtime-image acceptance.


自动赶路回血已独立实测通过：`TestLiveMovementItemRecoveryFreshAI` 在正常遇敌逃跑后
由移动流程自动使用小肉，HP 10/35 → 28/35，并确认到达 `100 (310,515)`；撤销无限金钱
能力后重登确认 HP 28、剩余一块小肉。证据见
`build/ai/movement-item-recovery-live-evidence.json` 和
`build/ai/movement-item-recovery-fresh-route.log`。此前战死及状态过期的失败记录保留；
该结果不代表完整礼物路线已可生存，也未调用模型。任务自动准备补给、可达的新手练级路线
和完整交付仍待完成。


## Linux 容器中的真实 Codex 验收

`TestLiveContainerCodexSkillMCP` 已通过。使用本机已有 `gcc:13-bookworm` 镜像，
将交叉编译的 `stoneage-ai-runner`、`stoneage-game-mcp` 和官方 Codex 0.154.0
Linux arm64 文件只读挂入容器；没有构建或拉取镜像。Codex 的官方 npm 包版本为
`@openai/codex@0.154.0-linux-arm64`，下载字节已与 registry 的 SHA-512 integrity
一致性校验，包布局中的 `bin/`、`codex-package.json`、`codex-resources/` 和
`codex-path/` 保留在 `build/ai/container-runtime-qa/`。

真实 DeepSeek Flash 回合通过 shell 工具读取已安装的 `stoneage-play/SKILL.md`，
随后调用一次认证 MCP 观测并返回仅存在于响应中的随机值。验收解析了生成的 TOML，
确认 `approval_policy=never`、`sandbox_mode=danger-full-access`、Responses API，
并检查独立 `models.json` 中的 DeepSeek Flash 描述。当前操作者 `.codex` 未挂入。

Docker 实际 inspect 确认用户为 `501:20`（本次测试私有 bind 目录的非 root 属主），
根文件系统只读、capabilities 全部撤销、no-new-privileges，以及仅三个预期 bind：
只读 QA 程序、只读 Skill 源和可写的单 profile 状态目录。测试后的独立检查确认没有
遗留 `sa-codex-qa-*` 容器。API key 和 MCP capability 仅通过私有 stdin 请求传入，
不进入 Docker 参数或容器环境；测试拒绝输出中出现原始凭据。

证据：`build/ai/container-runtime-qa/live-evidence.json`，包括实际镜像 ID、三个程序
SHA-256、模型、usage、Skill/MCP/配置及隔离检查。日志为 `live-isolation.log`，
官方包来源为同目录 `codex-provenance.json`。runner 与命令入口的 race 回归和 vet
均通过。此前测试夹具的 token 长度与 TOML 引号断言错误已修正。

该测试使用认证的模拟游戏观测端点，不修改游戏，也不证明正式 `ai-runtime` 发布镜像、
broker 与真实游戏的完整端到端链路或 UID 10001 发布配置已经验收。后续仍需用正式
发布镜像完成这些检查。官方配置位置与自定义 provider 依据：
https://developers.openai.com/codex/config-advanced/ 。

在准备好的 Linux 程序目录上可显式重跑（默认测试不会调用模型）：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  GOCACHE="$PWD/build/ai/go-cache" GOTMPDIR="$PWD/build/ai/tmp" \
  TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go build -mod=readonly -o build/ai/container-runtime-qa/bin/ \
  ./cmd/stoneage-ai-runner ./cmd/stoneage-game-mcp

STONEAGE_CONTAINER_CODEX_LIVE_TEST=1 \
  GOCACHE="$PWD/build/ai/go-cache" GOTMPDIR="$PWD/build/ai/tmp" \
  TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
  go test -mod=readonly ./internal/airunner \
  -run '^TestLiveContainerCodexSkillMCP$' -count=1 -v
```


## 容器 Agent 连接真实 QA 角色与会话恢复

`TestLiveContainerCodexRealGameResume` 已通过。测试通过现有 provisioner 新建专用
QA 账号和角色，使用真实 named-protocol gateway、`GameBackend`、`OwnStateRefresher`
和私有 HTTP `Gateway`。服务端 `/proc/1/exe` 的 SHA-256 与已审核 observation 构建一致。
模型通过原生 Skill/MCP 读取服务端角色名称、楼层、坐标和 HP，结果与网关实际转发的
权威观测逐项比较，并验证背包身份已知、角色绑定未变化。

第一只模型容器完成后被删除，第二只容器复用该 profile 的独立状态目录，显式指定
第一回合的 thread ID 恢复。第二回合再次调用 `game_observe`，保持同一 thread，且
正确回忆了仅第一回合提供的随机口令。两次观测均为真实初始状态：楼层 1006、
坐标 `(15,22)`、HP 35；不是在提示中预先给模型答案。完成后撤销角色 capability，
用原令牌访问网关得到 HTTP 401。

测试只暴露观测端点，其余 MCP 操作不可用；底层 QA session 仅允许 `S("AI")` 状态
请求，任何其他动作在提交前被拒绝。测试还记录所有非观测 backend 操作，并断言
尝试次数为零。两次观测的 Connected/World 状态、等级、最大 HP、已知存档位 0
和至少一个服务端稳定宠物 ID 均一致。两回合均无游戏修改动作。
Docker 实际检查确认非 root、只读根、capabilities 全撤销、no-new-privileges、
三个挂载的确切来源/读写属性及退出码 0；主代理另行确认没有遗留
`sa-game-codex-*` 容器。原有 fresh-character 夹具负责关闭会话、临时 named gateway
及临时存储。

证据：`build/ai/container-runtime-qa/real-game-evidence.json`；日志：同目录
`real-game-final-race.log`。加强后的真实模型/真实游戏测试已带 race 检测通过，
静态检查也通过。执行入口是显式 opt-in 的
`STONEAGE_CONTAINER_REAL_GAME_LIVE_TEST=1 go test ./internal/aiservice
-run '^TestLiveContainerCodexRealGameResume$' -count=1 -v`，Go 缓存与临时目录沿用
前述项目内设置。测试复用已存在 Linux 基础镜像和已准备的程序，不拉取或构建镜像。

这是两个真实模型容器与真实游戏的只读链路、thread 恢复和令牌撤销验证，仍未覆盖
正式发布镜像、broker 的完整 Factory 调度、模型驱动的付费/任务操作、完整礼物任务
或持续练级。不能将本次只读结果记为这些功能已完成。

### 真实自动练级：出生地 1 到角色 2 级

`TestLiveLevelingFreshAI` 已通过 race 检测（40.18 秒游戏测试）。测试正常创建出生地 1
的角色，通过 `NewGameplayBuilder`、`GameBackend.StartLeveling` 和后台 Coordinator
执行 area 28、角色目标 2 级；使用实际知识库和碰撞导航，不替换路线或注入角色状态。
角色从 `2006 (20,16)` 出发，经村庄 2000 到野外 100，观测到两场战斗，最终在
`100 (127,614)` 达到 2 级、HP 32，退出战斗后检查点成为 completed。

本次修复了自动练级等待移动确认时遗漏原生 `S("c")` 查询的问题：原生 `W` 不保证
回报自己的最终坐标。待确认的移动现在会查询服务端坐标；成功写入查询不会确认移动，
也不会重发 W 或延长原操作期限。版本竞争等待下次观察，其他查询失败暂停并保留
未确认移动。原生会话和已持有控制权锁的 Web 会话均有回归覆盖。

证据：`build/ai/leveling-live-evidence.json`、`build/ai/leveling-live.log`。
知识摘要为 `4244c8871551d59232ad249299d3998bf38b2791afc5a74d4e2218e018458177`；
测试核对运行中的 QA 二进制、有效地图及遭遇/敌人数据。回归与静态检查日志为
`build/ai/leveling-refresh-regression.log`、`build/ai/leveling-refresh-vet.log`。
真实测试开关为 `STONEAGE_LEVELING_LIVE_TEST=1`，仍需指定有效 QA 数据目录并使用
项目内 Go 缓存与临时目录、`-mod=readonly`。

这证明了生产 Go 练级入口的一次真实升级，尚不代表模型容器驱动的完整练级链路、
长期补给恢复、宠物升级或出生地 0 礼物任务已验证。

### Codex 容器通过原生 MCP 完成真实练级

`TestLiveContainerCodexLeveling` 已通过 race 检测（52.18 秒）。官方 Codex 0.154.0
在独立容器中运行 DeepSeek Flash，读取安装的 `stoneage-play` Skill，依次调用
原生 `game_observe`、`game_query_knowledge`、`game_start_leveling` 和
`game_task_status`，最后再次观测。测试逐项检查成功的原生 MCP 事件、实际后端调用
顺序、唯一的练级启动、服务端状态及持久化检查点；没有脚本代替模型启动任务。

正常创建的出生地 1 角色从 1 级升到 2 级，最终位于 `100 (115,614)`、HP 34、
已退出战斗；receipt 为 `confirmed`，检查点为 `completed`，模型最终 JSON 与
权威观测一致。记录了 45 次移动、2 次地图事件、8 次战斗指令，其中 4 次是宠物
攻击指令。这证明指令实际写入，不单独证明宠物伤害归因。测试后没有遗留的
`sa-game-codex-*` 容器。

这轮同时覆盖了已修复的知识库 verified 标记、移动提交前版本竞争的有限重规划、
失败检查点原因保留，以及原生 W/KS 宠物状态解析。DeepSeek 的运行时模型目录将
`supports_search_tool` 设为 false，使游戏 MCP 工具直接呈现；上游嵌入目录原文及其
来源哈希保持不变，安装文件使用自己的实际哈希。OpenAI 和通用自定义模型配置
不受该 DeepSeek 配置调整影响。

当前证据为 `build/ai/container-runtime-qa/leveling-evidence.json`，日志为同目录
`leveling-live.log`，原生 MCP 事件为 `native-mcp-events-leveling-1.json`。重跑入口：

```sh
STONEAGE_CONTAINER_LEVELING_LIVE_TEST=1 \
STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR="$PWD/build/player-integration/game/gmsv/data" \
GOCACHE="$PWD/build/ai/go-cache" GOTMPDIR="$PWD/build/ai/tmp" \
TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
go test -mod=readonly -race ./internal/aiservice \
  -run '^TestLiveContainerCodexLeveling$' -count=1 -v
```

该测试复用已存在的基础镜像和挂载程序，没有拉取或构建镜像。正式发布镜像、broker
完整调度、持续练级时的技能刷新与补给恢复、出生地 0 完整礼物任务仍未因此验收。

### 连续升级后的宠物指令验证

新增的 `TestLiveLevelingContinuousAI` 已通过 race 检测（137.13 秒），同一正常新建
角色由生产 Go 协调器连续从 1 级升到 3 级。首次观测到 2 级时累计写入 21 次宠物
攻击指令，最终累计 38 次；最终位于 `100 (155,659)`、HP 23、非战斗状态，检查点
completed。最终宠物技能快照含 2 项。因此此前单次终态快照的 `Skills=null` 不足以
证明技能永久丢失，不能据此添加技能继承或假定技能槽的补丁。

实测使用 `STONEAGE_CONTINUOUS_LEVELING_LIVE_TEST=1` 和与上节相同的有效数据、缓存
设置；测试名为 `TestLiveLevelingContinuousAI`。独立证据为
`build/ai/leveling-continuous-evidence.json`，日志为 `build/ai/leveling-continuous-before.log`。
模型目录、runner、原生游戏会话、练级协调器及服务整合的 race 回归与 vet 通过，日志
分别为 `build/ai/continuous-leveling-regression.log` 和 `build/ai/continuous-leveling-vet.log`。
这次连续测试未调用模型，不代替前述模型容器验收；也不证明补给、复活或任意时长运行。

### 练级中的随身物品恢复

练级协调器现在在已确认的世界状态、没有未决操作时检查生命值。低于最大 HP 的一半
（向上取整）时，先调用与赶路流程共用的 `TravelItemRecovery`；缺少配置则暂停。
只消费背包里已有且来源已核验的恢复物品，不在此接口内购买物品。实际物品模板、
背包身份、一次消耗及 HP 增加仍由 `ItemHealingSkill` 的服务端观察确认。

恢复调用前持久化 `prepared`，单次调用受剩余任务时长及 10 秒上限约束。调用或确认
失败保留未决状态并暂停，禁止自动重复消耗；成功后还需确认角色、位置、等级、最大 HP
未变化且实际 HP 增加，再恢复 `ready`。下一 tick 重新观察，才决定继续恢复或移动。
没有恢复证据不能因函数返回成功就继续走动。缺货自动采购和复活仍未接入；宠物世界治疗见后续记录。

配套实测在准备伤势时暴露了两个赶路版本竞争问题，已加入回归修复：已确认战斗/治疗
恢复后，从一次新观察重新规划，避免连续两次采样造成虚假的初始版本冲突；原始调用的
ExpectedRevision 仍严格校验。地图事件仅对协议层明确的发送前 ErrStaleRevision 做
最多三次尝试，每次重新检查世界状态、切换源坐标及目标区域生命要求。位置变化、其他
写入错误、负面或缺失 EV 回执均中止，不重放已经提交的地图事件。

`TestLiveLevelingItemRecoveryFreshAI` 已加入显式开关
`STONEAGE_LEVELING_ITEM_RECOVERY_LIVE_TEST=1`，目标是正常购买、自然受伤、生产 builder
中的练级协调器自动恢复、随后撤销资金能力并重登核验。目前该真实整合测试**尚未通过**：
早期运行发现并修复上述赶路竞争和测试回执存储漏接；最新运行完成购买，在旅途中先以
19/35 HP 逃脱，随后战斗死亡，未进入世界状态下的自动恢复验证。
日志为 `build/ai/leveling-item-recovery-live.log`，诊断为
`build/ai/travel-diagnostic-144128129.json`。不得将之前赶路恢复的成功证据当作此次
练级恢复已通过。后续应使用已验证低级区域及对应出生地的补给来源准备伤势。

本轮练级/服务/Web race 回归通过，记录于
`build/ai/leveling-health-final-regression.log`；静态检查为
`build/ai/leveling-health-vet.log`。新增覆盖包括恢复前持久化、逐次确认 HP 后再移动、
异常结果保留 prepared 且不重试、无补给时停止导航、恢复后的版本变化，以及地图事件
未发送/已移动/未知写入的区别。

### 出生地 1 补给与战斗恢复缺口

目录新增 `marinas-meat-shop` 和 `hometown-1-small-meat`，保留原 `small-meat`
供货条目。原始 `npc/genout/shop_m.create:1090` 定义玛丽那丝肉店在
`2004 (17,13)`，使用 `npcgen_shop|file:genout/ss_2004_17_13`；交互点为 `(17,15)`。
商店 ItemList 为 `2344-2347`，buy_rate 为 1.0，小肉 itemset 基价 12。
`special_rate:0.8` 属于 `NPC_GetSellItemList` 回购计算，不改变玩家购买价。
地图记录允许 `2000 (92,77) ↔ 2004 (15,21)`，可从老人家 2006 经村内到达。

`TestLiveLevelingSuppliedAI` 通过正常建角、生产 StockSkill 在村内购买五份肉，然后
请求 area 28、目标 5 级，并要求实际执行过恢复物品。实测已确认购买与 QA 无限资金
能力生效，但完整练级测试尚未通过：首次运行宠物死亡后持续攻击，角色随后死亡。
该失败记录保留于 `build/ai/before-retreat-leveling-supplied-live.log` 和对应 evidence。
测试核对实际 QA 的 2006、2000、2004、100 地图及商店、itemset、mapwarp 文件。

已增加战斗撤退判断：使用实时战斗名册中的自身与 MyNo+5 宠物 HP，低于半血时发送
E，宠物等待。E 仅是尝试，沿用原有 prepared/待确认回合逻辑，不能认定逃跑成功。
人物菜单禁用时仍发送 N；已经死亡的人物仍暂停。最初版本在世界状态下遇到受伤的
出战宠物会暂停；后续已接入下述宠物治疗实现。

后续宠物治疗依据已核对：`callfromcli.c:52` 将 toindex 1..5 映射到宠物槽 0..4，
0 是自身；`ITEM_useRecovery_Field` 可增加目标 HP，但不会清除死亡旗。`battle.c:1207`
在结束战斗时清除死亡旗并将死亡宠物 HP 设为 1。K 状态没有死亡旗字段，因此必须在
确认退出战斗后治疗，并用稳定宠物身份、实际 HP 增加及物品减少确认，不把肉当作
战斗中的复活指令。后续实现与实测见下文。

撤退修复后的真实运行（95.30 秒）记录了一次 E 和 `W|FF|FF`，服务端随后确认退出
战斗；人物 HP 25/35，宠物 HP 19/42，检查点以“出战宠物需要治疗”暂停。目标 5 级
尚未达成，测试保持失败；不能把安全暂停计为持续练级完成。该次证据现归档为
`build/ai/before-pet-healing-leveling-supplied-evidence.json`（passed=false），日志为
`build/ai/before-pet-healing-leveling-supplied-live.log`。终态 items_known=false 代表背包观察已失效，
不是五份肉已用完；本次没有物品使用提交。后续治疗须先重新刷新背包与宠物身份。

商店目录及原始数据一致性回归通过，日志 `build/ai/hometown-stock-regression.log`；
练级、服务与 Web race 回归通过，日志 `build/ai/supplied-leveling-regression.log`；
静态检查通过，日志 `build/ai/supplied-leveling-vet.log`。


### 宠物治疗已接入，持续练级仍未通过

`TravelItemRecovery.HealPet` 通过生产 builder 已有的恢复对象向练级协调器提供
`PetHealthRecovery`。仅在退出战斗、人物存活且宠物有有效 HP 时执行；先查询关联的
AI 背包与宠物身份，校验出战槽、稳定身份、外观、名称、等级及最大 HP，然后只发送
一次原生 ID（目标为宠物槽 + 1）。确认必须同时满足同一宠物实际 HP 增加和对应模板
物品数量减少一份。失败或未知结果保留 prepared 并暂停，不自动重放物品使用。

最新真实 QA 运行耗时 255.66 秒，目标仍为 5 级，结果 **失败**：240 秒任务时限到达时
人物仅为 2 级。期间实际提交 `ID` 背包槽 5、目标 1，肉从五份降为四份，宠物从
18/39 HP 恢复到 37/39；检查点回到 ready，之后宠物攻击提交从 16 次增加到 19 次。
这证明已完成一次宠物治疗并继续战斗，不能证明已完成持续练级或自动补货。
该次证据归档为 `build/ai/pet-healing-confirmed-leveling-supplied-evidence.json`，日志为
`build/ai/pet-healing-confirmed-leveling-supplied-live.log`。后续测试采样增加人物和宠物 EXP/MaxEXP，便于
核对低级区战斗收益与升级进度；上述旧运行不包含这些新字段。

宠物治疗及发送前身份变化测试已通过，四包 race 回归记录为
`build/ai/pet-recovery-regression.log`，静态检查记录为 `build/ai/pet-recovery-vet.log`。

稳定 ID 暂未知时，适配器额外要求刷新前后的本地 Identity/IdentityEpoch 相同，避免
同槽位同外观的另一只宠物继承旧治疗请求；同一本地身份补齐稳定 ID 仍可继续。新增
同身份补齐和槽位重用测试通过，记录为 `build/ai/pet-recovery-identity-regression.log`。
此次加固发生在 EXP 采样实测启动之后，不能将该次实测作为加固版本的真实验证。

EXP 采样实测已结束（255.99 秒）：`leveling-supplied-evidence.json` 的 passed=false，
240 秒任务限额到达时人物为 3 级、EXP 7/18、HP 22/35，宠物 HP 44/47。三次实际
物品目标分别为 `[5,1]`、`[6,1]`、`[7,1]`，背包肉从五份减少到两份，宠物攻击提交
59 次。经验与升级均有实际进展，尚未达到 5 级；不是缺货暂停，也不能证明自动补货。
下一步须扩大真实运行观察时段并验证补货链路，继续保持 5 级与实际恢复的完成条件。


### 自动补货接线与长时验证

练级请求支持 `parameters.supply_item`（已核验 StockContract 的 alias）、
`supply_target_count`（默认 10，范围 2..13）、`supply_reorder_count`（默认 3，
至少 1 且小于目标库存）。配置阈值必须同时提供供货条目。示例：

```json
{"area_id":28,"supply_item":"hometown-1-small-meat","supply_target_count":10,"supply_reorder_count":3}
```

`LevelingStock` 在世界状态且完成必要治疗后查询真实背包，库存达到触发阈值时生成
保守报价（目标库存 × 核验单价，覆盖途中恢复消耗）。协调器检查预算并持久化
prepared/ReservedSpend，然后调用生产 StockSkill 返回已核验商店，仅购买当前缺额、
保留两个空栏，并确认物品数量和金额。协调器再次确认角色、商店位置和目标库存才
恢复 ready；下一 tick 由已有练级导航重新前往所选区域。治疗接口没有嵌套购买，
采购途中只能使用现有恢复物品；无物品、路线不通、未知写入或不完整回执均暂停。
预算保守占用不会在成功后按推测返还；不是无限资金时仍受 reserve_gold/maximum_spend 限制。

区域和供货参数随 encounter 步骤一起存入计划，恢复时重新读取；prepared 采购不会
因重启而自动重放。API 参数白名单也已接通。首次长时测试在该白名单处失败，运行
15.66 秒、尚未进入练级，日志归档 `build/ai/restocking-parameter-rejection.log`。

长时测试仍要求 5 级与真实恢复，新增至少两次采购提交（初始采购和后续自动补货）；
基于此前 240 秒只能到 3 级的证据，将观察预算扩至 1200 秒。使用
`go test -timeout 25m`，整体测试上下文为 22 分钟，单次采购仍受 2 分钟及剩余任务时间约束。
上一轮 3 级的终态证据已归档 `build/ai/before-restocking-leveling-supplied-evidence.json`。
当前新一轮真实验证尚在运行，不能把自动补货接线视为全链路已通过。

自动补货接线、API 选择器传递、持久化恢复、数量/位置确认、预算拒绝、接管与未知
结果不重放的回归已由主代理验证：`build/ai/leveling-restock-regression.log` 包含
aileveling、aiservice、aimcp 和 Web 的 race 检查通过；静态检查通过，记录为
`build/ai/leveling-restock-vet.log`。这些测试不替代仍在运行的真实补货链路验证。


自动补货第一次实际触发的运行在 219.67 秒暂停：人物 3 级 EXP 6/18，第二次治疗后
肉剩三份，尚无第二次购买。错误发生在 Quote，旧回执只写通用原因，未能确定是容量
还是其他计划条件，不能视作采购成功。该记录已归档
`build/ai/restocking-quote-rejection-evidence.json` 和对应 live.log。已补充安全的报价
诊断（背包空位、采购所需栏位、保留栏位）及终态物品槽/模板证据，重新进行真实验证。
尚未根据猜测降低补货目标或丢弃战利品。

原生 `stoneage-leveling` Skill 更新为 1.1.0，并更新固定目录摘要；现在说明供货
选择器、预算、服务端采购确认与禁止绕过未决采购。此前供货 alias 仅能来自任务配置；后续已增加下述服务端供货发现接口。Skill 通用校验脚本因本机缺少 PyYAML 未能运行；
项目自己的版本、摘要、安装及重复安装验证已通过，见
`build/ai/leveling-skill-validation.log`。原测试把所有 Skill 版本写死为 1.0.0，已改为
核对各目录条目版本。最新 aileveling/aiservice race 通过，旧合集中的该 Skill 断言
失败由上述单独 aimcp 全包 race 通过记录替代；静态检查通过。


### 原生 Agent 的供货发现

`game_query_knowledge` 支持 `kind: "rule", text: "supply"`，返回当前生产 builder
实际配置且通过来源指纹与商店规则校验的条目。条目 ID 为 `supply:<alias>`，内容含
alias、物品模板、单价、NPC 名称、商店地图与交互坐标。知识查询与执行复用同一份
已复制的供货目录；未启用、来源不匹配或不合法的条目不会被宣传为可用。Verified
只证明供货事实，实际赶路仍由执行层核验，不能据此假设任意起点都能安全到达。
原生练级 Skill 已说明该查询及选择方法，并同步固定摘要。供货发现、禁用/不合法
条目过滤、生产 builder 接线及 Skill 完整性回归通过，日志为
`build/ai/supply-discovery-regression.log`、`build/ai/supply-discovery-negative-regression.log`；
静态检查记录为 `build/ai/supply-discovery-vet.log`。目前未用真实模型验证新查询决策。


### 补货拒绝根因：漏认掉落恢复肉

诊断运行在 341.04 秒确认失败原因：背包 free=1，而补到 10 份需要 purchase=7、
reserve=2。人物 3 级 EXP 16/18，库存包含三份 2344 与模板 1234、1612、2044，
并非资金不足。原始物品表核查发现这些掉落物全是恢复肉：itemset.txt 第 1229 行
1234 小块肉（ITEM_useRecovery，参数体20）、第 1541 行 1612 乌力斯坦的肉
（体10）、第 1826 行 2044 贝恩达斯的肉（体10）。没有出售或丢弃这些物品。
该失败及完整背包槽/模板证据归档为
`build/ai/before-loot-healing-leveling-supplied-evidence.json` 和对应 live.log。

恢复目录新增上述三种掉落物，仍绑定原始 itemset SHA-256 与知识指纹，人物和宠物
恢复适配器复用原有逐次消耗及实际 HP 确认。补货触发阈值现在计算所有已核验恢复
物品的独立背包槽；别名重复不会重复计数，未核验、来源不符、非背包槽均不计入。
采购 target_count 仍指所选供货模板的目标库存。这样不会在背包已有大量可用恢复肉
时，只因商店小肉低于阈值而强行采购。原生 Skill 已同步说明并更新固定摘要。
原始数据校验及满包恢复物品识别测试通过，见 `build/ai/loot-healing-regression.log`。

新一轮真实练级已启动，仍保留原有 5 级、恢复物品使用和自动补货要求，没有把旧的
失败改判成功。测试额外记录每次物品操作前能确认的模板 ID（不能确认时为 0），
以区别掉落肉与商店肉。掉落补给可能改变采购需求；若无法满足既有自动补货实测门槛，
须单独报告该缺口，不能把充足补给下的持续练级等同于完整补货链路证明。

掉落补给修复经独立只读复核后，由主代理验证三包 race 通过（
`build/ai/loot-healing-full-regression.log`），静态检查通过（
`build/ai/loot-healing-vet.log`）。当前真实测试仍运行；不得根据这些单元/整合回归宣称
真实掉落肉恢复或完整自动补货已经通过。


### 返店补货后的真实继续练级证据（运行中）

掉落恢复修复后的长时运行已观察到：在野外触发补货，回到 2004 (17,15)，
StockSubmissions 从 1 变为 2、商店小肉从两份增到十份，检查点为 ready。返店时
人物为 2 级 EXP 0/6、宠物攻击提交 20 次；随后重新进入 floor 100，继续战斗并升到
4 级。该运行仍未终态，不能将这些中间证据当作 5 级测试已通过。

补给实测断言进一步要求样本包含野外→商店且库存确认→回野外后的新攻击和等级增长，
不再只检查采购提交次数。此次断言修改发生在正在运行的测试编译之后；该运行的
最终样本需由主代理另外核对这些里程碑。编译检查为
`build/ai/leveling-proof-compile.log`，不是 opt-in 实测通过记录。

`TestLiveContainerCodexLeveling` 现已接入真实供货目录与 QA 资金能力，安装 play 和
leveling 两个原生 Skill，要求模型先查询区域 28 及 supply 规则，再选地图 2004 的
已核验 alias 启动一次 2 级任务。该短测试验证模型供货发现与初始采购，不能替代
长时 5 级返店补货验证；新版本尚未运行真实模型，旧模型通过记录对应修改前版本。
新增核对出生地 1 的四张实际地图，以及供货知识出现在 start 之前、供货参数传递和
实际采购提交；`build/ai/container-supply-compile.log` 仅证明编译与 opt-in 跳过正常。


### 掉落治疗与返店补货：5 级实测已通过

本轮 `TestLiveLevelingSuppliedAI` 在 1117.68 秒完成，角色从 1 级升至 5 级，
最终检查点 completed、HP 23/35，商店小肉库存 10。当前证据为
`build/ai/leveling-supplied-evidence.json` 和同名前缀的 `live.log`。
主代理额外检查最终样本：先有野外活动，样本 325 回到 2004 (17,15)，库存已知为
10、采购提交 2 次、人物 2 级、宠物攻击 20 次；样本 523 返回地图 100，人物升至
3 级且宠物攻击增加到 33 次，随后最终达到 5 级。因此补货后继续练级的强化条件
也得到实际样本支持（该断言编辑晚于运行程序编译，以上为独立核验）。
11 次恢复操作的模板记录包含 2344、1234、2044，证明商店肉及两种掉落肉均有实际
使用；本轮没有 1612 的使用证据。采购请求为初始 `1|5` 和返店 `1|8`。

容器模型测试进一步核对两个原生 Skill 都有 shell 读取证据、准确的知识查询参数、
目标与时间/死亡预算、所有轮询使用启动返回的同一个 handle，以及采购所在地图和
数量；同时记录并拒绝用额外 game_action/start_task 绕过练级流程的测试结果。
QA 数据路径统一遵循 `STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR`。
这些增强已编译通过，真实模型结果待下轮运行记录，不能据此宣称整个 AI 功能完成。


### 供货后 MCP 状态查询的多行窗口修复

增强的容器测试首次运行 59.07 秒、诊断运行 192.32 秒，均实际完成 2 级任务，
但最终状态查询失败，不能视为模型端到端通过。诊断运行保存在
`build/ai/container-runtime-qa/supply-multiline-reproduction/`，首次失败保存在
`supply-final-observe-failure/`。最终观测中的历史肉店窗口文字包含合法换行：
“店里有上好的肉喔。\n想要哪一个呢？”。它仅 384 字节，其他集合大小也在限额内；
`RemoteBackend.Observe` 使用单行文本规则验证窗口，错误地返回了后端不可用。

现将窗口内容、物品说明和聊天正文限定为显示文本，允许 CR/LF/Tab，仍拒绝 NUL、
ESC 等控制字符并保留原长度上限。角色标识与工具参数仍使用原单行规则。
`TestRemoteObservationDisplayLineBreaks` 经真实 HTTP RemoteBackend 路径验证字符
保留、非法字符和超长拒绝；修复前四个子测试均失败，修复后 aimcp/aiservice 两包
race 及 vet 通过（`build/ai/observation-text-{before,regression,vet}.log`）。
修复后二进制已在项目 build 目录重建，真实模型复测正在进行。


### 原生 Codex + DeepSeek 自主选择补给：真实容器复测通过

修复后的 `TestLiveContainerCodexLeveling` 于 71.41 秒通过。模型读取 play/leveling
两个原生 Skill，先执行 `leveling/id=28` 和 `rule/text=supply` 查询，再选择
`hometown-1-small-meat` 启动唯一练级任务（人物 2 级、all、300 秒、零死亡预算）。
服务在 2004 (17,15) 提交 `1|10`，完成实际移动、跨图及战斗；终态为 confirmed，
检查点 completed，人物 2 级、HP 26，位置 100 (139,614)。全部 13 次任务轮询
使用启动返回的同一个 handle，无额外 game_action/start_task 绕过练级。

证据：`build/ai/container-supply-live.log`、
`build/ai/container-runtime-qa/leveling-evidence.json` 与
`native-mcp-events-leveling-1.json`。主代理独立核对成功的原生 MCP 事件中最后一次
状态查询位于索引 17、随后成功观测位于索引 18，模型最终 JSON 与真实观测一致。
这一原生事件顺序已追加为测试断言并编译通过；追加晚于本次程序编译，以上事件
顺序另行验证，未将失败的观测尝试视作成功。

本轮证明现有 QA 镜像中挂载的官方 Codex 与项目 runner/MCP 能完成此供货练级
回合。证据明确保留 `released_image_verified=false`、`broker_verified=false`；
不能据此声称正式发布镜像、完整 broker 链路、所有出生地任务和长期社交行为已完成。


### Factory/broker 打包镜像整合验收入口

新增 `TestLiveFactoryBrokerCodexLeveling`。它与 QA 挂载模式共享同一组真实供货、
练级和原生 MCP 断言，但通过生产 Factory、持久 broker 和原样调用的 DockerCLI
运行预装镜像。Factory 从独立模型数据库及 SecretStore 读取配置，并签发游戏能力；
测试不生成假模型结果，不覆写 Docker 参数，不挂载工作区二进制，也不拉取或构建镜像。

额外门槛为：实际容器 UID 10001、只读根目录、cap-drop ALL、no-new-privileges、
仅一个 profile 命名卷；同一个调用请求重放不能启动第二个容器；Factory 返回的
thread、文本、usage 必须对应 broker journal；会话关闭撤销游戏 token，宿主机
transport/journal 无明文 provider key 或 token，且没有宿主 Codex/workspaces。
成功证据写到 `build/ai/container-runtime-qa/factory-broker/`，与 QA 挂载模式分开。

运行前须由部署流程提供并预先加载镜像；下面显式 opt-in 会使用 QA 角色、付费模型
请求及测试独有 Docker profile 卷。完成后仅清理已确认容器消失的测试卷，未知结果
保留核查。示例不执行镜像下载或构建：

```sh
# STONEAGE_FACTORY_BROKER_IMAGE 指向已加载的项目 ai-runtime 镜像。
STONEAGE_FACTORY_BROKER_LEVELING_LIVE_TEST=1 \
STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR="$PWD/build/player-integration/game/gmsv/data" \
GOCACHE="$PWD/build/ai/go-cache" GOTMPDIR="$PWD/build/ai/tmp" \
TMPDIR="$PWD/build/ai/tmp" GOPROXY=off \
go test -mod=readonly -race ./internal/aiservice \
  -run '^TestLiveFactoryBrokerCodexLeveling$' -count=1 -timeout 10m -v
```

当前本机没有项目 ai-runtime 镜像，此路径仅编译、静态检查和相关两包 race 通过，
不能声称已完成打包镜像整合实测。用已有 GCC 镜像执行前置检查，在 0.29 秒拒绝其
错误入口，未创建角色、容器或调用模型；`build/ai/factory-broker-image-rejection.log`
是预期拒绝记录，不是功能通过记录。相关回归与检查记录位于
`build/ai/factory-broker-{compile,regression,vet}.log`。
镜像即使通过运行合同，也仅能标记 packaged_image_verified；GitHub 发布来源与
版本仍需独立核验，不能自动标记 released_image_verified。


共享验收代码重构后，QA 挂载路径 `TestLiveContainerCodexLeveling` 于 90.20 秒再次
通过，含之前追加的“最终成功原生观测晚于最后任务查询”断言；`1|10` 采购和唯一
轮询 handle 已由主代理另行核对。此次 `broker_verified=false`，不能将此回归结果
混同于尚缺镜像的 Factory/broker 正式路径实测。


独立审查修正了新入口缺少 MaxOutputTokens 的配置错误；增加真实模型 Store 校验
测试，避免仅编译无法发现必填参数遗漏。正式路径现在在 session 仍活跃时调用
Factory.Close，并通过 wrapper 转发和计数验证底层 gameplay backend 确实关闭，
随后核验能力撤销。broker 组件证据先标记未完成，只有全部供货练级断言通过才提升
为 passed；最后执行的 cleanup 回调会把任意后置断言或清理失败写回 passed=false。
修正后的 aiservice/aibroker 两包 race 和 vet 均通过；这些仍不代替缺少镜像的实测。


### STARTMSG 已进入任务定义与实际执行引擎

补齐花与贝壳任务的 `confirm-start-message` 步骤：领取花后、去弥生前，提交
日美子 231 号窗口的 OK=1。成功条件为 `window_submitted`，而非已经存在的花或
now:2 标志；因此不会跳过这次说明窗口提交。任务整体仍保持 unverified。

AutomationGame 分别投影可操作窗口和当前窗口的本地提交状态。提交状态要求当前
已提交窗口、最新历史记录一致、玩家在线并处于世界状态，以及唯一可见 NPC 的
目录身份/地图/窗口类型/序号/object 匹配；旧窗口、更换对象、断线或重登不能冒充
当前提交证据。prepared 但缺乏提交证据的检查点仍暂停，不自动重发。通用任务完成
文案改为“已确认目标条件满足”，避免把本地写入结果表述为服务端确认。

`TestLiveGiftPickupFreshAI` 于 18.93 秒通过：使用嵌入定义的确认步骤和实际
Automation Engine 完成窗口提交并持久化 SubmittedWindows；重复提交被拒绝；
重登后花/now:2 保留，领取花的 prepared 检查点恢复仍只提交一次。
证据为 `build/ai/embedded-start-message-live.log` 及
`build/ai/gift-pickup-live-evidence.json`，新增 `start_message_task_verified=true`，
同时保留 `task_execution_verified=false`。该实测发生在通用完成文案改动之前；
文案不改变状态、提交或检查点行为。完整送花路线的生存准备仍未完成。

任务定义参与知识指纹，新版本为
`e0d097e3940743690ef8391f90c980e437bf0261203de15d386f6af607fb24be`。
三个供货/治疗/NPC 目录与对应绑定测试已更新版本，实际 NPC/itemset 数据未改；
此前实测文件仍保留原指纹，不能当作新任务定义的完整验收证据。


审查后进一步拒绝不同 alias 同时匹配同一当前 NPC 窗口的歧义（包括可操作窗口和
已提交窗口）；Start/Resume/Tick 完成文案保持一致。最新真实 pickup 复测于
17.73 秒通过，证据字段使用 `start_message_step_verified=true`，而完整任务字段
仍为 false；四包 race 及 vet 再次通过。首次 18.93 秒证据保存在
`build/ai/before-alias-review-gift-pickup-evidence.json`。
OK 无回执的源码依据为 `server/legacy/source/2.5/gmsv/npc/npc_exchangeman.c:501-515`：
_NEWEVENT 分支只响应 NEXT/PREV，非 _NEWEVENT 分支直接 break，均无 OK 响应。
任务内 SourceRef 限定为游戏 data 根目录下的证据，故上述 C 源码依据另行记录，
没有伪造一个位于 data 目录中的源码引用。

### Chat provenance and persistent social identity gap

Chat history no longer stores native `FromID` as the memory subject. That
value is a reusable protocol handle, not a durable person ID. Each headless
connection now generates a random 128-bit observation context; successful
character login rotates it and logout clears it. The context is captured when
the chat event arrives, so later login changes never relabel previous messages.
It passes through the MCP observation into confirmed chat memory. The memory
explicitly says `speaker_identity: unresolved`, preserves the observed numeric
handle, and leaves the durable subject empty. The context describes where the
observation occurred; it does not prove that repeated handles even within that
context belong to one person.

Scoped chat dedupe includes this context, so identical timestamp/handle/text
from two connections remain distinct. Legacy unscoped keys retain their old
format to avoid duplicating stored observations after upgrade. Restoring the
recorder rebuilds either form. Entropy failure leaves context unknown; it does
not substitute a process counter or account identifier.

At the supervisor prompt boundary, legacy `chat.message` rows are excluded
from durable-target recall and recent chat rows are copied with empty subject
and unresolved identity. This normalization occurs before the JSON byte-budget
check and does not rewrite source events or stored audit rows. Both custom and
default prompts explain that chat and party handles do not establish persistent
player identity or friendship. Confirmed conversation content remains usable
as historical context, with current observations taking priority.

Preserved-source review establishes why a new native identity is needed:

- `gmsv/char/char_base.c:2401–2416` implements `CHAR_setPetUniCode`, explicitly
  “Unique Pet Code”; `gmsv/char/char.c:1064–1067` calls it for pet slots during
  login. No player identity lifecycle is established by this field.
- `CHAR_SAVEINDEXNUMBER` is an account-local `.0.char` / `.1.char` slot.
  `saac/char.c:245–253` assigns the first empty slot to a new character;
  `saac/char.c:344–352` removes the deleted character's file. Thus an account
  and slot pair can refer to a later replacement character.

Persistent social recognition remains incomplete. It needs a server-generated,
immutable ID saved with the character, distinct for replacement characters,
plus chat-time/party/actor identity evidence that cannot retroactively attach
an old message to a reused object handle. Names, account slots, transient
handles and model-inferred friendship must not substitute for that evidence.
This change deliberately does not claim cross-login relationship recognition.

Validation: context lifecycle and projection tests; recorder dedupe across
contexts and Forget/restore; legacy target-recall exclusion without losing
recent conversations or modifying audit history. Race tests for aigame, aimcp,
airuntime, aiservice, aisupervisor and Web, plus vet, passed. No live model or
sustained gameplay run was performed.

### Native player identity persistence foundation

Modern patch `0022-character-identity.patch` appends a dedicated
`CHAR_PERSISTENTID` / `charid` string field. It leaves existing field indices,
pet `ucode`, account IDs, character names and save slots unchanged. The ID is
`pc1_` followed by 32 lowercase hex digits from 128 bits of operating-system
randomness, fitting the native `STRING64` field.

`StoneAge_CharacterIdentityPrepareSave` runs at
`CHAR_charSaveFromConnectAndChar`, before the ordinary SAAC save submission.
The admin mutation save path also prepares identity before capturing its save
receipt, so first-time identity generation is included in that receipt.
The shared serializer is intentionally read-only: administrator snapshot and
revision calls also use it. New player characters and old characters missing
the field receive an ID on their next ordinary save; valid existing IDs are
preserved across retries and reloads. New default character data clears the
field, so same-name recreation in a reused account slot generates a different
ID. Pets and unused character entries cannot receive a player ID.

Random-source failure leaves the field empty and allows the ordinary gameplay
save to proceed; identity-dependent features must treat it as unknown. A
nonempty malformed value is preserved for diagnosis and fails validation;
this helper never silently rerolls or repairs an existing identity. Partial
reads and interrupted reads are handled before publishing the complete value
to character memory.

This foundation alone is not completed social recognition. Publication and
chat attribution are described in the following sections. A generated in-memory ID
is not proof of a successful SAAC save. Publishing requires either loading an
already persisted valid value or tracking successful save acknowledgement.
A crash before that first save can discard the pending ID. The next work is
that lifecycle evidence, then chat-time attribution and durable social recall.
Legacy auxiliary backup writers serialize existing IDs but do not backfill
missing IDs. Administrative archive copying/restoration also needs explicit
identity semantics before external identity consumers are enabled.

Offline validation (`server/legacy/modern/tests/test-character-identity.py`):
patch applies without fuzz, production module passes syntax checking against
patched native headers, and an executable checks enum/string-table alignment
under the preserved build macros. A warnings-as-errors C harness uses the
native serializer and loader string-field loops to verify old archives,
roundtrip retention, read-only serialization, same-name replacement, missing
and malformed IDs, pets/unused entries, random-read failure, partial reads and
EINTR. The script writes temporary files only under `build/ai/character-identity`.
`build.sh` shell syntax and whitespace checks passed. This does not establish
full binary/image build, actual SAAC save acknowledgement or live gameplay
acceptance; none was run in this feature-first phase.

### Publishing identity only after an authoritative archive load

Patch `0023-loaded-character-identity.patch` adds a non-persisted work string
`CHAR_WORKPERSISTENTID_LOADED`. `CHAR_login`, after constructing the online
character from the SAAC response, records the valid loaded `charid` there.
The generic parser and serializer do not establish this provenance, since
administrator inspection/rollback can use them for in-memory copies.

The read-only identity accessor requires an active player, a valid stored ID,
and an exact match to that loaded work string. A newly generated pending ID,
missing/invalid ID, different ID written in memory, pet or unused record returns
unknown. The native character-scoped `S("AI")` response optionally includes
`character_id` only through this accessor. It does not scan other players or
read account credentials, and the observation path never generates an ID.

The Go parser accepts only the exact `pc1_` + 32 lowercase hexadecimal format;
empty, malformed or duplicate fields reject the response as a whole. A valid
response omitting the optional field clears previous identity evidence, retaining
compatibility with older servers. Character login/logout and observed outgoing
character-login selection clear the previous identity. MCP exposes it as
`persistent_character_id` only for a connected, ready, authoritatively observed
character. The existing account/slot `character_id` execution binding remains
separate; changing it would invalidate current task leases and checkpoints.

Source review of `gmsv/callfromac.c:118–162` confirms the current save ACK is
routed by connection `fdid` and connection state, without a returned immutable
character ID or an instance-bound save receipt. It is not promoted into identity
confirmation. Newly backfilled old characters therefore gain public identity
after a successful ordinary save and subsequent archive reload; the runtime
does not force a logout. Same-session promotion would require explicit pending
save metadata and matching acknowledgement support.

This completes the own-character observation path. Chat-time attribution is
described below; party and visible-actor identity evidence, administrative
restoration semantics and persistent relationships remain incomplete. No
historical chat handle is retroactively joined to this new field.

Validation: native lifecycle/accessor tests cover pending IDs, loaded IDs,
in-memory replacements and reused default characters; the native observation
harness covers optional identity emission while retaining its read-only scope
checks. Go tests cover wire format, optional/duplicate fields, lifecycle clearing
and separation from execution binding. Five affected packages passed race tests
and vet; shell syntax and whitespace checks passed. No image build or live model
or gameplay test was performed.


### Chat-time speaker attribution and bounded conversation recall

A native `S("AICHAT|1|objectID|color|characterID|hexMessage")` companion
immediately precedes supported player `TK` chat. The ID comes only from the
loaded-identity accessor; NPC/system/pending identities remain unattributed.
The companion includes the exact TK message bytes before protocol encoding,
including CP936 text. Ordinary TK rendering is preserved.

The Go receiver consumes metadata once, on the immediately following server
packet. Only a TK with matching object index, color and exact message bytes
receives `speaker_character_id`. Intervening packets, mismatches and lifecycle
changes discard it. No reusable object-index-to-person table is retained and
old messages are never retroactively attributed.

The recorder stores valid chat-time attribution as `server_chat_v1`, with the
persistent speaker ID as Subject. Session context still scopes event dedupe,
including recorder restarts. Missing or invalid attribution remains unresolved.
Prompt normalization strips legacy or inconsistent speaker claims without
changing stored audit rows. Both default and custom prompts treat chat text as
quoted reference data, not instructions or evidence of friendship.

A ready observation may recall up to two distinct attributed speakers from
its last five minutes of chat, in addition to the existing task/character
subjects. Each subject contributes at most four historical records; the overall
32-record / 16-KiB prompt budget and half-budget ceiling for historical recall
remain in force. This supports remembering conversations, not claiming a
relationship, identifying party members or completing social-behavior acceptance.

Native patch `0024-chat-identity.patch` wraps `CHAR_talkToCli` and
`CHAR_playerTalkedfunc` using their actual sender character index. The wrapper
checks that sender's current object index and loaded identity, then sends the
companion and the original TK exactly once. Messages longer than 2047 bytes
retain TK behavior without attribution. The browser ignores the companion
before UI logging, transition deferral or rendering; the server-side observer
still processes the original stream.

Offline validation: `test-chat-identity.py` applies the patch without fuzz,
checks both call sites, syntax-checks the production module against native
headers, and runs a warnings-as-errors C harness covering send order, exact
CP936/escaped bytes, size boundaries, unknown identity and unchanged TK
fallbacks. Go tests cover intervening packets, login/logout, changed sender,
color/text mismatch, recorder restart dedupe and bounded default/custom prompt
recall. Five affected packages passed race tests and vet. Browser companion
isolation and the existing protocol regression suite passed. Full image build,
live protocol/gameplay acceptance and model behavior remain untested in this
feature-first stage.


### Human automation recovery across Web process restart

`Checkpoint.owner_context` stores a versioned, server-owned Web recovery
binding: account, slot, character name, configured game line, loaded persistent
character ID, original configuration and last activity time. The Web plan-store
adapter writes it atomically with Create and preserves it through Save; browsers
and models cannot supply the binding. Generic deterministic execution treats it
as opaque metadata, with no Codex/model dependency.

Before accepting sessions, the Web runtime acquires an exclusive OS lock on a
persistent `.web-recovery.lock` file alongside the canonical database path.
The sidecar is retained after release; it is separate from SQLite's own locks.
Concurrent Web runtimes sharing this database fail startup instead of both
claiming an interrupted run. The reviewed lock implementation supports Linux
and macOS; other platforms reject file-backed automation recovery. In-memory
stores have no cross-process recovery.

Startup considers only valid Web-owned records, pauses interrupted running
checkpoints with revision checks and retains prepared/submitted phases. It
builds no gameplay backend and sends no game packet. A newly authenticated
player must explicitly resume or discard the offered task. Resume rebuilds the
controllers, checks the identity again, and continues the original plan and
budget using existing receipt reconciliation. Unknown delivery is not replayed.
A same-name replacement in the same slot has a different persistent identity
and cannot claim the former character's task. If ordinary player status arrives
before the persistent identity, the matching slot remains blocked from starting
a replacement task until identity confirmation arrives.

The newest durable run for each complete identity supersedes older runs,
including when the newest is completed or cancelled. Restart does not renew
the recovery window: the 24-hour limit uses the last real execution write,
not the startup pause timestamp. Expired Web-owned running rows are paused
without being offered. Legacy/other-owner checkpoints are not automatically
reassigned, cancelled or paused. Old servers and characters without a loaded
persistent ID retain only the existing process-local recovery behavior.

Offline tests cover actual SQLite close/reopen, startup reconciliation, original
plan resumption, unknown delivery without socket writes, explicit cancellation,
replacement identities, expiry across repeated startup and terminal-history
suppression. Runtime and lock tests cover real SQLite coexistence and exclusive
ownership. This establishes component behavior; live deployment/crash and
sustained gameplay acceptance remain pending the consolidated testing stage.

Verification for this increment: automation, leveling, planner, service and Web
packages passed `go test -race` and `go vet`; the final identity-arrival guard
also passed the full Web race suite. Changes were not deployed and no live
leveling, task execution or model calls were made.


### Creating AI players with pet levels and riding

New admin creations enable initial riding by default, with an explicit opt-out.
Custom mode preserves each pet's requested level; random mode preserves the
configured pet-level range. The first generated pet is the initial mount.
Random candidate validation checks every template at the maximum pet level and
minimum character level before reserving an account, so the result cannot depend
on a lucky draw. Incompatible templates/ranges are rejected instead of changing
the requested pet or its level. The random form starts with character level
10–60 and pet level 5–10.

Mounted birth mode asks the running native server for a compatible level-one
pet from its effective catalog, then uses the same initialization journal and
publication checks as random/custom mode. This changes the birth-mode pet
selection when riding is enabled. Explicitly disabling riding retains ordinary
server birth behavior. Old stored requests and resolved plans without the new
mount field retain their original behavior.

The native initialization protocol retains v1 for walking plans and uses v2 for
mounted plans. Riding requires a real compatible graphic, sufficient permission,
loyalty and the server's level-difference rule. The mounted pet is not also the
default battle pet. Initial compatibility currently follows the ordinary native
`ridePetTable` for the stock character appearance; it does not grant family
membership or special riding certificates for other combinations. The initial
saved snapshot exposes read-only `ride_pet_slot`
and `learn_ride`; publication/recovery verifies the first slot is mounted. These
fields are not added to the general character attribute editor. Default riding
is a creation setting, not an instruction to remount continuously during play.

Shared auto-leveling retains the mounted role: native battle settlement awards
riding-pet experience separately, so a mount need not be selected as the battle
pet. Before leveling movement, the coordinator requires a confirmed riding slot
(including explicit walking), checks mounted-pet health, and heals a living pet
below half health through the reviewed item adapter. Missing, dead or unconfirmed
mount state pauses the task. Recovery binds pet identity and its original role,
requires observed item consumption plus HP gain, and does not retry an uncertain
item use. Human Web sessions and AI sessions use the same state parser and
coordinator for this behavior.

Shared deterministic quest travel also checks the selected riding and battle
pets before entering an encounter tile, including warp arrivals and pre-write
revision retries. It consumes reviewed healing items one at a time for a living
pet below half health, then replans from fresh observations. Unknown riding
status, missing/dead pets and changed identities stop travel; town-only movement
remains available. The MCP observation exposes explicit riding/battle selections
without treating slot zero as a default. Transient K-packet identity invalidation
may be enriched only for the same local pet incarnation; that internal continuity
is never exported as a persistent model-facing identity. Uncertain item use
returns immediately without another recovery attempt. This is shared human/AI
behavior and does not make the unreviewed task catalog executable.

Validation for this change uses Go unit/race tests, admin JavaScript tests and
native source harnesses. Live server creation, relogin and client rendering remain
part of consolidated acceptance; no production deployment is implied.

### Recovering interrupted AI-player creation with an initialization journal

Account creation now commits the account, audit entry and initialization-journal
account link in one auth-database transaction. Failure to save the link rolls
back the account creation, removing the previous untracked-account crash gap.
The resolved random plan remains reserved before this transaction.

Before native initialization, the journal persists the immutable account/slot
binding and complete non-secret profile draft. A confirmed `Actual` snapshot
is recorded only after the initialization service has verified the native save.
If profile publication fails after that point, the record becomes
`publication_pending`, the account is disabled and its private credential is
retained for explicit recovery. Unconfirmed mutations remain
`failed_or_unconfirmed` and are never retried. The provider rejects new-format
journal entries until publication is complete, so a profile row committed in
the other database cannot become runnable early. Prior verified `applied`
records without drafts remain compatible.

Creation, recovery and startup reconciliation share a nonblocking process lock
in the private game-secret directory. The persistent lock file is not deleted
on release. Startup disables accounts linked to interrupted initializations;
it does not create characters, reroll choices or submit native initialization.
Creation and recovery deployments must share the same configured secret root.
The reviewed lock implementation supports Linux and macOS.

The admin console lists incomplete creations, including rows that never reached
profile publication. Only `applied` / `publication_pending` records with both a
confirmed snapshot and complete draft can request recovery. Recovery validates
the journal/account/binding and private credential, reads and verifies the
current **offline archive**, then idempotently completes the binding/profile
publication. It does not log in, call `Apply`, modify levels or issue pets.
Recovered profiles are stopped and require a separate start action. Repeating
recovery after publication is read-only and does not re-enable an account that
an administrator subsequently disabled.

Records without confirmed mutation evidence stay visible for investigation;
matching live character statistics alone do not authorize replay or publication.
This does not automatically reconstruct a lost mutation receipt. Birth-mode
provisioning without an initialization journal remains outside this recovery
flow. No game credentials or native failure details appear in the recovery API.

Tests exercise publication failure in SQLite, reconstruction from persisted
journals, both missing and partially published profile rows, provider gating,
no repeated initializer/login calls, mismatched or online verification rejection,
read-only completed retries, startup quarantine, atomic account-link rollback,
and read-only offline archive verification. Admin tests cover authorization,
CSRF, audit actor, safe response fields and sanitized failures. Live crash,
container and gameplay acceptance remain pending consolidated validation.

Validation for this increment: auth, provisioning, player manager, admin and
admin-command packages passed race tests and vet. The final recovery/provider
checks and admin endpoints passed focused race reruns; four admin JavaScript
regression suites passed, including recovery list/DOM safety and existing
creation/unknown-operation flows. No production action, model call, container
build or live initialization was performed.


### Visible-player and teammate recognition

The native/Go integration adds `AIPERSON|1` companions for exact visible C
records and party N status rows. Only loaded server character identities are
eligible. Companion metadata is consumed by the immediately following packet;
C may have a bounded batch of companions for its individual records. Attribution
requires the same object index and exact original record bytes. Invalid,
duplicate, conflicting, oversized or interrupted metadata cannot identify a
person. Ordinary native messages retain their gameplay purpose. Full N rows
now decode all native fields rather than treating the full-status marker as an
empty update mask.

Visible C replacement, CD removal, map change and character lifecycle discard
old actor evidence. An unpaired N update clears its previous attribution. CA
movement preserves the identity of the same currently known actor, and no
actor/party mapping is used to retroactively identify old chat. MCP exposes
`persistent_character_id` separately from transient action handles.

The observation recorder stores first confirmed sighting and first confirmed
shared-party facts separately, with server identity provenance and restart-safe
deduplication. These facts establish neither friendship nor trust. Current
teammates/visible players can select bounded historical recall even without a
new chat message; legacy unattributed chat remains excluded from person recall.
Current authoritative observations override historical names and locations.
Administrative restoration semantics and live cross-login social acceptance
remain outstanding; this component work does not complete the full AI goal.


Validation for person recognition: related Go packages passed race tests and
vet (`build/ai/person-identity-{race,final-race,vet}.log`). The main agent
independently compiled the production C helper, exercised distinct character
and object indexes, full/partial N rows, mismatched membership and invalid
objects, then fed its actual output through the Go parser
(`build/ai/person-identity-independent-{native,cross-language}.log`). Patch
application with zero fuzz and syntax checks of the patched generated sender
also passed. These are offline component and cross-language checks; they do
not claim a live shared-party session, actual model-driven conversation or
complete relationships acceptance.

### Human-player automatic leveling supplies

The Web leveling panel now exposes optional automatic resupply without an AI
player or model connection. Select a reviewed item, a target count of 2–13,
and a reorder threshold from 1 to one below that target. The directory shows
the item's display name and unit price; the panel shows the conservative
single-refill authorization (`target_count × unit_price`). Actual purchases
use the shortfall and preserve two backpack slots. The existing spending cap
and gold reserve remain enforced throughout the run.

Preflight checks the selected contract, observed inventory, available slots,
and (when a refill is already needed) funds and spending authorization without
sending game packets. Unknown inventory or stale offers block preparation.
Supply settings are persisted with the leveling plan and copied into recovery
configuration. Quest mode does not accept these settings: quests such as the
adult ceremony need their own inventory preparation. A reviewed shop contract
does not certify a safe route to that shop from every location.

Validation: Go race tests passed for `internal/aiservice`,
`internal/aileveling`, and `client/web`
(`build/ai/human-supply-final-race.log`); related vet and Web Node tests passed.
This is component validation, not sustained gameplay or browser acceptance.

Social recall prioritizes recent server-attributed conversation partners over
passively visible people within the existing four-subject lookup budget.
The AI's own messages do not consume the two-speaker allowance. Target and
character history retain their priority. A crowded-scene regression buries
an earlier conversation beneath unrelated observations, then verifies both
default and custom prompts recall that speaker despite two other teammates
and a later self-message. Supervisor race tests and vet passed
(`build/ai/social-recall-race.log`). This validates recall selection, not live
conversation or administrative character-restoration identity semantics.
An independent Go overlay restored the previous subject ordering without
changing working sources; the same crowded-scene test then failed because
the active speaker's old conversation was absent
(`build/ai/social-recall-old-order.log`). This confirms the regression detects
the original behavior rather than merely exercising the new branch.

Administrative identity audit: the current Web player routes expose reads and
attribute/item/pet edits, not a character archive restore/clone API. Offline
edits serialize the parsed archive and CAS-write it back. AI initialization
publication recovery is a separate existing operation: `RecoverInitial`
checks account, slot, character name, initial state and immutable profile
binding, whose character ID remains `account:slot` for account routing. The
following implementation adds native person identity evidence separately; it
does not reinterpret the routing ID or add a character clone/restore endpoint.

### Persistent identity during initialization publication recovery

Management snapshots read `persistent_character_id` only from a parsed SAAC
archive with a valid native `charid` (`pc1_` followed by 32 lowercase hex
characters). Missing or malformed fields stay unknown and are never repaired
by a read. Online record projections do not manufacture persistence evidence.

Initialization now obtains the exact archive whose serialized character hash
matches the native save receipt. It validates initial state and character name
against that archive and retains its persistent identity in `InitialState.Actual`
before profile publication. It does not replace that evidence with a subsequent
online snapshot. A missing/invalid identity leaves the creation quarantined;
it never repeats the native initialization.

Interrupted publication recovery requires a valid identity in the original
journal and the same identity in the currently verified offline archive. A
same-name, same-slot replacement with matching levels/pets but a different ID
is rejected before profile/account publication. Legacy interrupted journals
without an identity are shown as nonrecoverable; recovery does not adopt the
current archive's ID as their missing historical evidence. Already published
records keep their prior compatibility and idempotent, read-only retry behavior.
This does not yet establish runtime identity continuity for arbitrary external
archive copies, nor does it prevent duplicate IDs introduced by such copies.

The five affected Go packages (`playerdata`, `playermanager`, `aiprovision`,
`admin`, and `cmd/stoneage-admin`) passed race tests and vet; admin initial-state
and recovery Node suites passed. Evidence is in
`build/ai/initial-identity-{race,vet,ui-tests}.log`. Tests cover archive-only ID
projection and byte retention, initialization's confirmed-save source,
missing/malformed identities, replacement characters with otherwise matching
recovery fixtures, unchanged quarantine/journal on rejection, restart recovery,
and both missing-profile and half-published-profile recovery. This is component
validation, not a live create/restart/recover acceptance run.

The native admin save path prepares the same identity before serializing its
save receipt; ordinary save preparation then preserves that value. This avoids
hashing an identity-free character and subsequently submitting a character
with a newly generated ID. Preflight/inspection remain read-only, and existing
malformed IDs retain ordinary save behavior rather than being silently repaired.
The main agent independently ran the new production-function receipt harness,
the character identity harness, admin guards, and initialization/rollback harness;
all passed (`build/ai/initial-identity-native-{receipt,character,guards,initialize}.log`).
The new receipt test is included in the release workflow. No live server,
container image build, or deployment was performed for this change.

### Human-readable departure preparation

Shared quest preflight now explains failed backpack space, solo-party state,
health, battle, and explicit gold requirements alongside character/pet levels.
For example, an adult-ceremony-style 15-slot requirement reports the current
free slots and asks the player to clear space; missing inventory or party
observations are reported as unsynchronized rather than as an empty backpack
or solo state. Duplicate preparation messages are collapsed. These messages
use the same failed conditions and observation as the existing execution gates;
they do not auto-discard items, leave a party, heal, or otherwise change gameplay.
Other opaque progress/item conditions retain the generic precondition failure.

The Web preview uses this shared evaluator. Its integration regression verifies
that the backpack diagnostic reaches the response while observation revision,
control generation, checkpoint store, and game socket remain unchanged.
Automation, planner, and Web packages passed race tests and vet
(`build/ai/preparation-diagnostics-{race,vet}.log`). No task's review or execution
verification flags were changed by this work.

The companion task audit found no missing level gate in the existing shared
entry paths. Current embedded definitions remain `unverified` with
`execution_verified=false`: adult ceremony requires character level 35,
solo state, and 15 free backpack slots; hometown-0 gift exchange requires
level 1 and its savepoint. Basic riding has no invented level prerequisite.
Their remaining review/execution acceptance must be completed before enabling
them in the normal task directory; improved preparation diagnostics do not
constitute that acceptance.

### Read-only departure route preflight

Shared quest preflight now optionally invokes `DepartureValidator` after its
existing prerequisites/contracts pass. `AutomationGame` dispatches this to the
installed movement skill for the first action of the first unfinished stage.
Completed prerequisites are skipped using the same stage-closure rules as
execution. Already-completed plans do not plan a new departure.

For a movement departure, the validator uses the supplied current location and
character level, a private copy of the movement configuration, and the same
encounter-aware navigator and zero-cost warp search as execution. Missing
routes, unknown positive encounter data, and unavoidable over-level encounters
make preflight not ready; safe detours remain usable. It does not call the
game observation refresher, write packets, move the character, or change the
shared navigator. Both human Web previews and model-started quests use the
same hook. Execution retains its fresh-state checks before each movement.

This validates only the initial movement action of the selected stage. It does
not simulate later NPC outcomes, prove a whole task's preparation, provide
paid NPC transit, or promote unverified riding/adult-ceremony contracts. The
Web unreachable-map regression also verifies unchanged control generation,
observation revision, plan store, and socket traffic.

Validation: race tests passed for automation, planner, service, and Web packages;
vet passed for the same packages. Evidence:
`build/ai/departure-preflight-{race,vet}.log`. Focused tests exercise unreachable
routes before checkpoint creation, current-level filtering, unknown encounter
data, a safe detour, cross-map reachability, cancellation, and stage selection.
These are offline tests and do not establish riding-course live acceptance.

The riding-course source review found no menu/fee/level prerequisite mapping
change to make: the 1040 trainer uses 101 → 200 → 210, course choices 4 → 1
and YES(4), a 5000 fee, and authoritative `learn_ride=40`. The preserved native
purchase branch checks already-learned state and funds; family revenue ownership
is not player eligibility. The modern funding patch covers this purchase path.
The draft still needs effective-build applicability, NPC/window identity,
authoritative skill and spending receipts, and route/preparation acceptance
before promotion. No reviewed/verified flag was changed.
