# StoneAge 2.5 Web

这里是 `runtime/legacy-client/sa_2903.exe` 的浏览器端移植，运行时只有两个产物：

* `index.html`：自包含网页、完整 LSSPROTO_CLI 函数控制台、世界/战斗状态机、库存/宠物/通讯/服务器窗口状态机和旧客户端线协议；
* `server.go`：标准库 HTTP→TCP 转发服务，并只读提供 `runtime/legacy-client/data/bgm`、`se` 下的原版 WAV 以及自动地图所需的 `auto.dat`/`pal` 数据；它不解析、不重排、不改写游戏包，只为浏览器持有原版 TCP socket。

网页里的 `StoneAgeProtocol` 是根据 PE 机器码中 `LSSPROTO_CLI/LSSPROTO_UTIL` 的调用路径实现的：消息号/函数名头、空格转义、0–61 base-62 整数、JEncode、`ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-` 64 表、9 位 Ringo 字典压缩和换行分帧都在网页中完成。`LSSPROTO_CLI.H` 中 50 个客户端发送入口和 36 个服务端回调均有字段 schema；页面提供对应的通用协议控制台，并对登录、角色、地图、移动、库存、宠物、通讯录/邮件、事件、服务器窗口、聊天和战斗提供专用状态与操作。

网页按 `sa_2903.exe` 与 Linux 2.5 GMSV 的命名协议编码无参数 `CharLogout`。“回记录点”发送这个包，由 2.5 GMSV 在保存前改写为记录点；“原地登出”先停止尚未发送的长距离路线并排空已经排队的写入，再用 2.5 原生 `S("c")` 状态请求核对 GMSV 实际持有的楼层与坐标。若战斗退出或乐观行走造成最后一两步尚未被服务端接受，页面会通过单步 `W` 与新的 `S("c")` 回包完成校正；只有坐标一致后才半关闭当前 TCP 会话。本仓库的 2.5 运行补丁会让 EOF 连接停留在 `WHILELOGOUTSAVE`，等 `saacproto_ACCharSave_recv()` 收到人物档落盘回执后才关闭 GMSV socket；桥接端另保留一个短暂的持久化排空期，以兼容未安装该补丁的原始 2.5 服务端。页面刷新/卸载时通过 `DELETE` 与 `{keepalive:true}` 关闭连接，走原生断线保存路径，保留服务端已确认的当前位置；不发送会回记录点的 `CharLogout`。浏览器进程被强制终止而未触发页面事件时，由服务端断线或会话过期处理。

网页只借用 8.5 客户端的视觉、按钮位置和输入习惯；线协议严格以本仓库的 2.5 GMSV 头文件及 `server/go/bridge/translator.go` 的数字桥接表为准。`protocol_test.js` 会逐项对比网页 schema、桥接表和本地 2.5 `lssproto_serv.h` 的函数编号/字段，防止把 8.5 的函数或字段顺序带进 2.5。左上角的角色卡按钮只发送 2.5 支持的 `AAB` 查看前方玩家；第四格轮胎图标使用 2.5 的 `TD("D|D")` 查找正前方玩家，不再误走 NPC 的 `TK` 对话；点击有效 NPC 后才走原版 `TK(P|hi)`，服务器返回的 `TK`/`WN` 仍进入游戏内聊天和窗口层。8.5 的 `SaMenu`、`RideQuery`、签到和摆摊等可选入口不加入 2.5 schema，相关视觉按钮不会向服务器发未知封包。

场景事件的返回值也以 2.5 GMSV 为准：`callfromcli.c` 把 `EVENT_main()` 的 C `TRUE` 原值（`1`）放进 `EV` 回包，`0` 才是事件未接受。8.5 新服务端的 `0` 成功约定不能直接套用到网页，否则正常传送会被误报为失败。

