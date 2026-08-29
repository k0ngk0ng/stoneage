# sa_2903 web

这里是 `runtime/legacy-client/sa_2903.exe` 的浏览器端移植，运行时只有两个产物：

* `index.html`：自包含网页、完整 LSSPROTO_CLI 函数控制台、世界/战斗状态机、库存/宠物/通讯/服务器窗口状态机和旧客户端线协议；
* `server.go`：标准库 HTTP→TCP 转发服务，并只读提供 `runtime/legacy-client/data/bgm`、`se` 下的原版 WAV；它不解析、不重排、不改写游戏包，只为浏览器持有原版 TCP socket。

网页里的 `StoneAgeProtocol` 是根据 PE 机器码中 `LSSPROTO_CLI/LSSPROTO_UTIL` 的调用路径实现的：消息号/函数名头、空格转义、0–61 base-62 整数、JEncode、`ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-` 64 表、9 位 Ringo 字典压缩和换行分帧都在网页中完成。`LSSPROTO_CLI.H` 中 50 个客户端发送入口和 36 个服务端回调均有字段 schema；页面提供对应的通用协议控制台，并对登录、角色、地图、移动、库存、宠物、通讯录/邮件、事件、服务器窗口、聊天和战斗提供专用状态与操作。

网页按 `sa_2903.exe` 与 Linux 2.5 GMSV 的命名协议编码无参数 `CharLogout`。“回记录点”发送这个包，由 2.5 GMSV 在保存前改写为记录点；“原地登出”先停止尚未发送的长距离路线并排空已经排队的写入，再用 2.5 原生 `S("c")` 状态请求核对 GMSV 实际持有的楼层与坐标。若战斗退出或乐观行走造成最后一两步尚未被服务端接受，页面会通过单步 `W` 与新的 `S("c")` 回包完成校正；只有坐标一致后才半关闭当前 TCP 会话。本仓库的 2.5 运行补丁会让 EOF 连接停留在 `WHILELOGOUTSAVE`，等 `saacproto_ACCharSave_recv()` 收到人物档落盘回执后才关闭 GMSV socket；桥接端另保留一个短暂的持久化排空期，以兼容未安装该补丁的原始 2.5 服务端。页面刷新/卸载时会用一次 `fetch(..., {keepalive:true})` 尝试发送合法的无参数登出包，桥接端在同一请求中完成 TCP 会话清理；发送失败仍会走 DELETE 兜底，不伪造 8.5 独有的 `CharLogout(Flg)` 扩展。

网页只借用 8.5 客户端的视觉、按钮位置和输入习惯；线协议严格以本仓库的 2.5 GMSV 头文件及 `server/go/bridge/translator.go` 的数字桥接表为准。`protocol_test.js` 会逐项对比网页 schema、桥接表和本地 2.5 `lssproto_serv.h` 的函数编号/字段，防止把 8.5 的函数或字段顺序带进 2.5。左上角的角色卡按钮只发送 2.5 支持的 `AAB` 查看前方玩家；第四格轮胎图标使用 2.5 的 `TD("D|D")` 查找正前方玩家，不再误走 NPC 的 `TK` 对话；NPC/门的问候不会由地面点击自动触发，按 `T` 后才发送原版 `TK(P|hi)`，服务器返回的 `TK`/`WN` 仍走游戏内聊天和窗口层。8.5 的 `SaMenu`、`RideQuery`、签到和摆摊等可选入口不加入 2.5 schema，相关视觉按钮不会向服务器发未知封包。

目标二进制的已知 SHA-256 是 `9abb989b207d2db6eeb0a95fbc13cfb681eca8a20e9ddc169edb64a03497a3ee`；网页不加载或执行 exe，也不依赖 `client/mobile` 的 Godot 场景。服务器只把网页生成的包原样转给现有 TCP 网关。

## 启动

先启动仓库已有的游戏网关（默认 `127.0.0.1:9065`），再在本目录启动网页转发器：

```bash
cd client/web
GOFLAGS=-mod=mod STONEAGE_TCP_UPSTREAM=127.0.0.1:9065 go run .
```

然后打开 <http://127.0.0.1:8088/>；如果从同一局域网的其他设备访问，请使用运行网页服务这台机器的局域网 IP，例如 `http://192.0.2.10:8088/`。网页转发器默认监听 `0.0.0.0:8088`，也可以用环境变量限制监听地址。常用配置：

```bash
STONEAGE_WEB_LISTEN=0.0.0.0:8088 \
STONEAGE_TCP_UPSTREAM=127.0.0.1:9065 \
STONEAGE_WEB_ASSETS=assets/original \
STONEAGE_WEB_MAPS=runtime/legacy-client/map \
STONEAGE_WEB_AUDIO=runtime/legacy-client/data \
STONEAGE_WEB_CDN_BASE_URL=https://cdn.example.com/stoneage \
STONEAGE_WEB_MAX_SESSIONS=64 \
GOFLAGS=-mod=mod go run .
```

