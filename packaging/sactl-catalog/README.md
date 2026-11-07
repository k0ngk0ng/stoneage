# sactl 分发定义

当前仓库同时维护 `sactl` 源码、正式 Release、`Formula/sactl.rb` 和 `bucket/sactl.json`。命令仍为 `sactl`；客户端、配套 skill、CDN 安装入口与其他 Release 产物继续使用现有发布链。

## 安装

```sh
brew tap k0ngk0ng/stoneage https://github.com/k0ngk0ng/stoneage
brew install k0ngk0ng/stoneage/sactl
```

Windows：

```powershell
scoop bucket add stoneage https://github.com/k0ngk0ng/stoneage
scoop install stoneage/sactl
```

## 同步与验证

`sync-sactl-catalog.yml` 在正式 Release 发布后由 `packages` 作业调用，验证该版本的 GitHub 资产摘要与独立下载的 `SHA256SUMS`，同时验证校验文件自身的 GitHub 摘要。随后分别检查 macOS arm64/amd64、Linux arm64/amd64 和 Windows amd64 的真实归档及 Homebrew/Scoop 安装，全部通过后才提交同仓库的包定义更新。每小时的定时任务和手动触发也使用同一验证流程。

```sh
python3 scripts/test-sactl-catalog.py
python3 scripts/sync-sactl-catalog.py --offline --check
python3 scripts/test-sactl-archive.py
```

`--version v0.2.18` 可验证指定的已发布稳定版本。锁文件与模板位于本目录；缓存、解包目录和 JSON 证据位于 `.cache/`。本机归档测试只在仓库内执行程序；Homebrew/Scoop 安装测试限定在一次性 GitHub Actions runner 上运行。

首次迁移保留原包仓库发布的 `sactl 0.2.18` 定义与哈希，来源见 `ORIGINS.json`。Release 工作流已停用向旧仓库推送的任务，也不再引用原跨仓库推送凭据；现有 secret 没有删除或轮换。旧手工发布辅助脚本保留用于追溯，自动化发布统一通过新工作流完成。
