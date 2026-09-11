# 运维访问说明

- 生产 StoneAge 主机：`root@192.0.2.10`
- SSH 私钥仅引用本机路径 `~/.ssh/id_ed25519_sa`，不得复制私钥内容到仓库。
- 示例：`ssh -o IdentitiesOnly=yes -i ~/.ssh/id_ed25519_sa root@192.0.2.10`
- 排障优先执行只读日志和容器状态检查；重启、部署或配置修改须取得用户当次明确授权。

# 发版原则

- 必须先将代码、配置变更和版本 tag 推送到 GitHub，确认对应 GitHub Actions 检查及镜像发布成功，再在服务器上修改部署配置、拉取该 tag 的镜像并部署。
- 不得先在生产服务器临时修改代码或配置，再补推 GitHub。生产部署必须对应已发布的 GitHub 版本。
- 服务器发布使用项目入口 `bin/stoneage`：`check` 校验、`pull` 拉镜像、`deploy` 部署、`status` 查看状态。镜像已拉取时使用 `bin/stoneage deploy --no-image-update`；不要绕过入口直接用 Docker Compose 发布，以免漏掉项目仓库认证和部署检查。
- 部署顺序固定为：服务运行期间先 `bin/stoneage check`、`bin/stoneage pull`，拉取成功后执行 `bin/stoneage deploy --no-image-update`，由入口完成旧容器停止与新容器启动，最后 `bin/stoneage status`。不得提前手动停服等待下载。
- 只更新程序镜像、未改资源时不做备份，不得为备份延长停服时间；资源变更的备份需求另行明确。保留旧版本镜像以便回退。
- 严格使用 `bin/stoneage`，禁止临时拼装 SSH/脚本编排停服、备份、部署或回滚。部署流程需要调整时，只能在仓库的 `bin/stoneage` 入口修改、提交并发布后使用，不得在生产现场另造流程。
- 部署后核验实际镜像版本、容器健康状态与相关功能。
