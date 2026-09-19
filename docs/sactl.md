# sactl：命令行游戏客户端

`sactl` 是一个无头客户端：它自己登录网关、进入世界、观察和操作角色，全程不需要浏览器。
它给两种使用者用——人在终端里手工调试，以及 Codex 这类终端 agent 把游戏当成一组 shell
命令来驱动。

## 为什么是命令行而不是工具调用

给模型的旧接口是几个不透明的把手（`game_start_leveling` / `game_observe` / 一个通用动作
工具），模型看不到世界、不能试、不能改，真正的决策被塞在确定性引擎里；每次调用还要穿过
MCP → admin → unix socket → web 四跳。模型因此变成了旁观者。

`sactl` 把决策权还给模型：每条命令都是原子的、立即返回真实结果，模型可以"看一眼 → 做一步
→ 再核对"。Codex 本来就是终端 agent，敲命令是它最擅长的工作方式。

## 结构

```text
sactl serve --config <file>     长驻进程：持有游戏会话，监听私有 Unix socket（0600）
sactl <命令> [参数]              一次性客户端：连 socket → 发一条请求 → 打印结果 → 退出
```

登录、选角、进入世界是有状态的长连接，不能每条命令重连，所以有守护进程这一层。一次性命令
通过 `runtime/sactl/sactl.sock` 找到它。

退出码：`0` 成功、`1` 动作失败或结果未知、`2` 用法错误、`3` 守护进程未运行。模型可以据此
区分"游戏拒绝了这一步"和"客户端没配好"。

## 安装

**0. 包管理器（推荐，macOS / Windows）**

```bash
# macOS
brew install k0ngk0ng/tap/sactl

# Windows（PowerShell）
scoop bucket add k0ngk0ng https://github.com/k0ngk0ng/scoop-bucket
scoop install k0ngk0ng/sactl
```

两个渠道都由 Release workflow 里的 `packages` 作业自动更新：它按发布出来的 `SHA256SUMS`
渲染公式与清单，推送到 `k0ngk0ng/homebrew-tap`（brew 里叫 `k0ngk0ng/tap`）和
`k0ngk0ng/scoop-bucket`。该作业需要一个仓库 secret `TAP_GITHUB_TOKEN`（对两个仓库有
`contents: write` 的 PAT）；未配置时它只打警告并跳过，不影响发版。

用包管理器安装还顺带绕开了 macOS 的隔离问题：brew 与 scoop 都用 curl 下载，不会写入
`com.apple.quarantine`。

另外三种方式，任选一种：

**1. 从 Release 下载（推荐给使用方）**

每个版本都会发布独立的客户端包 `stoneage-sactl-<版本>-<系统>-<架构>.(tar.gz|zip)`，
覆盖 `darwin/arm64`、`darwin/amd64`、`linux/amd64`、`windows/amd64`，包内含二进制、
`sactl.toml.example`、`sactl.md` 和安装脚本：

```bash
tar -xzf stoneage-sactl-v0.1.48-darwin-arm64.tar.gz
cd stoneage-sactl-v0.1.48-darwin-arm64
install -m 755 sactl ~/.local/bin/sactl
xattr -d com.apple.quarantine ~/.local/bin/sactl 2>/dev/null   # 见下方 macOS 说明
mkdir -p ~/.config/sactl && cp sactl.toml.example ~/.config/sactl/sactl.toml
chmod 600 ~/.config/sactl/sactl.toml    # 填账号、密码、map_directory
```

> **macOS：从浏览器下载的副本会被系统杀掉。** 浏览器会给文件打上
> `com.apple.quarantine`，而 macOS 只允许**带苹果开发者签名**或被信任的隔离文件运行；
> 本项目没有开发者签名（临时签名不够），于是运行时报的就是 `zsh: killed ./sactl`
> （退出码 137），没有任何提示。三种解决办法，任选其一：
>
> ```bash
> xattr -d com.apple.quarantine ./sactl     # 手动装完后清掉属性
> scripts/install-sactl.sh                  # 包内脚本会自动清掉
> scripts/install-sactl.sh --download        # 用 curl 下载，根本不会带该属性
> ```
>
> 用 `curl`/`gh` 下载不受影响，因为只有浏览器一类应用会写这个属性；用上面的 Homebrew
> 安装同样不受影响。

> **Windows：** 浏览器下载的 zip 会被打上 Mark-of-the-Web，首次运行会有 SmartScreen
> 提示。解压后执行 `Unblock-File .\sactl.exe`（或直接用 zip 内的
> `install-sactl.ps1`，它会自动解除并安装到 `%LOCALAPPDATA%\Programs\sactl`）：

> ```powershell
> powershell -ExecutionPolicy Bypass -File install-sactl.ps1 -AddToPath
> ```

**2. 用安装脚本（一次到位）**

```bash
scripts/install-sactl.sh                        # 从源码构建并安装
scripts/install-sactl.sh --download             # 下载最新正式版（curl，不带隔离属性）
scripts/install-sactl.sh --download v0.1.48     # 指定版本
scripts/install-sactl.sh --prefix /usr/local/bin --force
```

