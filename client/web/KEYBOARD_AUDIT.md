# 2.5 客户端本地按键审计

对照 `vendor/upstream/code_sa_client/SYSTEM/MAIN.CPP`、`CHAT.CPP`、`MENU.CPP`、`FIELD.CPP`、`GAMEMAIN.CPP` 的正式（非 `_DEBUG`）路径，网页端的本地控制共 39 项，当前 **39/39 已实现，0 项遗漏**。其中 `Shift+Backspace` 是 8.5 `_SA_VERSION_25` 保留下来的当前输入框清空变体，作为 `Backspace` 的本地编辑分支实现，不另重复计数：

| 模块 | 原版按键 | 网页实现 |
| --- | --- | --- |
| 聊天缓冲区（14） | Enter、Backspace、Delete、Tab、↑、↓、F1–F8 | `handleChatInputKeydown`、文档级键盘边界 |
| 任务栏/窗口（9） | Esc、Ctrl+M/S/P/I/E/A、Ctrl+Q/W | `fieldMenuShortcut`、Escape 分支 |
| 现场动作（13） | Ctrl+0、Ctrl+^、Ctrl+9、Ctrl+7、Ctrl+8、Ctrl+1、Ctrl+2、Ctrl+4、Ctrl+5、Ctrl+6、Ctrl+-、Ctrl+3、Ctrl+\\ | `actionShortcuts` |
| 画面控制（3） | F5、Alt+Enter、F12 | 禁止刷新、Fullscreen API、640×480 截图 |

## 清空聊天的按键

原版没有 `/clear` 或其他文字指令。`MAIN.CPP` 的 `VK_DELETE` 直接调用 `ClearChatBuffer()`，只清空屏幕上最多 20 行聊天，不删除发送历史，也不删除当前未发送草稿。网页对应 `clearChatBuffer()`，同时清空地图和战斗聊天层。

浏览器按键兼容如下：

* Windows/Linux 的 `Delete`、前向删除（`code=Delete`）都会清空聊天。
* macOS 键盘上标为 Delete 的退格键仍保留原版 Backspace 删除输入文字；使用 `Cmd+Backspace` 作为清空聊天的显式兼容别名。
* `Shift+Backspace` 清空当前获得焦点的输入缓冲区（聊天、邮件、称号/对话等），对应 `_SA_VERSION_25` 的 `WM_KEYDOWN/VK_BACK` 分支；它只改本地编辑框，不发送 `TK` 或其他服务端封包。

动作快捷键的 `Ctrl+Yen` 也做了浏览器键盘布局兼容：`\`、`IntlYen`、`¥/₩` 均映射到原版第 13 个动作；小键盘减号（`NumpadSubtract`）映射到原版 `Ctrl+-`。

## 有意不迁移的路径

`F11`、Home、End、Insert、F10、Shift、PrintScreen 等只出现在调试/测试代码（`_DEBUG`、`TESTVIEW.CPP` 或 `#if 0`），不属于正式游戏功能。正式版 `F9` 只是切换 DirectDraw 低分辨率；网页始终使用 640×480 逻辑画布再按 CSS 缩放，因此没有独立的 F9 状态可切换。

普通聊天、方括号内容和除清屏别名外的 `/` 开头内容全部按原版包装为 `TK(P|text)` 交给 2.5 GMSV；网页只在本地拦截明确列出的清屏别名（带斜杠和不带斜杠的严格同义词），并兼容全角斜杠/字母，不会把 8.5 服务端命令误带进 2.5。

## 文字指令与服务端指令

这里要区分两类东西：

* **清空聊天不是原版文字指令。** 2.5 的 `MAIN.CPP` 没有 `/clear`（也没有“清空聊天记录”字符串），只有 `VK_DELETE -> ClearChatBuffer()`。网页仍按原版支持 `Delete`（macOS 的 `Cmd+Backspace` 兼容别名），另外提供带斜杠和不带斜杠的严格同义词作为触摸设备便利别名；这些别名不会进入 `TK`，也不会改变服务端聊天历史。`Shift+Backspace` 则只清空当前输入框草稿，不清屏。
* **聊天魔法是服务端功能。** `server/legacy/source/2.5/gmsv/char/char_talk.c` 按当前宏预处理有 183 个表项、182 个名称（`dp` 重复一项）和 94 个处理函数；其中 `battlein`、`battleout`、`debug`、`crash`、`shutdown` 五个内部处理器各有中英文别名，共 10 个内部表项，排除后为 173 个表项、172 个别名和 89 个处理函数。它们全部是 GM/管理权限指令，包括 `[help]`、`[warp ...]`、`[reset ...]` 等。它们由 GMSV 按权限解析，网页只发送原文，不在浏览器复制一份解析器；因此不存在“网页漏迁移”这一层。8.5 的 `send <raw packet>`、`.光环`（宏已关闭）和 `/叠加` 也不会被当成 2.5 普通玩家指令带入。
* 编译条件打开时的 `/go`、`/gg`、`pub back door ...`、`vip ...` 和聊天触发动作同样在 GMSV 处理，仍沿用同一 `TK(P|text)` 通道。

截至本次审计，正式 2.5 客户端的 39 项本地键盘功能为 **39/39，0 项遗漏**。有意不带入网页的是 `_DEBUG`/`TESTVIEW` 调试命令（如 `[battlein]`、`[battleout]`、`movescreen`、`playnpc`）以及 2.5 协议没有的 8.5 扩展；这不是可玩的聊天指令。
