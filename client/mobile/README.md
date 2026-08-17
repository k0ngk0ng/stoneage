# StoneAge 手机端

这是一个竖屏 2D Godot 4 原型。它不复用 1.82 客户端源码，只读取已经烘焙进仓库的 PNG 与 `manifest.json`，因此构建手机包时不需要移动硬盘上的 exe 或 764 MB 的 `real_15.bin`。视口会随不同宽高比的竖屏设备扩展，避免在手机两侧留下黑边。

当前在线切片包含：

- 地图 200、1006：根据服务器 floor 自动切换与 PC DAT 对应的等距地图；
- 登录页：通过本地 StoneAge 网关真实验证账号，再进入角色；
- NPC 16065：接取、查看和完成一个任务；
- 按 PC 同样的服务器遇敌规则随机遇敌；
- 竖屏回合制战斗：攻击、防御、逃跑；
- 与 PC 同一 TK/WN 通道的聊天：服务器欢迎语、NPC/任务消息和玩家消息进入同一记录，可展开/收起，折叠时只显示未读数；连接握手、地图同步等内部状态不会刷屏；
- 2.5 客户端中的玩家、NPC、怪物、地图和战斗地面贴图。
- 随包 CJK UI 字体，避免 Android/iOS 因系统字体差异显示方框。

## 打开原型

安装 Godot 4 后打开本目录，运行 `main.tscn`。项目固定为 390×844 竖屏视口，Godot 的 Android/iOS 导出模板可以直接使用同一套场景和脚本。仓库同时保留本机验收用的 Android debug APK；重新导出仍需要对应的 Godot 导出模板、Android SDK/JDK 或 Xcode。

首次验收路径：在登录页输入本机演示账号后进入地图，点击“对话 NPC”接任务，点击方向键移动；遇敌由服务器按 PC 同样的地图事件/遇敌率随机触发。战斗中用“攻击”三次，回到世界后再次与 NPC 对话即可看到任务完成状态。

当前已在本机 Android 模拟器验证并导出 `builds/stoneage-mobile-online-debug.apk`。地图楼层、坐标、角色快照和行走确认均来自服务器；手机只使用从 PC BIN 提取的对应楼层贴图。

也可以用 Godot 的无窗口验收脚本检查完整状态机：

```bash
godot --headless --path . --script tests/mvp_smoke.gd
```

如果是首次在命令行运行、Godot 尚未导入 PNG/字体，可先执行一次 `godot --headless --path . --editor --quit-after 15`，让编辑器完成资源导入。

## 重新提取贴图

移动硬盘资源只在开发时作为输入：

```bash
python3 tools/asset_cooker.py \
  --client-root ../../runtime/legacy-client \
  --output assets/generated \
  --palette 1
```

`--palette 1` 是实机截图对应的普通环境色表；`0`、`5`、`14` 等是客户端内置的其他色调，可用于后续昼夜/地图效果。提取器完成以下工作：

1. 解析 80 字节一条的 `adrn_15.bin`；
2. 从 `real_15.bin` 读取 `RD` 记录并解码自定义 RLE；
3. 将 RD 的 Windows bottom-up 行序翻成手机使用的 top-down 行序，避免人物上下倒置；
4. 读取 `spradrn_5.bin`/`spr_4.bin` 的动画帧；
5. 应用 `Palet_*.sap` 的 BGR 调色板和索引 0 透明色；
6. 解析 `.MAP`、`.DAT`、`battleMap/*.sab`，合成等距地图和战斗地面；
7. 输出普通 RGBA PNG、偏移/尺寸/来源哈希及地图层数据到 `assets/generated/`。

`.DAT` 的第一层是可绘制的 object bitmap；第二层是行走/高度等逻辑 image，第三层是事件标志，后两层不会被误当成贴图重复铺满地图。

## 在线协议边界

登录、角色进入、地图同步、NPC 对话、行走和聊天都经过现有 Go 网关；行走成功后由服务器按 PC 的地图遇敌率随机发送 `EN`，手机只接收这个通知，不发送 `EN`，也没有“主动遇敌”按钮。`MC/C/CA/XYD` 是地图和角色状态的权威来源，客户端不再本地预测坐标。手机请求与 PC 绘制一致的 37×37 `M` 地图窗口；`MC` 的 27×27 只用于校验/可见性握手。聊天只接收 PC `LOGIN.H` 中的消息型 `WN`（0/1/10/11），不会把商店/选择菜单的内部序列化数据显示出来。