**3. 手工构建**

```bash
go build -mod=mod -o build/local/sactl ./cmd/sactl
```

安装后的文件位置：

| 用途 | 路径 |
| --- | --- |
| 二进制 | `${PREFIX:-~/.local/bin}/sactl` |
| 配置 | `$XDG_CONFIG_HOME/sactl/sactl.toml`（默认 `~/.config/sactl/sactl.toml`） |
| socket 与状态 | `$XDG_STATE_HOME/sactl/`（默认 `~/.local/state/sactl/`） |

配置查找顺序：`--config` 指定的文件 → `$STONEAGE_SACTL_CONFIG` → `./sactl.toml` →
`./runtime/sactl.toml`（仓库内开发用）→ `~/.config/sactl/sactl.toml`。socket 路径也可用
`--socket` 或 `STONEAGE_SACTL_SOCKET` 覆盖。

**`map_directory` 必须指向一份 2.5 服务端数据目录的副本**（含 `map/`）。`goto`、`warp`、
`exits`、`encounters` 都在本地读这份数据；只做观察和对话可以不配它。

## 开始使用

```bash
sactl serve                                            # 前台持有会话；或 nohup 到后台
sactl status                                           # 另开一个终端
```

另开一个终端：

```bash
./build/local/sactl status            # 会话、连接与角色状态
./build/local/sactl chars             # 账号下的角色
./build/local/sactl create-character SactlHero
./build/local/sactl enter SactlHero
./build/local/sactl observe           # 世界快照：坐标、附近实体、背包、宠物、聊天
./build/local/sactl walk up
./build/local/sactl goto 18 22        # 用地图数据寻路，逐步核对坐标
./build/local/sactl say "你好"
./build/local/sactl log 20            # 最近的服务端事件
./build/local/sactl wait 30s          # 阻塞等待下一个事件
./build/local/sactl stop
```

`sactl` 使用专用账号，不要和人类玩家或 AI 玩家共用同一个角色：一个角色只能有一条连接。

## 命令

| 命令 | 说明 |
| --- | --- |
| `status` / `observe [--json]` | 守护进程状态 / 完整世界快照 |
| `chars` / `create-character <名字>` / `delete-character <名字> <名字>` / `enter <角色名\|槽位>` | 角色生命周期；`enter` 会被记住，重连后自动回到世界 |
| `walk <方向>` | 走一步（`up`/`down`/`left`/`right`，或 `n/ne/e/se/s/sw/w/nw`） |
| `goto <x> <y>` | 同层寻路并逐步核对坐标 |
| `exits` | 列出当前层的传送出口 |
| `warp [层 [x y]] [--time M\|A\|N]` | 踩上传送点传送，或跨图旅行到目标（多跳自动规划） |
| `encounters` | 列出当前层的遭遇区（哪里走动会触发战斗） |
| `seek-encounter` | 走进最近的遭遇区并尝试触发战斗 |
| `look <方向>` | 只转向不移动 |
| `say <文本>` | 公屏发言（默认 color 0 / range 3） |
| `talk <名字> [文本]` / `choose <行号>` / `reply <ok\|cancel\|…> [文本]` | NPC 对话：发起、选选项、答消息窗口 |
| `battle <指令>` / `battle-end` / `battle-help <0\|1>` | 战斗回合（`H\|FF` 攻击、`W\|FF\|FF` 宠物、`T\|FF` 防御、`S\|01\|FF` 技能、`E` 逃跑、`N` 等待、`G` 放弃、`HELP`）；一个回合需要玩家和宠物各自提交一次，动画结束后用 `battle-end`（EO）确认 |
| `item use\|drop\|drop-gold\|move\|magic\|pickup …` | 背包与地面物品 |
| `mail list\|add\|send\|remove-contact …` | 名片簿与邮件 |
| `pet status\|standby\|battle\|rename\|drop …` | 宠物 |
| `party invite\|leave` / `duel` | 组队与决斗 |
| `trade request\|offer-item\|offer-gold\|offer-pet\|lock\|confirm\|cancel` | 玩家交易全流程（服务端会再次校验对手朝向、报价与余额） |
| `logout` | 离开世界并关闭会话；守护进程在下一条命令时重连并重新进入 |
| `alloc <0-3>` / `social <设置> <0\|1>` | 加点 / 社交开关（party、duel、party-chat、trade-card、trade） |
| `ride <槽位>\|off` / `title equip\|text` | 骑乘 / 称号 |
| `click <对象id>` / `probe <procget\|playernumget\|echo>` | 点击地图对象 / 空闲探测包 |
| `query <c\|i\|w\|j\|n\|t\|g\|AI\|k0..k9>` | 请求一条状态流 |
| `send <FUNC> [参数…]` / `functions` | 兜底通道：按协议字段表校验后发送（整数写 `#N`） |
| `log [条数]` / `wait [时长]` | 事件流 / 阻塞等待 |
| `stop` | 停止守护进程 |

全局参数：`--socket <path>`、`--config <file>`、`--timeout <dur>`、`--json`。

