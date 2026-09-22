package gamewiki

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// EnemyGymSkill, enabled by the checked-in 2.5 version.h. Missing table
// definitions remain visible rather than being described as working skills.
var gymSkills = []int{3, 10, 11, 12, 30, 31, 40, 41, 50, 51, 52, 60, 61, 80, 90, 100, 110, 150, 151, 152, 210, 503, 504, 540, 541, 542, 543, 544, 545, 546, 548, 575, 600, 601, 628, 635}
var randomSkills = map[int][]int{
	1: {3, 10, 11, 12, 30, 31, 40, 41, 50, 51, 52, 60, 61, 80, 90, 110, 120, 150, 210, 303, 309, 315, 321, 503, 504, 506, 507, 541, 542, 543, 544, 545, 546, 547, 575, 579, 580, 606, 613, 615},
	2: {12, 13, 20, 41, 52, 152, 210, 306, 312, 318, 324, 325, 500, 501, 502, 505, 508, 541, 542, 543, 544, 545, 546, 547, 576, 580, 594, 606, 613, 616},
}

func randomKind(id int) int {
	for _, r := range [][2]int{{564, 580}, {739, 750}, {895, 906}} {
		if id >= r[0] && id <= r[1] {
			return 1
		}
	}
	for _, r := range [][2]int{{655, 720}, {859, 894}, {907, 940}} {
		if id >= r[0] && id <= r[1] {
			return 2
		}
	}
	return 0
}
func (b *builder) params(ref aiknowledge.NPCEnemyRef) (map[string][]string, string) {
	if strings.HasPrefix(ref.Argument, "file:") {
		name := path.Clean("npc/" + strings.TrimPrefix(ref.Argument, "file:"))
		if f, ok := b.files[name]; ok {
			return f.Fields, name
		}
		return nil, name
	}
	m := map[string][]string{}
	for _, s := range strings.Split(ref.Argument, "|") {
		k, v, ok := strings.Cut(s, ":")
		if ok {
			m[strings.ToLower(strings.TrimSpace(k))] = append(m[strings.ToLower(strings.TrimSpace(k))], strings.TrimSpace(v))
		}
	}
	return m, "内联参数"
}
func first(m map[string][]string, k string) string {
	if len(m[k]) > 0 {
		return m[k][0]
	}
	return ""
}
func paramTable(m map[string][]string) Table {
	t := Table{Title: "对白、条件与事件参数", Columns: []string{"参数", "内容"}}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Rows = append(t.Rows, []string{k, strings.Join(m[k], "\n")})
	}
	return t
}
func (b *builder) combatRows(e *Entry, enemyIDs []int, gym, sktype int, title string) {
	t := Table{Title: title, Columns: []string{"敌人", "敌人ID / 模板ID", "等级", "基础HP", "基础攻 / 防 / 敏", "地 / 水 / 火 / 风", "技能/候选技能", "规则"}}
	for _, id := range enemyIDs {
		n, ok := b.enemies[id]
		if !ok {
			e.Notes = append(e.Notes, fmt.Sprintf("引用敌人%d不存在", id))
			continue
		}
		p, ok := b.bases[n.TemplateID]
		if !ok {
			e.Notes = append(e.Notes, fmt.Sprintf("敌人%d的模板缺失", id))
			continue
		}
		levels := n.Levels
		if gym > 0 {
			levels = aiknowledge.Range{Min: gym, Max: gym}
		}
		hp, a, d, s, valid := NativeStats(p, levels)
		hpText, ads := "待核对", "待核对"
		if valid {
			hpText = rangeText(hp)
			ads = rangeText(a) + " / " + rangeText(d) + " / " + rangeText(s)
		}
		skills := append([]int(nil), p.PetSkillSlots[:]...)
		rule := "模板技能；施放由战斗策略决定"
		if r := randomKind(id); r > 0 {
			skills = append([]int(nil), gymSkills...)
			if r == 1 {
				skills = append(skills, 1)
				rule = "前2槽随机技能或普通攻击；外观/元素/装备随机"
			} else {
				rule = "前2槽随机技能"
			}
			skills = append(skills, p.PetSkillSlots[2:]...)
		}
		if pool, ok := randomSkills[sktype]; ok {
			skills = nil
			for _, i := range pool {
				if _, exists := b.skills[i]; !exists {
					i = 1
				}
				skills = append(skills, i)
			}
			rule = "全部7槽随机抽取，缺失定义回退攻击"
		}
		names, missing := b.skillNames(skills)
		e.Notes = append(e.Notes, missing...)
		if len(t.Rows) == 0 && len(e.Tables) == 0 {
			e.HP = hpText
			e.Level = rangeText(levels)
			e.Skills = names
		}
		t.Rows = append(t.Rows, []string{p.Name, fmt.Sprintf("%d / %d", id, p.TemplateID), rangeText(levels), hpText, ads, fmt.Sprint(p.Elements), names, rule})
		e.link("enemy", id, p.Name+"（出战配置）")
		for _, i := range skills {
			if s, ok := b.skills[i]; ok {
				e.link("skill", i, s.Name)
			}
		}
		if en := b.c.Entries[key("enemy", id)]; en != nil {
			en.link(e.Kind, strings.TrimPrefix(e.Key, e.Kind+":"), e.Name)
		}
	}
	if len(t.Rows) > 0 {
		e.Tables = append(e.Tables, t)
	}
}
func (b *builder) addNPCs() {
	templates := map[string]aiknowledge.NPCTemplate{}
	for _, t := range b.k.NPC.Templates {
		templates[t.Name] = t
	}
	used := map[string]bool{}
	for _, c := range b.k.NPC.Creates {
		for ri, ref := range c.Enemies {
			t, ok := templates[ref.Template]
			kind := "npc"
			if t.FunctionSet == "NPCEnemy" {
				kind = "battle_npc"
			}
			used[ref.Template] = true
			name := c.Name
			if name == "" {
				name = t.DisplayName
			}
			if name == "" {
				name = ref.Template
			}
			id := fmt.Sprintf("%s@%d-%d", c.Source.Path, c.Source.Line, ri)
			e := b.add(kind, id, name, t.FunctionSet)
			if strings.Contains(name, "�") {
				e.field("原始名称", name)
				e.Name = t.DisplayName
				if e.Name == "" || strings.Contains(e.Name, "�") {
					e.Name = ref.Template
				}
				e.Notes = append(e.Notes, "原始NPC名称存在编码损坏，标题暂使用模板名称；请按位置和敌人编号辨认。")
			}
			e.Location = b.mapName(c.Floor) + " " + point(c.Born)
			e.field("位置", e.Location)
			e.field("出生范围", point(c.Born))
			e.field("移动范围", point(c.Move))
			e.field("功能", t.FunctionSet)
			e.field("创建数量", c.SpawnCount)
			e.field("图号", c.Graphic)
			e.Sources = []string{source(c.Source), source(t.Source)}
			if !ok {
				e.Notes = append(e.Notes, "NPC模板不存在")
			}
			if m := b.maps[c.Floor]; m != nil {
				e.link("map", c.Floor, m.Name)
				m.link(kind, id, e.Name+" "+point(c.Born))
			} else {
				e.Notes = append(e.Notes, "对应地图文件不存在")
			}
			params, src := b.params(ref)
			if src != "内联参数" {
				e.Sources = append(e.Sources, src)
			}
			if kind == "battle_npc" {
				gym, sktype := atoi(first(params, "gym")), atoi(first(params, "sktype"))
				enemyIDs := ids(first(params, "enemyno"))
				if len(enemyIDs) == 0 {
					e.Notes = append(e.Notes, "enemyno缺失，无法正常初始化战斗")
				}
				e.Group = "其他战斗NPC"
				if name == "宝物袋" {
					e.Group = "宝物袋"
				}
				limit := 10
				title := "战斗阵容（首项及随从）"
				if gym > 0 {
					e.Group = "道场弟子"
					limit = 64
					title = "主对手候选（每次随机1名）"
					e.Description = "主对手及宠物候选各随机1名，等级由道场层数决定"
				} else {
					e.Description = "完整阵容见详情；列表血量对应首个配置敌人"
				}
				if len(enemyIDs) > limit {
					enemyIDs = enemyIDs[:limit]
				}
				b.combatRows(e, enemyIDs, gym, sktype, title)
				if gym > 0 {
					pets := ids(first(params, "enemypetno"))
					if len(pets) > 64 {
						pets = pets[:64]
					}
					b.combatRows(e, pets, gym, sktype, "宠物候选（每次随机1只）")
					e.HP = "随候选变化，详见阵容"
					e.Skills = "道场随机技能池；查看完整候选"
				}
				trigger := map[int]string{0: "接触", 1: "对话", 2: "接触或对话"}[atoi(first(params, "entype"))]
				e.field("触发方式", trigger)
				e.field("同NPC同时仅一场战斗", first(params, "onebattle") == "1")
				if first(params, "dieact") == "1" {
					e.field("胜利后", "传送或事件分支，见事件参数")
				} else {
					wait := first(params, "time")
					if wait == "" {
						wait = "120"
					}
					e.field("胜利后", "NPC暂时消失，约"+wait+"秒后检查恢复")
				}
				big := 0
				for _, id := range enemyIDs {
					if b.bases[b.enemies[id].TemplateID].Size == 1 {
						big++
					}
				}
				if gym == 0 && big > 5 {
					e.Notes = append(e.Notes, "大型敌人超过5只，原生入场逻辑会筛除超额单位")
				}
			}
			if len(params) > 0 {
				e.Tables = append(e.Tables, paramTable(params))
			} else if ref.Argument != "" {
				e.Notes = append(e.Notes, "参数文件未被解析，不能据此确认交互内容")
			}
			e.Notes = unique(e.Notes)
		}
	}
	for name, t := range templates {
		if !used[name] {
			e := b.add("npc_template", name, name, "未被地图创建记录引用的NPC模板")
			e.field("功能", t.FunctionSet)
			e.Sources = []string{source(t.Source)}
			e.Notes = []string{"未找到地图放置位置"}
		}
	}
}
func unique(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}
func (b *builder) addEncounters() {
	groups := map[int]aiknowledge.EncounterGroup{}
	for _, g := range b.k.Groups {
		groups[g.ID] = g
	}
	for _, a := range b.k.Encounters {
		e := b.add("encounter", a.ID, b.mapName(a.Floor)+" · 遭遇区域 "+text(a.ID), "野外遭遇配置；不是固定NPC坐标")
		e.Location = point(a.Bounds)
		e.field("地图", b.mapName(a.Floor))
		e.field("区域", point(a.Bounds))
		e.field("最多敌人数", a.MaxEnemies)
		e.field("遭遇概率参数", rangeText(a.EncounterProbability))
		e.Sources = []string{source(a.Source)}
		if m := b.maps[a.Floor]; m != nil {
			e.link("map", a.Floor, m.Name)
			m.link("encounter", a.ID, "遭遇区域 "+text(a.ID)+" "+point(a.Bounds))
		}
		t := Table{Title: "敌群与候选敌人", Columns: []string{"敌群", "区域权重", "候选敌人", "敌人ID", "生成权重", "等级"}}
		for i, id := range a.GroupIDs {
			if id < 0 {
				continue
			}
			g, ok := groups[id]
			if !ok {
				e.Notes = append(e.Notes, fmt.Sprintf("敌群%d缺失", id))
				continue
			}
			e.Sources = append(e.Sources, source(g.Source))
			weight := ""
			if i < len(a.GroupProbabilities) {
				weight = text(a.GroupProbabilities[i])
			}
			for j, eid := range g.EnemyIDs {
				if eid < 0 {
					continue
				}
				n, ok := b.enemies[eid]
				if !ok {
					e.Notes = append(e.Notes, fmt.Sprintf("敌人%d缺失", eid))
					continue
				}
				nw := ""
				if j < len(g.CreateProbabilities) {
					nw = text(g.CreateProbabilities[j])
				}
				t.Rows = append(t.Rows, []string{fmt.Sprintf("%s [%d]", g.Name, id), weight, n.Name, text(eid), nw, rangeText(n.Levels)})
				e.link("enemy", eid, n.Name)
				if en := b.c.Entries[key("enemy", eid)]; en != nil {
					en.link("encounter", a.ID, e.Name)
				}
			}
		}
		e.Tables = append(e.Tables, t)
		e.Notes = unique(e.Notes)
	}
	for _, w := range b.k.Warps {
		if m := b.maps[w.From.Floor]; m != nil {
			if len(m.Tables) == 0 {
				m.Tables = append(m.Tables, Table{Title: "传送出口", Columns: []string{"起点", "目标地图", "目标坐标", "类型/时间/条件"}})
			}
			m.Tables[0].Rows = append(m.Tables[0].Rows, []string{fmt.Sprintf("(%d, %d)", w.From.X, w.From.Y), b.mapName(w.To.Floor), fmt.Sprintf("(%d, %d)", w.To.X, w.To.Y), w.Type + " / " + w.Time + " / " + w.Attribute})
			m.link("map", w.To.Floor, b.mapName(w.To.Floor))
		}
	}
}
func (b *builder) addGameTasks() {
	for _, t := range b.k.TaskDefinitions {
		e := b.add("server_task", t.ID, t.Name, t.Description)
		e.field("任务ID", t.ID)
		e.field("验证状态", t.Status)
		e.field("准备审查", t.PreparationReviewed)
		e.field("本服执行验证", t.ExecutionVerified)
		e.field("说明", t.PreparationNotes)
		if !t.ExecutionVerified {
			e.Notes = append(e.Notes, "尚未通过本服完整执行验收")
		}
		table := Table{Title: "已定义步骤", Columns: []string{"步骤", "内容"}}
		for i, s := range t.Steps {
			table.Rows = append(table.Rows, []string{text(i + 1), s.Description})
		}
		e.Tables = append(e.Tables, table)
		e.Sources = []string{source(t.Source)}
	}
}