正式版 2.5 的客户端本地键盘功能也按源码保留（详见 [`KEYBOARD_AUDIT.md`](KEYBOARD_AUDIT.md)）：`Delete` 清空当前 20 行聊天，`↑/↓` 浏览最近 64 条发送历史，`F1`–`F8` 插入八条记录文字，`Tab` 保持原输入缓冲区规则，`Enter` 直接发送已有内容；这些行为在地图和战斗阶段都使用同一个聊天缓冲区。系统菜单里的显示行数（0–20）、十种文字颜色和发言范围（原版 `NowMaxVoice`，1–10）会在本地保存，并以原版 `MyChatBuffer.color`/`NowMaxVoice` 数值写入 `TK`。`Esc`、任务栏/动作的 `Ctrl` 组合键、`F5` 禁止刷新、`Alt+Enter` 全屏切换和 `F12` 640×480 截图同样由网页本地处理。动作栏最后一项原版是 `Ctrl+Yen`，网页兼容 `\\`、`IntlYen`、`¥/₩` 等浏览器键码；小键盘减号也兼容 `NumpadSubtract`。8.5 的 `Ctrl+V` 也已兼容：聊天输入框失焦时仍会从系统剪贴板插入文本；`Ctrl+R` 属于 8.5 `_TELLCHANNEL` 密语频道，而 2.5 没有对应服务端协议，故不发送伪造封包。聊天中的普通文字、方括号命令和斜杠命令全部原样包装为 `TK(P|text)` 交给 2.5 GMSV，不在网页重复实现服务端命令解析；只存在于 `_DEBUG` 客户端编译的测试命令不会进入网页。原版没有 `/clear` 文字指令；网页额外接受带斜杠别名及其不带斜杠的严格同义词作为本地清屏别名，不向服务端发送；同时兼容手机输入法产生的全角斜杠/字母。macOS 上标为 Delete 的退格键保留原版 Backspace 删除输入，使用 `Cmd+Backspace` 清空聊天；前向删除键仍按原版 `Delete` 处理。

协议/指令的逐项差异（包括 `PMSG` 宠物邮件和场景 `PS` 宠物技能的专用网页入口）见 [`COMMAND_AUDIT.md`](COMMAND_AUDIT.md)。

目标二进制的已知 SHA-256 是 `9abb989b207d2db6eeb0a95fbc13cfb681eca8a20e9ddc169edb64a03497a3ee`；网页不加载或执行 exe。服务器只把网页生成的包原样转给现有 TCP 网关。

登录页左下角以低调的灰褐色显示当前程序发布版本（例如 `v0.1.23`），来自镜像或二进制构建时注入的 Git tag；本地开发显示 `dev`，与资源包版本独立。

## 大屏显示

默认使用“适应窗口”模式：场景仍采用 640×480 逻辑坐标，保持比例放大或缩小至贴合可用窗口。选择“倍数显示”后，可放大的窗口按屏幕物理像素的整数倍显示，并对齐像素边界；手机等需要缩小的窗口继续适应可用空间。

游戏内打开“系统”，默认使用“适应窗口”，点击可切换为“倍数显示”，再次点击切回；选择保存在当前浏览器。显示模式同时应用于登录、人物、地图、战斗及弹窗。人物名字与家族名使用独立高分辨率文字层，原始角色和地图素材保留像素风格。

本地显示回归可在仓库根目录运行 `node client/web/display_scaling_test.js` 和 `node client/web/world_labels_test.js`。

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

生产启动使用根目录的 [`config/web.toml`](../../config/web.toml) 集中配置 Web、对象存储
和 CDN，Compose 会把它只读挂载并执行
`stoneage-web -config /etc/stoneage/web.toml`。本地也可以复制一份、把其中的目录和
TCP 上游改为本机路径后运行 `go run . -config /path/to/web.toml`；也支持
`STONEAGE_WEB_CONFIG=/path/to/web.toml`。文件中的未知字段、非法对象存储 endpoint、
不完整的 endpoint/bucket 组合和非法 CDN URL 都会让服务在启动时直接报错。

从仓库的 `client/web` 目录启动时，`STONEAGE_WEB_ASSETS` 默认就是 `assets/original`。这组资源直接从 `runtime/legacy-client/sa_2903.exe` 的伴随数据（`real_15.bin`、`adrn_15.bin`、`spr_4.bin`、`spradrn_5.bin`、地图和 `Palet_1.sap`）提取；所有已同步的地图、物件、角色和战斗场景都使用原版索引与位图。部署到别的目录时请显式设置该变量。资源通过只读 `/assets/` 路径提供，不接受浏览器指定任意目录。