生产启动使用根目录的 [`config/web.toml`](../../config/web.toml) 集中配置 Web、OSS
和 CDN，Compose 会把它只读挂载并执行
`stoneage-web -config /etc/stoneage/web.toml`。本地也可以复制一份、把其中的目录和
TCP 上游改为本机路径后运行 `go run . -config /path/to/web.toml`；也支持
`STONEAGE_WEB_CONFIG=/path/to/web.toml`。文件中的未知字段、非法 OSS endpoint、
不完整的 endpoint/bucket 组合和非法 CDN URL 都会让服务在启动时直接报错。

从仓库的 `client/web` 目录启动时，`STONEAGE_WEB_ASSETS` 默认就是 `assets/original`。这组资源直接从 `runtime/legacy-client/sa_2903.exe` 的伴随数据（`real_15.bin`、`adrn_15.bin`、`spr_4.bin`、`spradrn_5.bin`、地图和 `Palet_1.sap`）提取，不引用 `/client/mobile` 的图片；所有已同步的地图、物件、角色和战斗场景都使用原版索引与位图。部署到别的目录时请显式设置该变量。资源通过只读 `/assets/` 路径提供，不接受浏览器指定任意目录。

`static.cdn.base_url` 是公开静态资源根地址；`STONEAGE_WEB_CDN_BASE_URL` 仅保留为应急环境变量覆盖。设置后，返回给浏览器的页面会把 `/assets/`、`/maps/` 和 `/audio/` 改写为这个根地址下的同名目录；`/api/sessions`、`/api/npcs` 等动态接口不会改写。`static.oss` 保存阿里云 OSS 的 endpoint、region、bucket、固定 prefix；AK/SK 不写在 TOML，而是仅作为 Docker secret 文件注入批量资源同步工具，Web 游戏进程不会读取或上传。如果配置了 OSS 而未配置 CDN，Web 后端会自动使用 `https://<bucket>.<endpoint>/<prefix>` 作为公开资源根地址。CDN 基址只接受不含账号、查询串和片段的绝对 HTTP(S) URL，末尾斜线会自动去除；OSS endpoint/bucket 必须同时设置。生产部署应使用 HTTPS 和固定资源根目录，CDN 路径和 OSS prefix 都不带 tag，发布时增量同步；JSON 清单使用短缓存或 `no-cache`，PNG、地图和音频可长期缓存，同名二进制确实发生变化时刷新对应 CDN URL。CDN/OSS 源站还需为网页域名配置 CORS 和 `Timing-Allow-Origin`（下载进度需要）。OSS/CDN 都未设置时仍由当前 Web 进程提供本地文件，便于开发和故障排查。

客户端资源应作为整体由部署脚本或 CI 发布。仓库根目录的
`scripts/sync-client-assets.sh` 会启动一次性的 Compose `assets-sync` 容器，统一同步
`assets/`、`maps/`、`audio/` 三棵目录；任务结束后容器即删除，AK/SK 不会进入 Web、
admin HTTP 进程或浏览器。同步器只取 `client/web/assets/original`、`map/`、
`data/auto.dat`、`data/bgm/` 和 `data/se/`，不会把客户端存档、聊天记录或 PE 支持文件
放进公开 bucket。admin 的资源按钮（如启用）只是请求同一个受限整包任务，不会接收路径、
bucket 或命令参数；service-control 仅在任务启动时从两个 Docker secret 文件读取密钥。
若资源已经由 CI 发布，不配置同步凭据即可。

音乐通过只读 `/audio/bgm/` 和 `/audio/se/` 路径提供。网页按 `_SA_VERSION_25` 客户端的时机切换标题、地图、战斗/首领 BGM，并按服务器 `SE` 包播放对应音效；浏览器第一次用户操作后才会解锁音频，这是浏览器自动播放策略的限制。WAV 使用长期缓存，地图音乐标记只接受随附 `sa_2903` 2.5 客户端源码的 40–46 范围；47–53 属于后续 8.5 表，不会在本网页端启用。

地图音乐按 `MAP.CPP` 的绘制顺序读取当前 M 窗口：同一格先检查地面再检查物件，采用最后一个有效 40–46 标记；窗口没有标记时回到 BGM 0，避免从上一张地图/房间继承错误音乐。楼层切换期间完成的 DAT 会保存在进程缓存，返回该楼层时重新安装到当前自动地图绘制缓冲，不会留下灰色菱形。

战斗指令菜单沿用随附 2.5 `sa_2903` 客户端的 `BATTLEMENU.CPP` `BattleCntDown`：BP/BC 和入场动作完成后开始 30 秒倒计时，人物与出战宠物共用同一个截止时间；倒计时结束会按原客户端顺序提交 `N`，有存活出战宠物时再提交 `W|FF|FF`。数字使用 `CG_CNT_DOWN_0..9` 原版贴图（25900–25909），不使用浏览器字体；2.5 GMSV 的命令超时为 120 秒，网页的安全 watchdog 只用于异常断线兜底。8.5 源码中的 99 秒 `_BATTLE_TIME_` 不用于本网页端。

