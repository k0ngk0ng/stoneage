# 已验证的白虎骑乘

## 固定来源与映射

资源根目录为 `vendor/client-resources/xinyanglao-2026-09-14/`，来源和 SHA-256 见其 `README.md`、`manifest.json`。资源组为：

```text
path/ride-3-1.82-110000-110105/
  real.bin
  spr.bin
  spradrn.bin
  adrn.decoded.bin
  adrn-decoding-provenance.json
```

`adrn.decoded.bin` 的全部 27,840 条记录已与同组 REAL 验证一致，直接复用。保留解码来源记录和根目录 `stoneage.exe`；不重复查找动画、不猜偏移、不把启动配置参数写入公开文件。

当前脚本固定导入源图号 `110048–110059`，映射为目标 `104025–104036`，图片命名空间为 `bitmaps/white-tiger-8.5`。其他资源或不同目标映射需要单独核查并实现，现有入口没有通用图号参数。

## 生成候选包

从仓库根目录执行，输出目录必须尚不存在；已有输出时换用新的 `build/` 子目录。

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/import-white-tiger-assets.py \
  --source vendor/client-resources/xinyanglao-2026-09-14/path/ride-3-1.82-110000-110105 \
  --adrn vendor/client-resources/xinyanglao-2026-09-14/path/ride-3-1.82-110000-110105/adrn.decoded.bin \
  --output build/white-tiger-import-review
```

导入器检查全部 ADRN/RD 记录后才写出图片；校验失败应排查输入组和来源记录，不放宽断言或用猜测定位绕过错误。

## 白虎专用校验

- 12 个角色，每个角色有 8 个方向，动作集合为 `0/1/2/3/4/10/12`。
- 每角色 256 个独立位图、576 个帧引用。
- 动作对应 `(帧数, 原生 tick)`：`0=(9,6)`、`1=(2,3)`、`2=(8,9)`、`3=(33,6)`、`4=(9,3)`、`10=(2,3)`、`12=(9,6)`。
- 动作 0 和 12 的事件为零起算帧 3 上的 `10101`、帧 6 上的 `10001`，其他动作无该类事件。
- 真彩 RD 为 zlib BGRA、bottom-up 行序；脚本转换为 RGBA PNG，保留 alpha 及 ADRN 定位。

输出包括图片、完整 `sprites.json`、仅包含动作 3/4 的 `field-bootstrap-sprites.json` 和 `provenance.json`（源哈希、映射、独立位图数）。脚本不修改应用清单；接入前检查现有目标图号，再根据授权范围合并候选包并做视觉验证。
