# 地图包与行走性能

游戏内打开「系统 → 导入地图包」，下载常用四村或全部地图包，再选择下载得到的 `.samap` 文件并点击「开始导入」。浏览器会显示文件大小、可用空间及进度。取消、关闭页面或刷新后，重新选择同一个文件即可继续；已完成的文件会校验后复用。

v0.1.69 提供以下 Release 附件：

| 包 | 范围 | 文件数 | 下载大小 |
| --- | --- | ---: | ---: |
| `stoneage-common.samap` | 地图 1000、2000、3000、4000（四个出生村） | 1,957 | 7,866,223 字节 |
| `stoneage-full.samap` | 当前 Web 资源中的全部 995 张地图 | 7,893 | 89,239,954 字节 |

包包含 DAT/MAP 及地图使用的地面、建筑贴图。人物、宠物、音乐和游戏程序仍按需加载；地图包不等于离线游戏，登录和游戏操作仍需要连接服务器。缓存属于当前浏览器、当前网站；更换浏览器或清除网站数据后需要重新导入。页面会申请持久存储，浏览器仍可能因空间不足拒绝保存。

资源版本必须匹配。导入只写入当前版本的静态缓存，逐文件校验 SHA-256，不替换图片的 CDN 固定地址，不清除旧缓存。常用包和全量包的重叠文件不会重复下载。每个文件独立写入，取消后保留已完成部分；损坏文件不会被写入，修复或重新下载地图包后可继续。浏览器禁用缓存、没有 Service Worker 控制或存储空间不足时会提示原因。

## 制作资源包

使用现有 Web 资源，不重新解码 BIN，不修改原始客户端：

```sh
python3 tools/build-map-pack.py --help
python3 tools/build-map-pack.py \
  --publication build/_client-manifest.json \
  --floors 1000,2000,3000,4000 \
  --output build/map-packs/stoneage-common.samap
python3 tools/build-map-pack.py \
  --publication build/_client-manifest.json \
  --output build/map-packs/stoneage-full.samap
```

`_client-manifest.json` 必须来自当前资源发布。工具按发布工具相同的规则计算资源版本，并检查每个输入文件的大小和 SHA-256；本地文件与已发布资源不一致时直接失败。默认对象前缀是 `stoneage`，其他部署使用 `--prefix`。省略 `--floors` 会选择资源清单中的全部地图。仅本地测试可用 `--local-dev` 代替 `--publication`，这种包不能导入生产资源版本。

输出为一个 `.samap` 文件及同名 JSON 清单。格式是 `SAMAP001`、4 字节小端目录长度、UTF-8 JSON 目录、按目录排列的原始文件。PNG 已压缩，不再整体压缩，导入使用 File.slice，内存只需容纳一个文件。DAT 决定贴图依赖；同名原版 MAP 是探索颜色数据，不能当作贴图编号扫描。工具记录缺失贴图，发布前必须核对 `unmapped_bitmaps`；本次两个包均为零。

发版时将包、JSON 清单和包级 SHA-256 校验文件上传到同一个 GitHub Release，更新 `client/web/map-packs.json` 中的资源版本、文件大小及下载地址。下载入口只显示与当前资源版本相符的包。地图包没有改变现有 CDN 图片内容，因此不需要重复上传原来的贴图。

## 性能改动与验证

前台绘制由 RAF 驱动，多个网络/资源事件合并为一次绘制；后台及 RAF 暂停时仍由计时器推进移动。新地图窗口复用重叠单元与按原版对角线顺序绘制的地面分块，每批准备/绘制以 4 ms 为预算，完成前继续保留上一张完整画面。单次操作可能略超预算，因此它不是硬实时保证。

行走时不再扫描 localStorage 地图窗口；保存合并后等待移动空闲再执行。可见图片最多同时加载 6 个，行走方向附近预加载最多同时 2 个且队列有上限。DAT、精灵索引及调色板转换在 Worker 中处理；索引分批交回主线程，调色板使用 ImageBitmap，避免同步 PNG 编码和再次解码。

2026-09-21 在 Apple M4、Headless Chrome 152、1280×577 视口测试，使用实际地图 1000 和原版 Web 图片。以 v0.1.68 页面作为对照，固定 60 次单格动画及 37×37 地图窗口移动，记录实际画布提交间隔；测试使用本地模拟传输，不代表生产端到端延迟，也没有改变游戏行走速度。

| 场景 | v0.1.68 画布提交 P95 | 本次改动画布提交 P95 | 行走期间 >50 ms 长任务 |
| --- | ---: | ---: | ---: |
| 资源已缓存，正常 CPU | 25.0 ms | 17.4 ms | 两版均为 0 |
| 资源已缓存，CPU 降速 4 倍 | 25.0 ms | 18.7 ms | 两版均为 0 |
| CPU 降速 4 倍、150 ms 网络延迟、200 KB/s 下载 | 未单独测行走对照 | 19.6 ms | 本次为 0 |

冷启动对照采用上述 CPU/网络限制并清理测试浏览器的 HTTP 与网站缓存。旧版在页面初始任务后出现 89、108、157 ms 三次长任务，本次未再出现；两版仍分别有 209、212 ms 的页面初始化长任务。启动耗时包含测试工具操作间隔，不能据此比较加载速度。测试不能承诺所有手机、地图和网络下都不掉帧。

实际浏览器还验证了：常用包取消到第 30 个文件后续传；整包导入；全量包复用常用包全部文件；断网时仍通过固定 URL 读取地图 3000/4000 的 DAT 和 PNG；调色板转换像素值；村庄地面及建筑显示。单元回归覆盖重叠复用、地面/PARTS 顺序、过期窗口、调色板失效、RAF 后台回退、路径和版本校验、损坏包、重启续传以及预加载并发上限。既有协议、碰撞/路径、地图边界回归和 Web Go 测试一并执行。

```sh
node --test client/web/walking_scheduler_test.js client/web/map_layers_test.js client/web/map_pack_test.js
node client/web/sw_cache_test.js
PYTHONDONTWRITEBYTECODE=1 python3 tools/build-map-pack_test.py
go test -mod=readonly ./client/web
go vet -mod=readonly ./client/web
```

本次完整资源场景和测量产物位于本地 `build/walk-optimization/`，不随公开源码提交。
