# StoneAge Revival 2.5 — Linux amd64 服务端

要求：x86-64 Linux、Docker Engine，以及可用的 `nc`。游戏网关默认只监听
`127.0.0.1:9065`，管理后台默认只监听 `127.0.0.1:8080`：

```bash
./start-server.sh
./start-admin.sh
./status-server.sh
./status-admin.sh
```

第一次使用前创建后台管理员（只需一次）：

```bash
printf '%s\n' '请替换为强密码' | \
  ./bin/stoneage-admin create-admin \
    -db runtime/stoneage-auth.db -username admin -password-stdin
```

然后访问 `http://127.0.0.1:8080/`。如需临时网页初始化，可在启动后台前设置
一个随机的 `STONEAGE_ADMIN_SETUP_TOKEN`，访问 `/setup` 创建管理员；不要把该
令牌写入公开脚本或长期保留。

后台可以管理游戏账号、启用/禁用账号、重置密码、查看审计日志、发送在线通知，
在“游戏服务”内部分别修改 GMSV/SAAC 常用配置，并通过受限 operator 重启服务：

```bash
./stop-admin.sh       # 停后台，不删除账号
./restart-gateway.sh  # 只重启游戏网关，通常不会影响 GMSV/SAAC
./restart-gmsv.sh     # 只重启 GMSV
./restart-saac.sh     # 只重启 SAAC
./stop-gmsv.sh        # 只停止 GMSV，容器和 SAAC 继续运行
./stop-saac.sh        # 只停止 SAAC，容器和 GMSV 继续运行
./restart-game.sh     # 重启 GMSV + SAAC，会断开玩家
./restart-server.sh   # 重启游戏服务和游戏网关，会断开玩家
./stop-server.sh      # 停游戏服务，保留角色和账号数据
```

后台“服务”页面展示游戏网关和游戏服务两个部署服务，游戏服务内部提供 GMSV、SAAC
配置入口；GMSV 配置编辑
`runtime/legacy-server/gmsv/setup.cf`，SAAC 配置编辑
`runtime/legacy-server/saac/acserv.cf`。配置页面的保存不会自动重启服务，需回到
“服务”页面手动重启对应进程。在线通知会写入受限的 `admin-notice.txt` 队列，
由 GMSV 主循环发送给当前在线玩家。

`runtime/stoneage-auth.db` 是 SQLite 认证库；角色、邮件和家族仍在
`runtime/legacy-server/saac/`。备份前先停止两个服务。网关已经强制使用
`-auth-required`，未知账号或错误密码会被拒绝；不要把它改回无认证模式后公开到
互联网。

如果希望由 Docker Compose 统一编排网关、后台、operator 和旧版游戏服务，请在
仓库根目录使用 `docker-compose.yml`；本地镜像先执行
`./scripts/build-local-images.sh`，生产环境则把 `.env` 指向 GHCR 的版本镜像后执行
`docker compose up -d`。Compose 的 operator 需要访问 Docker socket 执行
经过固定白名单的服务脚本，后台本身不挂载该 socket。默认端口仍只绑定回环地址。

可信局域网或 VPN 联机时，可令 Go 网关监听所有接口：

```bash
STONEAGE_GATEWAY_LISTEN=0.0.0.0:9065 ./start-server.sh
```

只允许受信任设备访问 TCP 9065。不要公开 GMSV 的 19065、SAAC 的 9300、管理
后台 8080 或 SQLite 文件。需要互联网访问时，请将后台放在 Caddy/Nginx HTTPS
反向代理后，并把 `STONEAGE_ADMIN_COOKIE_SECURE=true` 传给 `start-admin.sh`；
游戏端优先使用 Tailscale/WireGuard 等 VPN。

当前 Compose/发布说明只覆盖单机单线路。客户端启动器可以列出分别部署的其他
服务器，但不要向单线路 Compose 或启动脚本追加额外 GMSV 监听端口；多节点部署
会单独提供编排方式。

发布包只包含演示人物，不包含开发机账号、运行日志、诊断文件或崩溃转储。
