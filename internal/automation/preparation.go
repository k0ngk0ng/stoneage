package automation

import "fmt"

// preparationProblems explains failed departure conditions using the same
// snapshot and predicates as execution. It never observes or changes the game.
func preparationProblems(conditions []Condition, o Observation) []string {
	problems := taskLevelProblems(conditions, o)
	seen := map[string]bool{}
	for _, problem := range problems {
		seen[problem] = true
	}
	for _, c := range conditions {
		if c.Match(o) {
			continue
		}
		problem := ""
		switch c.Kind {
		case "pet_species_absent":
			problem = fmt.Sprintf("已有宠物种类 %d 与交付目标冲突，或种类资料尚未同步；请先将已有同种宠物存入银行", c.Value)
		case "pet_free_slots":
			if !o.Flags["pets:known"] {
				problem = "宠物资料尚未同步"
			} else {
				problem = fmt.Sprintf("宠物空位不足：当前 %d，要求至少 %d；不会丢弃已有宠物", 5-len(o.Pets), c.Value)
			}
		case "gold_reward_capacity":
			if !o.Flags["gold_limit:known"] {
				problem = "石币容量尚未同步，不能确认可领取任务奖励"
			} else {
				problem = fmt.Sprintf("领取 %d 石币奖励后必须低于持币上限，请先存放多余石币", c.Value)
			}
		case "backpack_free_slots":
			used, known := o.OwnProgress["backpack_used_slots"]
			if !o.Flags["inventory:known"] || !known || used < 0 || used > 15 {
				problem = fmt.Sprintf("背包资料尚未同步：任务要求至少 %d 个空位", c.Value)
			} else {
				problem = fmt.Sprintf("背包空位不足：当前 %d，要求至少 %d，请先整理背包", 15-used, c.Value)
			}
		case "flag_set":
			if c.ID == "party:solo" {
				if _, known := o.Flags[c.ID]; !known {
					problem = "队伍状态尚未同步：任务要求单人进行"
				} else {
					problem = "任务要求单人进行，请先退出队伍"
				}
			}
		case "not_battle":
			problem = "正在战斗，请结束战斗后再开始任务"
		case "alive":
			if o.Dead {
				problem = "人物已死亡，请先复活"
			} else {
				problem = "人物生命状态尚未确认，请先恢复并同步状态"
			}
		case "character_hp_full", "character_hp_percent":
			switch {
			case !o.Connected || !o.Ready || o.Character.MaxHP <= 0 || o.Character.HP < 0 || o.Character.HP > o.Character.MaxHP:
				problem = "人物生命资料尚未同步"
			case o.Dead || o.Character.HP == 0:
				problem = "人物已死亡，请先复活"
			case o.Battle:
				problem = "正在战斗，请结束战斗后再开始任务"
			case c.Kind == "character_hp_full":
				problem = fmt.Sprintf("人物生命未满：当前 %d/%d，请先恢复", o.Character.HP, o.Character.MaxHP)
			default:
				problem = fmt.Sprintf("人物生命不足：当前 %d/%d，要求至少 %d%%，请先恢复", o.Character.HP, o.Character.MaxHP, c.Value)
			}
		case "gold_at_least":
			problem = fmt.Sprintf("任务石币不足：当前 %d，要求至少 %d", o.Gold, c.Value)
		}
		if problem != "" && !seen[problem] {
			problems = append(problems, problem)
			seen[problem] = true
		}
	}
	return problems
}