`static.cdn.base_url` 是公开静态资源根地址；`STONEAGE_WEB_CDN_BASE_URL` 仅保留为应急环境变量覆盖。设置后，返回给浏览器的页面会把 `/assets/`、`/maps/` 和 `/audio/` 改写为这个根地址下的同名目录；`/api/sessions`、`/api/npcs` 等动态接口不会改写。`static.oss` 保存对象存储的 provider、endpoint、region、bucket、固定 prefix；provider 支持 `aliyun-oss` 和 `cloudflare-r2`。AK/SK 不写在 TOML，而是仅作为 Docker secret 文件注入批量资源同步工具，Web 游戏进程不会读取或上传。如果配置了阿里云 OSS 而未配置 CDN，Web 后端会自动使用 `https://<bucket>.<endpoint>/<prefix>` 作为公开资源根地址；R2 的 S3 endpoint 默认需要签名，必须配 Cloudflare CDN 自定义域名。CDN 基址只接受不含账号、查询串和片段的绝对 HTTP(S) URL，末尾斜线会自动去除；对象存储 endpoint/bucket 必须同时设置。生产部署应使用 HTTPS 和固定资源根目录，CDN 路径和存储 prefix 都不带 tag，发布时增量同步；JSON 清单使用短缓存或 `no-cache`，PNG、地图和音频可长期缓存，同名二进制确实发生变化时刷新对应 CDN URL。CDN/对象存储源站还需为网页域名配置 CORS 和 `Timing-Allow-Origin`（下载进度需要）。存储和 CDN 都未设置时仍由当前 Web 进程提供本地文件，便于开发和故障排查。

页面启动时会注册同源 `/sw.js`。Service Worker 只接管 `assets/`、`maps/`、`audio/` 的 GET：二进制采用
cache-first，`_client-version.json` 和索引采用 network-first，API、TCP 轮询和 HTML 不缓存。版本 marker 的
revision 是 Cache Storage 命名空间；网页先拿到新 revision 才切换缓存，断网时仍可回退已有资源。CDN 根地址
会发送给 worker，因此跨域 OSS/R2 资源也能缓存（源站必须允许网页域名的 CORS GET/HEAD）。

客户端资源应作为整体由部署脚本或 CI 发布；Admin 是独立运维应用，不属于客户端资源
包，也不会接触 OSS AK/SK。仓库根目录的
`scripts/sync-client-assets.sh` 会启动一次性的 Compose `assets-sync` 容器，统一同步
`assets/`、`maps/`、`audio/` 三棵目录；任务结束后容器即删除，AK/SK 不会进入 Web、
admin HTTP 进程或浏览器。同步器只取 `client/web/assets/original`、`map/`、
`data/auto.dat`、`data/bgm/`、`data/se/` 和 `data/pal/`，不会把客户端存档、聊天记录或 PE 支持文件
放进公开 bucket。同步完成后会写入固定根目录下的 `_client-manifest.json`，记录公开文件
的大小和 SHA-256；再次发布只上传变化对象，且在全部对象成功后才更新清单。admin 的
资源按钮（如启用）只是请求同一个受限整包任务，不会接收路径、bucket 或命令参数；
service-control 仅在任务启动时从两个 Docker secret 文件读取密钥。
上传顺序固定为“图片/地图/音频 → `*.json` 与 `audio/auto.dat` → 发布清单”，这样
浏览器不会在索引先更新时读到尚未完成的资源。
若资源已经由 CI 发布，不配置同步凭据即可。

同步器随后还会写入同目录的 `_client-version.json`，内容只有 `revision`、对象数和总字节数，不会把 release tag
放进 URL。网页首次只加载登录/UI/当前地图；进入世界后在空闲时预热少量常用音乐和音效，其余地图与精灵按需
下载并显示字节进度。当前 1.2GB 资源不强制合并成单个 `.pack`，也不要求 OPFS；未来资源达到数 GB 时可增加
按地图/音频/精灵分片 pack，OPFS 作为可选加速层而不是运行时硬依赖。

Service Worker 在 revision 变化时会切换到新的 Cache Storage 命名空间，并接收发布器根据
`_client-manifest.json` 计算的变更/删除路径及其基准 revision；只有浏览器当前缓存正好是这个基准版本时，
未变化对象才会从上一个命名空间安全迁移命中。跳过版本、变化或删除对象不会复用旧内容；旧版没有 delta 信息的 marker
也会按全量更新处理。

音乐通过只读 `/audio/bgm/` 和 `/audio/se/` 路径提供。网页按 `_SA_VERSION_25` 客户端的时机切换标题、地图、战斗/首领 BGM，并按服务器 `SE` 包播放对应音效；浏览器第一次用户操作后才会解锁音频，这是浏览器自动播放策略的限制。WAV 使用长期缓存，地图音乐标记按 2.5 客户端实际使用的 40–53 范围映射；仅 54–55 属于受 `_NEWMUSICFILE6_0` 保护的后续录音，网页端不启用。

地图音乐按 `MAP.CPP` 的绘制顺序读取当前 M 窗口：同一格先检查地面再检查物件，采用最后一个有效 40–53 标记；窗口没有标记时保留原版 `map_bgm_no`（部分房间本来就继承区域音乐），不会误切到胜利音效。楼层切换期间完成的 DAT 会保存在进程缓存，返回该楼层时重新安装到当前自动地图绘制缓冲，不会留下灰色菱形。

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

