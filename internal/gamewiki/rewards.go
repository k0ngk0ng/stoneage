package gamewiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// These are explicit quest-to-script matches, not name-based guesses. Lists are
// interpreted according to NPC_RandItemGet and NPC_EventAddPet in npc_exchangeman.c.
var questRewardScripts = map[string]string{
	"b5":    "npc/extra/event/marju",
	"n2":    "npc/jaruga/event/event17_1,npc/jaruga/event/event17_2,npc/jaruga/event/event17_3",
	"n6":    "npc/jaruga/event/event20_2,npc/jaruga/event/event20_4",
	"n9":    "npc/jaruga/event/event06_1",
	"n12":   "npc/jaruga/event/ruri05,npc/jaruga/event/ruri06",
	"s2":    "npc/seimu/event/event01_2",
	"jot01": "npc/jaruga/event/oev_ed1",
	"jot02": "npc/jaruga/event/oev_edb",
	"jot03": "npc/sainasu/event/oev_edc",
	"jot04": "npc/sainasu/event/oev_edd",
}

func (b *builder) questRewards(e *Entry, q Quest) Table {
	table := Table{Title: "任务奖励", Columns: []string{"奖励", "数量 / 抽取次数", "获得方式与概率", "条件"}}
	script := questRewardScripts[q.ID]
	if script == "" {
		probability := "攻略未注明概率；尚未核对本服发奖配置"
		if strings.Contains(q.Reward, "随机") {
			probability = "随机奖励；候选池及概率尚未核实"
		}
		if strings.Contains(q.Reward, "选") || strings.Contains(q.Reward, "分支") {
			probability = "按选择或任务分支获得；不是全部同时获得"
		}
		table.Rows = append(table.Rows, []string{q.Reward, "按任务说明", probability, q.Prerequisites})
		return table
	}
	for _, script := range strings.Split(script, ",") {
		raw, err := os.ReadFile(filepath.Join(b.k.DataDir, script))
		if err != nil {
			table.Rows = append(table.Rows, []string{q.Reward, "待核对", "未能读取发奖配置", q.Prerequisites})
			continue
		}
		for _, block := range strings.Split(decode(raw), "EventEnd") {
			fields := map[string]string{}
			for _, line := range strings.Split(block, "\n") {
				k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
				if ok {
					fields[k] = strings.TrimSpace(v)
				}
			}
			condition := "完成任务并交付；背包需有空位"
			if q.ID == "n2" {
				condition = "集齐三颗试炼之玉和龙之魅笛，向对应龙兑换；三件装备只能选一件"
			}
			if q.ID == "n6" {
				if fields["GetItem"] != "2489" && fields["GetRandItem"] == "" {
					continue
				}
				condition = "将女儿的家书交给吉德"
				if fields["GetRandItem"] != "" {
					condition = "听取女儿的决定后向强恩汇报，随机领取一件装备"
				}
			}
			if q.ID == "n9" {
				condition = "持有两枚贝壳时交出一枚，保留另一枚；宠物栏需有空位"
				if fields["GetPet"] == "47" {
					condition = "仅持有一枚贝壳时交出最后一枚；宠物栏需有空位"
				}
			}
			if strings.HasPrefix(q.ID, "jot") {
				condition = "完成全程并交回最终检查证明；背包、宠物栏需有空位"
				if fields["GetPet"] == "" {
					condition = "中途退出的参加奖；仅持第 5～11 检查证明时可领"
				}
			}
			if q.ID == "n12" {
				bag := "大袋子"
				if strings.HasSuffix(script, "ruri06") {
					bag = "小袋子"
				}
				condition = "选择" + bag + "，交出大地之羽[地]；两个袋子只能选一个"
			}
			if q.ID == "b5" {
				condition = "等级至少 50，完成送信并向马祖交付"
				if fields["GetRandItem"] == "" {
					continue
				}
			}
			if q.ID == "s2" {
				condition = "交付先见之光；按任务进度领取"
				switch fields["GetItem"] {
				case "2606":
					condition = "成人并首次交付先见之光"
				case "2605":
					condition = "成人并第二次交付先见之光"
				case "2604":
					condition = "成人并第三次交付先见之光"
				}
				if fields["GetRandItem"] != "" {
					condition = "交付先见之光，前三阶段已完成；或等级至少 35（前面的阶段分支优先）"
				}
			}
			for _, field := range []string{"GetItem", "GetRandItem", "GetPet"} {
				values := nativeRewardIDs(fields[field], field)
				if len(values) == 0 {
					continue
				}
				counts := map[int]int{}
				order := []int{}
				for _, id := range values {
					if counts[id] == 0 {
						order = append(order, id)
					}
					counts[id]++
				}
				for _, id := range order {
					name := fmt.Sprintf("未定义奖励 [%d]", id)
					kind := "item"
					if field == "GetPet" {
						kind = "enemy"
						if n, ok := b.enemies[id]; ok {
							name = n.Name
							if p, ok := b.bases[n.TemplateID]; ok {
								name = p.Name
							}
							name += "（等级 " + rangeText(n.Levels) + "）"
						}
					} else if item, ok := b.items[id]; ok {
						name = item.Name
						kind = itemKind(item)
					}
					amount, probability := "1 件", "固定获得（满足条件且发放成功）"
					if field != "GetItem" {
						amount = "此奖励池抽 1 次"
						probability = fmt.Sprintf("%d/%d ≈ %.2f%%", counts[id], len(values), 100*float64(counts[id])/float64(len(values)))
						if len(values) == 1 {
							probability = "固定获得（满足条件且发放成功）"
							amount = "1 件"
							if field == "GetPet" {
								amount = "1 只"
							}
						}
					} else {
						amount = fmt.Sprintf("%d 件", counts[id])
					}
					table.Rows = append(table.Rows, []string{name, amount, probability, condition})
					e.link(kind, id, name)
				}
			}
		}
		e.Sources = append(e.Sources, script)
	}
	e.Sources = append(e.Sources, "npc/npc_exchangeman.c · NPC_RandItemGet / NPC_EventAddPet")
	return table
}

// NPC_EventAdd reads item lists into char buf[64], pet lists into buff2[128].
// strcpysafe reserves the last byte for NUL; atoi accepts a truncated last ID.
// Preserve every slot (including an empty token) because rand()%N uses slots,
// not the number of unique or valid items. All configured reward IDs are ASCII.
func nativeRewardIDs(value, field string) []int {
	if value == "" {
		return nil
	}
	limit := 63
	if field == "GetPet" {
		limit = 127
	}
	if len(value) > limit {
		value = value[:limit]
	}
	var out []int
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		end := 0
		for end < len(token) && token[end] >= '0' && token[end] <= '9' {
			end++
		}
		id, _ := strconv.Atoi(token[:end])
		out = append(out, id)
	}
	return out
}