能力对齐：浏览器的 37 个 `send()` 函数已有对应命令或走 `send` 兜底。唯一仍需注意的差异是
`send` 只校验字段个数与类型、不校验取值，所以位置相关的保护（如 `talk` 的相邻判定）只在
专用命令里生效。

## 两个必须知道的 2.5 协议事实

1. **服务端不会把你自己的走路结果回传给你。** 原版客户端是自己模拟行走，再用状态请求
   （`S:c`）向服务端确认坐标。所以 `walk`/`goto` 在提交 `W` 之后会持续用只读的 `S:c` 询问
   位置，直到坐标吻合或超时；超时按"未到达"报告，不会重发 `W`。只等事件会永远看到旧坐标
   ——这一点和直觉相反，也是最初走路"看起来没生效"的原因。
2. **方向字母有大小写语义，且不是从 `a` 开始的顺序。** 小写 `a`..`h` 是行走，大写是转身；
   字母表由 `cnvServDir()` 的旋转决定（`client/web/index.html:1669-1676`）。`sactl` 只接受
   方向名字，把字母留在内部，避免"`e` 到底是东还是下"这种歧义。
3. **观测里不会列出你自己的角色。** 更新自己坐标和它（`player_id`）分开显示，因为服务端
   从不回报你自己的行走，本地那条 actor 记录会一直停在旧坐标；两者同时出现会让读者看到
   一个角色两个位置。
4. **一回合战斗需要两次提交，且顺序固定。** 先玩家后宠物：宠物指令在玩家指令之前提交会被服务端拒绝（2026-09-18 实测遇到的顺序拒绝）。只提交玩家指令时回合不会推进（观察里 `player_submitted=true pet_submitted=false`）。结算动画期间 `command_ready=false`，动画结束后由客户端发 `EO`（本工具的 `battle-end`）推进到下一回合或离开战斗。
5. **选择窗口的 `select` 字段是 0，选项号在 `data` 里。** 原版客户端点击选择行时发送
   `select=0, data="<行号>"`，服务端的 NPC 处理器再用 `atoi(data)` 取按钮
   （`client/web/index.html:15015-15033`）。行号是**从第一个可选行起算、包含空行**的，
   所以窗口里显示的第一个选项可能叫 2 而不是 1；`choose` 直接用显示出来的号码。
   `internal/aigame` 原本要求 `select != 0`，会挡掉所有选项提交，已就地放宽。

## 与 AI 玩家链路的关系

`sactl` 直连网关使用命名协议，复用 `internal/aigame` 的会话、状态投影和动作校验，以及
`internal/ainavigation` 的地图寻路。它**不经过** `internal/aimcp`、管理端或 Web 会话租约。

现有 AI 玩家链路（Codex → MCP → admin → Web 租约）保持不变；把 AI 玩家迁到 sactl 上是后续
独立决策，不在本工具范围内。

## 交给模型驱动

`ai/skills/stoneage-cli/SKILL.md` 是给 Codex 的技能说明；`scripts/sactl-agent-smoke.sh`
跑一个真实 Codex 回合来测量"模型能不能自己玩"。脚本用 `runtime/codex-sactl` 下的隔离
`CODEX_HOME`（复制配置、不改操作者的 `~/.codex/config.toml`），密钥只来自复制过去的
`auth.json`，不经过命令行。

```bash
./build/local/sactl serve --config runtime/sactl.toml &
./scripts/sactl-agent-smoke.sh            # 用默认任务
./scripts/sactl-agent-smoke.sh task.txt   # 用自定义任务
```

2026-09-18 的实测（本地全栈、真实 Codex 0.154.0）：模型自己读技能、观察、定目标、用 `goto`
走访两个 NPC、每步 `status` 核对坐标，最后如实报告——包括明确指出"sactl 还没有 NPC 对话
命令，所以这轮只是走访，不把靠近当成交谈"。同一轮还暴露并促成修复了三个真问题：观测里
自己角色的坐标是陈旧的（服务端不回报自己的行走）、没有 `--help`、地图只有编号没有名字。

## 已知边界

- **`logout` 曾读不到应答，已修。** 根因不是 Ringo 解码，而是 `internal/aigame` 的并发缺陷：进入世界后后台读协程已拥有 socket，`request()` 又自己去读，两个 reader 交错导致封包框损坏（表现为 "truncated Ringo bit stream" / "invalid base64 length"）。现在进入世界后 `request()` 改为等后台读协程投递对应事件（`internal/aigame/protocol.go` 的 `awaitReply`/`deliverReply`），并有 `-race` 回归测试 `TestRequestAfterEnterWaitsForTheBackgroundReader` 钉住。

- 动作面目前覆盖观察、移动、寻路、聊天、角色生命周期；**NPC 对话、战斗、物品、商店、邮件、
  宠物、组队、交易仍待接入**（`internal/aigame` 已有对应动作种类，`internal/aiservice` 已有
  语义层）。模型实测中最先要的就是对话。
- `goto` 只在同一层内寻路；跨图传送（EV/warp）尚未接入。
- 地图数据来自 `map_directory`，与 GMSV 实际使用的版本必须一致。