这会重建登录/人物/字段/战斗 UI、角色帧、地图 200 视口，以及 `sa_2903.exe` 实际索引的 218 个（0–217）640×480 战斗视口；伴随目录中供后续客户端使用的 218/219 不会进入 2.5 资源包。

如果只更新了已有资源包的地图坐标清单，可运行
`python3 tools/extract-legacy-web-assets.py --map-origins-only`；该命令按每张 DAT 的行数重算等距投影原点，不会重新生成图片或音频。

`STONEAGE_TCP_UPSTREAM` 只在服务启动时读取，HTTP 客户端不能指定任意 TCP 地址。公网部署时只公开 8088（并放在 HTTPS 反向代理后），不要公开旧网关/SAAC 端口。

Linux Docker Compose 部署由根目录的 `docker-compose.yml` 管理。`web` 容器使用
control-plane 镜像内的 `assets/original`，并以只读方式挂载
`STONEAGE_CLIENT_DATA_ROOT` 提供 2.5 客户端的 `map/`、`data/bgm/`、`data/se/` 和 `data/pal/`；
它只通过 Compose 内网的 `gateway:9065` 转发协议，不会把 GMSV 或 SAAC 端口暴露给
浏览器。首次部署可直接运行 `./scripts/deploy.sh --init`，详见根目录
README 的 Linux Docker Compose 小节。

转发 API 是 `POST /api/sessions`（建立 TCP 并返回 `L\0` 握手）、`POST /api/sessions/:id/send`（JSON `{packet: base64}`）、`GET /api/sessions/:id/events`（按 TCP 顺序长轮询 base64 包）、`DELETE /api/sessions/:id`，另有只读资源 `GET /assets/*`、`GET /maps/*` 和 `GET /audio/*`。`send?close=1` 会在写入这一包后原子关闭桥接会话，保留给需要发送后关闭的兼容调用，页面卸载使用 DELETE 原地断线；即使写入失败也会清理会话。网页已经内置这套调用，不需要额外前端构建工具。

新建会话返回 `event_ack: true` 时，网页使用 `events?ack=N` 确认已处理的事件序号；响应包含 `acknowledged: true` 和逐条 `seq`。后端保留未确认事件，丢失响应后可重复读取，前端按序处理并跳过已确认事件。单次轮询取消只结束该 HTTP 请求；显式登出、TCP 关闭和空闲回收仍会结束会话。轮询临时失败最多重试三次，旧服务器不支持确认时保持原有断线恢复流程。发送游戏操作的 POST 不自动重试，以免重复执行。

前端数据处理异常会写入事件记录并保留连接；真正断线时，重连弹窗显示原因。浏览器控制台的 `StoneAgeWebClient.app.connectionDiagnostics` 保留最近 20 条异常摘要和堆栈，跨重连保留，用于区分网络错误和客户端异常。

## 测试

```bash
cd client/web
GOFLAGS=-mod=mod go test -race -count=1
node protocol_test.js
```

Go 测试使用本地 fake TCP upstream 覆盖握手、双向逐包转发、长轮询、关闭、错误握手、包大小、资源路由和并发会话上限；Node 用原版客户端捕获向量及 `LSSPROTO_CLI.H` 全量入口检查网页脚本、JEncode、Ringo、转义、base-62 和 schema。实际登录/地图/战斗仍以运行中的 GMSV/SAAC 返回为权威数据。

`protocol_test.js` 是需要完整保留客户端、旧服务端和资源源码树的本地协议审计。发布 CI 运行仓库内可独立复制的 Web 回归测试（战斗目标、重连、登录、NPC、资源、线路目录、精灵加载、Service Worker 缓存和世界帧），不会把该本地审计作为唯一入口。

`connection_retry_test.js` 同时运行 `event_recovery_test.js`，覆盖确认重放、请求取消、超时、有限重试、旧服务器兼容和前端异常隔离。

资源加载界面的“已读取资源”按当前页面实际读取的清单和图片响应体逐块累计，包括人物帧、兼容精灵清单及缓存返回的数据；压缩响应使用解压后大小，不等同于网络面板的传输流量。背景资源预取在首屏加载完成后开始。

Docker 部署通过 `STONEAGE_WEB_FORWARD_CLIENT_IP=true` 向支持该功能的认证网关发送 PROXY v1 客户端来源信息。Web 仅从 `STONEAGE_WEB_TRUSTED_PROXIES` 中的可信 HTTP 代理接受转发头；网关通过 `STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS=web` 信任 Web 服务的 Docker DNS 地址。直接连接原版 GMSV 或旧网关时保持默认关闭。
