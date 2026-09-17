# Agent 与 Web 游戏会话

人类浏览器与独立 AI 玩家共用 Web 游戏入口：

```text
浏览器 / AI 登录连接器 → Web /api/sessions → 游戏协议 gateway → GMSV
Agent 游戏工具 → Web /v1/game → 内部工具运行时 → Web 内部会话租约 → 同一游戏连接
本地 AI worker → Web /api/ai/worker/* → 内部运行时调度
```

管理端保存模型配置、AI 玩家档案、记忆、日程和执行状态。它通过 Web 登录连接器创建游戏会话，不再直接建立游戏协议 TCP 连接。工具运行时仍可在管理服务进程中执行规划、知识检索、记忆和审计，但实际游戏状态和写入权限由 Web 最终校验。

## 两种会话用途

- 独立 AI 玩家：连接器调用正常的 Web 会话 API，复用 `aigame` 的账号登录、角色创建和选角协议；进入角色后领取 Agent 租约。关闭自有会话时释放租约并注销连接。
- 人类托管基础：可信的服务端调用方对已有 Web 会话领取租约，复用当前登录连接，不另登账号，也不消费浏览器的事件队列。解除租约只归还控制权，不注销人类玩家。

本次提供内部接口和接入基础；自然语言目标输入、面向玩家的托管按钮和托管授权产品流程尚未接入。

## 内部接口

`InternalAgentHandler` 仅挂载到共享 Unix socket，默认部署路径为 `/run/stoneage-web/agent.sock`，权限 `0600`。该卷只提供给 Web 和内部运行时，不提供给 Codex 或本地 worker。公网 Web 路由不能访问 `/internal/agent/*`。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/internal/agent/attach` | 用会话 ID、当前控制代次和完整角色身份申请租约 |
| POST | `/internal/agent/observe` | 获取当前服务端快照 |
| POST | `/internal/agent/execute` | 用快照版本提交类型化游戏动作 |
| GET | `/internal/agent/watch` | 长轮询租约是否有效 |
| POST | `/internal/agent/detach` | 释放租约；不注销原游戏会话 |

除 attach 外均要求租约的随机 Bearer token。身份包括账号、角色槽位、角色名、游戏线路及可用的持久角色 ID；租约另外绑定本次角色登录实例。token 只存在于可信运行时内存，不写入提示词或玩家档案。

观察与写入均经过 Web 当前会话的控制 gate。人工接管、登出、断线或登录实例改变后，旧租约不能继续操作；迟到的 detach 不能撤销新租约。watch 将撤销传给运行时的 `LeaseDone()`，Factory 同时取消正在执行的模型回合。无租约活动超过 90 秒会释放控制权。

提交游戏动作不自动重试。确定未提交的版本冲突、无效动作和阶段错误保留原有错误类型；网络失败仍按结果未知处理，避免重复执行购买、邮件等动作。

## 特权

客户端和模型不能通过 `is_ai`、`unlimited_funds` 等参数给自己提权。无限金钱沿用服务端按已绑定账号和角色槽位发布的资金策略，由游戏服务端执行；普通人类会话不会因为被 Agent 托管而获得 AI 资金策略。

## 部署配置

启用 `docker-compose.ai.yml` 时 Web 与管理服务共享内部 socket 卷。Web 无需等待管理服务就绪，避免启动依赖环。

管理服务的游戏连接配置为 `STONEAGE_AI_WEB_BASE_URL`、`STONEAGE_AI_WEB_SERVER_ID` 和 `STONEAGE_AI_WEB_AGENT_SOCKET`。其中 Base URL 是服务间可达的 Web 地址；游戏线路 ID 必须来自 Web 的服务列表，不能用底层 TCP 地址代替。

本地 worker 复制命令使用独立的 `STONEAGE_AI_WEB_PUBLIC_URL`，必须是运行 worker 的机器可以访问的 Web 地址。它不能使用 Docker 内部的 `web` 主机名，也不从管理页面的域名推测。具体默认值见 Compose 与 `.env.compose.example`。

Web 配置 `STONEAGE_WEB_AGENT_SOCKET` 启用内部监听，并使用固定的 `STONEAGE_WEB_AGENT_GAME_UPSTREAM`、`STONEAGE_WEB_AGENT_WORKER_UPSTREAM` 转发经过允许的工具及 worker 路径。`STONEAGE_WEB_AGENT_GAME_UPSTREAM` 必须指向管理端实际监听的私有 AI Gateway `/v1/game` 地址（Compose 容器模式为 `http://admin:8081/v1/game`；同机运行时填写 `STONEAGE_AI_GATEWAY_LISTEN` 对应的本机地址），不能指向公网 Web 地址。代理不转发管理页面，也不接受调用方指定后端地址。

例如 Compose 容器模式的 Web 配置等价于：

```toml
[agent]
socket_path = "/run/stoneage-web/agent.sock"
game_upstream = "http://admin:8081/v1/game"
worker_upstream = "http://admin:8080"
```

本机 Codex 使用 `STONEAGE_AI_WEB_BASE_URL` 加 `/v1/game`；容器 Codex 使用 `STONEAGE_AI_CONTAINER_GATEWAY_URL`，Compose 已将其固定为 `http://web:8088/v1/game`。两者均由 Web frontdoor 转发到上述私有 Gateway，经过同一条 Web 会话、租约和能力校验链路。非容器运行时若 Web 与管理端分进程，必须给 `STONEAGE_AI_GATEWAY_LISTEN` 配置固定的私有端口，并将 Web 的 `game_upstream`（或 `STONEAGE_WEB_AGENT_GAME_UPSTREAM`）同步到该端口。

## 启动诊断

`worker_ready` / `connected` 仅表示本地执行器已经接入，尚不代表角色已登录或模型已启动。角色启动发生在管理服务，因此排查时需要同时查看管理服务和本地 worker 日志。

管理服务使用 `event`、`profile`、`stage`、`duration_ms` 标识启动阶段：账号绑定和初始化核验、资金策略、Web 会话创建、游戏认证、角色列表、选角、控制代次读取及 Agent 租约申请。`ai_web_request_failed` 另外记录固定接口路径、HTTP 状态和错误类别。日志不记录游戏密码、模型 key、租约 token、会话 ID、请求正文或上游错误原文。

本地执行器会报告服务端的启动失败诊断。管理页面也显示失败阶段及类别；模型运行命令尚未下发时，不应把执行器在线当作玩家运行成功。网络超时、会话失败和模型执行失败应按各自阶段处理，不能统一解释为服务器容量不足。

更换本地执行器后，旧回合仍按原执行器 ID 和代次核查。旧执行器无法联系时，只有先前经过身份校验且成功持久化的 `exited`、`stopped` 或 `removed` 记录可以作为终止证据；离线本身不能证明任务已结束。显式启动会保留未知结果并完成恢复核查，不把历史任务改写成执行成功。
