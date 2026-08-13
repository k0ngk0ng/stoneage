# 已验证功能与已知限制

验证环境：macOS 26.5.2、Apple Silicon、Wine 11、cnc-ddraw 7.1.0.0，日期
2026-08-13。

## 已实机验证

- 2.5 客户端启动、繁体风格界面和 CP936/GBK 中文显示
- 账号登录、服务器组/线路、人物列表、人物登入
- 地图 1006、地图移动、心跳及断线保存
- 两个真实客户端并发、彼此可见、双向聊天
- 宠物面板和宠物状态切换
- 物品面板及服务端添加物品后显示
- 完整战斗：遭遇、角色与三只敌人、血条、攻击指令、回合结算、返回地图
- Go 网关的登录竞态延迟与 2.5 `BC` 战斗字段兼容转换

## 尚未宣称完成

- Win11 实机回归尚未完成；发布包是可执行候选版
- 真正 NPC 对话窗口的按钮回复尚未形成稳定的低频端到端证据；登录公告的
  `WN` 窗口和客户端确认包已验证
- 公网安全账号系统、TLS、限流、自动备份尚未完成；请仅在本机、可信 LAN
  或 Tailscale/WireGuard 私网使用
- 目前仍依赖历史 C 版 GMSV/SAAC；Go 网关是现代边界，但服务端尚未整体重写

战斗证据位于 `runtime/screenshots/battle-bc-fix-char-scan-result.png` 与
`runtime/screenshots/battle-bc-fix-attack-selected.png`。

