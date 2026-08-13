# 局域网与互联网联机

## 当前默认边界

默认部署完全限制在本机：

```text
客户端 -> 127.0.0.1:9065（Go 网关）
网关   -> 127.0.0.1:19065（容器 GMSV）
GMSV   -> 容器内 127.0.0.1:9300（SAAC）
```

历史 C 服务没有现代认证、TLS、限流或充分的内存安全保证，不能把 19065、
9300 或容器网络直接暴露到公网。

## 局域网测试

在服务器机器上只对可信 LAN 开放 Go 网关：

```bash
STONEAGE_GATEWAY_LISTEN=0.0.0.0:9065 ./scripts/start-local.sh
```

为每位朋友生成指向服务器 LAN IPv4 的客户端；旧二进制字段只支持 IPv4：

```bash
./scripts/patch-legacy-client.py \
  --source runtime/legacy-client/sa_2903.exe \
  --output runtime/legacy-client/sa_2903-lan.exe \
  --host 192.168.1.50 --port 9065 --bypass-wgs
```

把完整 `runtime/legacy-client/` 复制给朋友，并让其启动 `sa_2903-lan.exe`。
只在主机防火墙中允许可信局域网到 TCP 9065；19065 和 9300 保持本机/容器内。

每位玩家必须使用不同账号。当前开发账号层可自动接受新账号，但它是本地测试
便利功能，不是公网注册系统。

本机并发基线已用两个独立 CP936 Wine prefix 验证：`ProbeHero` 与
`FriendHero` 同时通过网关进入地图 1006，第二人物移动并断线后坐标成功写回
SAAC。跨物理机器的 LAN/防火墙测试仍需在有第二台设备时执行。

## 互联网阶段的最低要求

在以下事项完成前，不做路由器端口转发：

- 用现代账号服务替换开发用 SAAC 登录路径，密码使用 Argon2id/bcrypt 哈希
- 网关加入认证会话、连接数限制、包大小/速率限制和审计日志
- 只公开一个受保护的边缘入口；GMSV/SAAC 永远位于私网
- 传输加密（VPN 如 Tailscale/WireGuard，或网关 TLS 隧道）
- 角色数据备份、恢复演练以及服务端崩溃隔离
- MySQL 若被引入，限定 8.0 + `utf8mb4`，不公开 3306

朋友小规模联机的第一选择是可信 VPN：每台设备加入同一个 Tailscale 或
WireGuard 私网，客户端补丁指向服务器的 VPN IPv4，防火墙只允许该接口访问
9065。多账号、并发、移动、聊天与断线保存已在本机双客户端完成回归；跨物理
机器的防火墙/VPN 路径仍需在实际网络上验证。

Linux amd64 发布包可直接作为朋友服务器：

```bash
STONEAGE_GATEWAY_LISTEN=0.0.0.0:9065 ./start-server.sh
```

macOS `.app` 默认严格绑定本机；若要让它充当朋友服务器，请使用仓库脚本或
Linux 发布包，以免误把历史服务暴露到公网。
