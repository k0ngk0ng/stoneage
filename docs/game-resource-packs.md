# 游戏资源 ZIP

游戏内「系统 → 导入资源」支持百科「游戏资源下载」（`/wiki/resources`）提供的 ZIP，无需解压。包含当前发布清单中的全部人物、宠物、骑乘、地图、特效、界面、音乐、音效与资源索引，不包含账号、存档或程序配置。旧 `.samap` 仍可导入，但必须匹配当前资源版本。

下载入口从 CDN 的 `_client-version.json` 获取资源版本，再读取 `packs/<revision>/resource-packs.json`。ZIP 与目录在资源发布时生成，不把下载地址绑定到程序版本，避免程序更新后仍指向过期包。尚未发布匹配包时显示提示，不提供旧版下载链接。

## 生成与发布

使用当前 CDN 发布清单和与之匹配的本地资源副本：

```sh
python3 tools/build-resource-pack.py \
  --publication build/resource-packs/_client-manifest.json \
  --assets client/web/assets/original \
  --client-data runtime/legacy-client \
  --output build/resource-packs/stoneage-resources.zip
```

默认对象前缀为 `stoneage`，可用 `--prefix` 设置。工具仅收录清单列出的公共资源，验证每个文件大小和 SHA-256；数据或版本不符直接失败。输出 ZIP 及同目录 `resource-packs.json`。

通过已发布版本的部署入口上传：

```sh
bin/stoneage sync-assets --resource-packs /path/to/resource-packs
```

上传器先核对当前发布清单、ZIP 校验和、文件清单和版本，再流式上传 ZIP，最后更新下载目录。不重新上传图片、不改变现有资源版本，不启动或停止游戏服务。此选项独立使用，不与 `--web-only`、`--map-packs` 或 `--dry-run` 同用。资源更新后应重新生成匹配的完整包。

## 浏览器导入

标准 ZIP 使用 STORE，无需在浏览器解压大文件。ZIP64 支持超过 65,535 个资源文件。浏览器读取清单并逐文件校验，单文件读取上限 256 MiB；完整包不会一次装入内存。ZIP 中的内部清单用于校验，用户不需要操作它。

资源按原来的 CDN 固定 URL 写入 Cache Storage。当前及旧版缓存中的文件只有 SHA-256 匹配才复用，不清空旧缓存。取消或刷新后重新选择同一包可继续；配额不足时保留已完成文件并提示错误。已导入且版本匹配的 JSON 索引也直接复用，不再请求另一份压缩索引。

缓存属于当前浏览器和游戏网站。导入不是离线游戏，登录与游戏操作仍需联网。ZIP 大小与浏览器实际占用不同，缓存条目较多时需要额外存储空间。
