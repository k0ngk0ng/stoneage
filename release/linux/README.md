# StoneAge Revival 2.5 — Linux amd64 服务端

要求：x86-64 Linux、Docker Engine，以及可用的 `nc`。默认只监听
`127.0.0.1:9065`：

```bash
./start-server.sh
./status-server.sh
./stop-server.sh
```

可信局域网或 VPN 联机时，可令 Go 网关监听所有接口：

```bash
STONEAGE_GATEWAY_LISTEN=0.0.0.0:9065 ./start-server.sh
```

只允许受信任设备访问 TCP 9065。不要公开 GMSV 的 19065、SAAC 的 9300，
也不要公开数据库端口。当前 SAAC 使用平面文件；若将来迁移数据库，唯一支持
基线为 MySQL 8.0 + `utf8mb4`。

角色数据位于 `runtime/legacy-server/saac/`。备份前先执行 `stop-server.sh`，
然后完整复制该目录。

内置开发账号为 `probe` / `local`，人物 `ProbeHero`。这是可信本地或 VPN
测试账号，不应视作安全的公网账号系统。

发布包只包含这个演示人物，不包含开发机的第二测试账号、运行日志、诊断文件
或崩溃转储。