地图行走也保留 `map.cpp` 的按住左键模式：按下地图空白处立即设定目标，按住时每 250ms 更新 `moveStack`/剩余路线，抬起后继续走到最后一个目标；鱼骨头平时跟随真实鼠标，按钮、聊天输入、NPC 选择和战斗层不会被地图拖动事件抢走。场景折叠期间只禁用路线/点击输入，鱼骨头仍跟随真实鼠标样本，不会被场景动画带着滑动或被脚本回中；浏览器真实鼠标不被锁定、捕获或程序移动。

开启回旋镖标志时，攻击目标遵循 `BattleButtonAttack()`：排除攻击者所在的五格行，其他存活角色均可点击；网页发送原始 battle id，由 2.5 GMSV 按 `bid / 5` 计算回旋镖行目标。

原版地图 DAT/MAP 通过只读 `/maps/` 路径提供。地图按钮使用 2.5 客户端的 `createAutoMap()` 规则读取当前楼层的地面、物件和 `MAP_SEE_FLAG`，绘制 54×54 菱形自动地图；可用 `STONEAGE_WEB_MAPS` 指定地图目录。

楼层切换沿用 `map.cpp/netproc.cpp/process.cpp` 的 `PRODUCE_CENTER_PRESSIN/OUT`：踩上传送格发送 `EV` 时先锁住旧地图的 back-buffer 并合拢黑幕，目标楼层的 `S:C`/`M` 只在黑屏下填充私有 back-buffer，M 窗口资源完整后才从中线展开。鱼骨头不是浏览器的原生鼠标，也不会触发 Pointer Lock；转场期间只禁用地图路线输入，鱼骨头仍根据真实鼠标移动更新，并且不随被压缩的场景层移动；浏览器真实鼠标不被锁定、捕获或程序移动。服务端主动传送则在更新楼层前启动同一流程，同楼层 M/MC 刷新不会触发黑幕。地图事件 3 为普通传送，6/7/8 分别受原版晨间、午间/傍晚、夜间时段限制。

本地调试时如果不想听音乐，可在地址后加 `?debugMute=1`（也支持
`localStorage.setItem("stoneage:web:debug-mute", "1")`）。这是开发用静音开关，
不改变普通 URL 下的原版 BGM/SE 默认设置。

如果需要从原始安装包重建资源包，运行：

```bash
python3 tools/extract-legacy-web-assets.py --all-battles
```

这会重建登录/人物/字段/战斗 UI、角色帧、地图 200 视口，以及原始 `battleMap` 的 220 个 640×480 战斗视口；所有 PNG 均来自 `sa_2903.exe` 的伴随数据。

`STONEAGE_TCP_UPSTREAM` 只在服务启动时读取，HTTP 客户端不能指定任意 TCP 地址。公网部署时只公开 8088（并放在 HTTPS 反向代理后），不要公开旧网关/SAAC 端口。

Linux Docker Compose 部署由根目录的 `docker-compose.yml` 管理。`web` 容器使用
control-plane 镜像内的 `assets/original`，并以只读方式挂载
`STONEAGE_CLIENT_DATA_ROOT` 提供 2.5 客户端的 `map/`、`data/bgm/` 和 `data/se/`；
它只通过 Compose 内网的 `gateway:9065` 转发协议，不会把 GMSV 或 SAAC 端口暴露给
浏览器。首次部署可直接运行 `./scripts/deploy-mvp.sh --init`，详见根目录
README 的 Linux Docker Compose 小节。

转发 API 是 `POST /api/sessions`（建立 TCP 并返回 `L\0` 握手）、`POST /api/sessions/:id/send`（JSON `{packet: base64}`）、`GET /api/sessions/:id/events`（按 TCP 顺序长轮询 base64 包）、`DELETE /api/sessions/:id`，另有只读资源 `GET /assets/*`、`GET /maps/*` 和 `GET /audio/*`。`send?close=1` 会在写入这一包后原子关闭桥接会话，供刷新/卸载阶段的 keepalive 登出使用；即使写入失败也会清理会话。网页已经内置这套调用，不需要额外前端构建工具。

## 测试

```bash
cd client/web
GOFLAGS=-mod=mod go test -race -count=1
node protocol_test.js
```

Go 测试使用本地 fake TCP upstream 覆盖握手、双向逐包转发、长轮询、关闭、错误握手、包大小、资源路由和并发会话上限；Node 用原版客户端捕获向量及 `LSSPROTO_CLI.H` 全量入口检查网页脚本、JEncode、Ringo、转义、base-62 和 schema。实际登录/地图/战斗仍以运行中的 GMSV/SAAC 返回为权威数据。
