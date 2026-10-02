package sacli

import (
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

var QueryHelp = func() string {
	var out strings.Builder
	out.WriteString("usage: sactl query <状态代码>\n\n向游戏服务器请求刷新一类状态（只读，大小写敏感，需要先登录并进入角色）。\n可用代码：\n")
	for _, group := range aigame.StatusQueryGroups() {
		fmt.Fprintf(&out, "  %-10s %s\n", group.Usage, group.Description)
	}
	out.WriteString(`
槽位从 0 开始；k/w 是宠物槽位，j 是装备槽位，n 是队伍槽位。
v0.2.8 起物品、装备和宠物编号会自动获取/刷新，通常无需手动 query AI。
战斗中可查询 AI（含合法关联请求）、BTIME、BTRULES；其他代码需要在世界场景使用。

示例：
  sactl query i            # 请求刷新装备和背包
  sactl query k0           # 请求刷新宠物槽位 0 的属性
  sactl query w0           # 请求刷新宠物槽位 0 的技能
  sactl status             # 查看已收到的状态摘要
  sactl --json observe     # 查看已解析的结构化状态
  sactl log 10             # 查看最近返回事件（如称号 T 包）

请求成功只表示已发送，不保证响应已经返回，也不保证当前场景有对应数据。
query 的即时 data 可能仍是旧快照；稍后用 status/observe 核对。
wait 等待任意事件，不能将一次唤醒当作本次查询完成。

高级：AI:<16位小写十六进制请求ID> 可关联扩展状态响应，通常直接用 AI 即可。
帮助无需登录：sactl query、sactl query --help、sactl query help。
`)
	return out.String()
}()

func QueryCodes() []string { return aigame.StatusQueryCodes() }

func ValidateQueryCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("query 需要一个状态代码；运行 sactl query --help 查看用途和示例")
	}
	if !aigame.ValidStatusRequest(args[0]) {
		return fmt.Errorf("不支持状态代码 %q；可用：c i k0..k4 w0..w4 j0..j4 n0..n4 t AI BTIME BTRULES。运行 sactl query --help 查看说明", args[0])
	}
	return nil
}
