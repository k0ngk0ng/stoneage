# 数据存储与账号库

## 当前方案

StoneAge Revival 的网页登录和游戏登录使用独立的 SQLite 认证库，默认路径为
`runtime/stoneage-auth.db`。库中只保存：

- 游戏账号、Argon2id 密码哈希和启用/禁用状态；
- 管理员账号、会话和失败锁定状态；
- 登录尝试与后台操作审计日志。

SQLite 启用 WAL、外键、`synchronous=FULL` 和 busy timeout。数据库文件会收紧为
`0600`，运行目录应只允许服务账号访问。密码不会写入日志，也不会回传网页。

SAAC 仍然负责旧游戏数据：人物、邮件、家族、锁文件和其他平面文件位于
`runtime/legacy-server/saac/`。认证库不替代这些角色数据，因此备份和迁移时要
同时保存 SQLite 文件及 SAAC 目录；备份前先停止游戏服务，避免复制到半写入文件。

## 初次初始化

先创建管理员，再启动后台：

```bash
./bin/stoneage-admin create-admin \
  -db runtime/stoneage-auth.db \
  -username admin -password-stdin
```

密码至少 8 位。Linux 发布包也可以在首次启动后台时设置
`STONEAGE_ADMIN_USER` / `STONEAGE_ADMIN_PASSWORD`，或临时设置
`STONEAGE_ADMIN_SETUP_TOKEN` 通过 `/setup` 创建管理员；管理员建立后 `/setup`
会自动关闭。

旧 SAAC 中已有账号可导入为禁用账号，再在网页中逐个设置密码：

```bash
./bin/stoneage-admin import-legacy \
  -db runtime/stoneage-auth.db \
  -char-dir runtime/legacy-server/saac/char
```

不提供 `-default-password` 时，导入账号不会获得访问权限。

## 备份与迁移

停止 `stop-server.sh` 和 `stop-admin.sh` 后，完整复制：

```text
runtime/stoneage-auth.db
runtime/stoneage-auth.db-wal（若存在）
runtime/stoneage-auth.db-shm（若存在）
runtime/legacy-server/saac/
```

恢复时保留文件权限，先启动后台让 SQLite migration 完成，再启动游戏网关。不要
直接删除 WAL/SHM 文件；如果需要单文件归档，应使用 SQLite 在线备份工具或在
服务完全停止后复制主库。

MySQL 8 不再是默认依赖。只有在未来拆分成多实例、需要集中式高并发账号服务时
才考虑迁移；迁移必须先完成 schema、备份恢复、双写和旧客户端兼容性演练，密码
仍只保存 Argon2id 派生结果。
