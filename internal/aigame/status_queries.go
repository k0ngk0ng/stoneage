package aigame

// StatusQueryGroup describes read-only S requests implemented by the current
// server. Indexed categories require a slot; bare w/j/n and g have no reply.
type StatusQueryGroup struct {
	Usage       string
	Description string
	Codes       []string
}

func StatusQueryGroups() []StatusQueryGroup {
	slots := func(prefix string) []string {
		return []string{prefix + "0", prefix + "1", prefix + "2", prefix + "3", prefix + "4"}
	}
	return []StatusQueryGroup{
		{"c", "当前位置：地图编号、地图尺寸、服务器坐标", []string{"c"}},
		{"i", "全部装备和背包：名称、图号、说明等基础数据", []string{"i"}},
		{"k0..k4", "指定宠物槽位的属性；例如 k0 是槽位 0 的宠物", slots("k")},
		{"w0..w4", "指定宠物槽位的技能；例如 w0 是槽位 0 的宠物技能", slots("w")},
		{"j0..j4", "指定装备槽位的精灵魔法、耗气和目标类型；无魔法时返回空项", slots("j")},
		{"n0..n4", "指定队伍槽位的成员状态；不是查询附近所有角色", slots("n")},
		{"t", "称号列表；当前通过 log 查看返回事件，尚未投影到 observe", []string{"t"}},
		{"AI", "扩展自身状态：物品/装备模板编号、宠物稳定编号、事件标记等；不调用大模型", []string{"AI"}},
		{"BTIME", "竞技场战斗回合时钟；不是游戏时间，非竞技场战斗时可能无有效截止时间", []string{"BTIME"}},
		{"BTRULES", "战斗规则摘要与引擎平台，用于本地 learned 模型兼容检查；需要支持此扩展的服务器", []string{"BTRULES"}},
	}
}

func StatusQueryCodes() []string {
	var codes []string
	for _, group := range StatusQueryGroups() {
		codes = append(codes, group.Codes...)
	}
	return codes
}

// ValidStatusRequest is shared by CLI hints and protocol validation.
func ValidStatusRequest(value string) bool { return validStatusRequest(value) }
