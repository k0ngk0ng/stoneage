# 连接与结果处理

## 本地前提

v0.2.2 起，首次配置由用户在终端运行 `sactl init`，只设置服务器及可选角色，不保存账号密码；默认写入用户配置目录，已有文件不会覆盖。用户运行 `sactl login` 交互输入账号及隐藏的密码，自动启动后台进程，凭据仅保留在内存中。Agent 不要求用户在聊天或命令参数里传密码。多会话用 `--profile <name>`，每条命令保持同一 profile；也可使用自定义 `--config`，两者不能混用。

需要已安装的 sactl 二进制和用户配置。可通过 `brew install k0ngk0ng/tap/sactl`、Scoop 的 `k0ngk0ng/sactl` 或项目 GitHub Release 安装；安装软件本身须符合当前用户授权。

配置查找顺序：`--config` → `STONEAGE_SACTL_CONFIG` → 当前目录 `sactl.toml` → `~/.config/sactl/sactl.toml`。不要读取或输出整份配置来排障。查看 `sactl --help` 确认版本支持的参数。

`serve` 持有长连接；普通命令通过 socket 访问它。守护进程未运行且用户已授权登录时，在允许的工作目录中按现有环境启动：

```sh
sactl login --config /path/to/sactl.toml  # 用户在终端交互登录
# 后续每条命令使用相同配置
sactl --config /path/to/sactl.toml --json chars
sactl --config /path/to/sactl.toml --json enter '用户指定的角色'
sactl --config /path/to/sactl.toml --json observe
```

`serve` 默认后台运行，只启动本地进程，不代表已经登录；`login` 才负责交互认证。`serve --foreground` 供排障和外部进程管理器使用，不能无边界等待其退出。尚未登录时请用户在终端执行 login，不索取明文密码。缺少目标角色或连接地址时向用户询问，不猜生产地址。

## JSON 与退出码

返回外层类似：

```json
{"ok":true,"data":{"Phase":"battle","Revision":42},"text":"供人阅读的摘要"}
```

这是外层格式示意，不是完整观察。`data` 的形状取决于命令；某些操作只有 `ok` 和 `text`，用后续 `observe` 核验。

| 结果 | 处理 |
| --- | --- |
| `ok=true` | 读取该命令的数据；核对预期游戏状态变化 |
| `kind=usage` / 退出码 2 | 修正命令或参数，不原样重试 |
| `kind=session` / 退出码 3 | 检查 daemon、配置或连接；未必仅是 daemon 未启动 |
| `kind=action` / 退出码 1 | 根据服务端原因及观察调整动作 |
| `kind=unknown` / 退出码 1 | 可能已提交；先观察核对，不能直接重发 |
| 退出码 4 | CLI 本地错误或 daemon 启动失败；可能没有 JSON，查看错误并核实状态 |
| `kind=server` / 退出码 1 | 报告服务端错误，核实状态后决定是否继续 |

即使没有合法 JSON（进程失败、连接断开），已提交动作也可能发生。不得把错误自动解释为“没有执行”。

`wait 30s` 等待下一条事件，并不指定等待某个业务结果；它也不能替代观察。`status` 只观察当前连接，不触发登录。其他游戏命令在有内存凭据或兼容旧配置时可能重连。`logout` 清除内存凭据，之后不会因查询而重新登录。

`auto-battle off` 停止自动战斗策略，`stop` 结束 sactl daemon，`logout` 离开世界并关闭会话，三者含义不同。不要为了结束一次任务擅自关闭用户原有会话。
