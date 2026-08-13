# macOS 发布版使用说明

`StoneAge Revival.app` 已内置 2.5 客户端、Go 协议网关及 Apple Silicon
Linux 服务端资产。仍需预先安装并启动：

- Wine Stable 11（需能从终端执行 `wine`）
- Docker Desktop 或 OrbStack（需能从终端执行 `docker`）

双击应用即可启动。首次运行会在
`~/Library/Application Support/StoneAge Revival/` 创建 CP936 Wine prefix、
可写服务端和日志；游戏资产仍从应用内读取。客户端中选择“本機”与
“本機一線”。

发布包不携带开发机的登录密码、聊天记录、邮件、图鉴、日志或崩溃转储，只
预置 `probe` / `local` 与人物 `ProbeHero` 作为可玩演示档。

应用使用本地 ad-hoc 签名，没有 Apple Developer ID 公证。若 Gatekeeper
阻止第一次启动，请在 Finder 中按住 Control 点击应用，选择“打开”。

停止服务请双击应用内 `Contents/Resources/停止伺服器.command`。角色数据
保存在上述 Application Support 目录，不会因删除 Docker 容器而丢失。

旧客户端画面文案为繁体风格，但底层字节编码是 CP936/GBK；不要改为 CP950
或 UTF-8。
