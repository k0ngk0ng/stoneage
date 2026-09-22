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
	"n1":                 "npc/jaruga/event/event01_1",
	"n3":                 "npc/jaruga/event/20501ev5",
	"n4":                 "npc/jaruga/event/event07_1",
	"n5":                 "npc/jaruga/event/ruri01",
	"n7":                 "npc/sainasu/event/nevent02_3",
	"n8":                 "npc/jaruga/event/kikukuev",
	"n10":                "npc/jaruga/event/event04_1",
	"n13":                "npc/sainasu/event/nevent01_1",
	"n14":                "npc/quiz/equiz_02",
	"b1":                 "npc/sainasu/event/event03_2",
	"b2":                 "npc/sainasu/event/event15_1",
	"b3":                 "npc/sainasu/event/event02_2",
	"b4":                 "npc/sainasu/event/genou_4,npc/sainasu/event/genou_1",
	"b6":                 "npc/seimu/event/event02_1",
	"b8":                 "npc/sainasu/event/event13_1",
	"b9":                 "npc/sainasu/event/event09",
	"j4":                 "npc/giiru/event/garunaev3",
	"j3":                 "npc/giiru/event/event30_6,npc/giiru/event/event30_5",
	"j5":                 "npc/giiru/event/nev1_5",
	"j1":                 "npc/giiru/event/ev28_ed1,npc/giiru/event/ev28_ed2,npc/giiru/event/ev28_ed3,npc/giiru/event/ev28_ed4,npc/giiru/event/ev28_ed5",
	"news_01":            "npc/poru/boy04",
	"news_10":            "npc/poru/girl",
	"news_02":            "npc/poru/team01,npc/poru/team02,npc/poru/mary12,npc/poru/team03,npc/poru/mary01",
	"sa25_01":            "npc/king/event69_7",
	"sa25_04":            "npc/king/event69_11,npc/king/event69_14,npc/king/event69_16,npc/king/event69_18,npc/king/event69_20,npc/king/event69_22,npc/king/event69_24",
	"mining-certificate": "npc/sainasu/event/nevent03_3",
	"z1":                 "npc/jaruga/event/ruri01",
	"z2":                 "npc/sainasu/event/genou_1",
	"z3":                 "npc/jaruga/event/hekisei01",
	"z4":                 "npc/giiru/event/sink/sink02",
	"s1":                 "npc/extra/event/hougyoku",
	"b5":                 "npc/extra/event/marju",
	"n2":                 "npc/jaruga/event/event17_1,npc/jaruga/event/event17_2,npc/jaruga/event/event17_3",
	"n6":                 "npc/jaruga/event/event20_2,npc/jaruga/event/event20_4",
	"n9":                 "npc/jaruga/event/event06_1",
	"n12":                "npc/jaruga/event/ruri05,npc/jaruga/event/ruri06",
	"s2":                 "npc/seimu/event/event01_2",
	"jot01":              "npc/jaruga/event/oev_ed1",
	"jot02":              "npc/jaruga/event/oev_edb",
	"jot03":              "npc/sainasu/event/oev_edc",
	"jot04":              "npc/sainasu/event/oev_edd",
}

// Final rewards only. These files also issue letters, bait, and later-version
// quest tokens, which must not be presented as rewards for the indexed quest.
var questFinalItems = map[string][]int{
	"n1": {2427}, "n4": {1206}, "n5": {2701}, "n7": {1264},
	"n8": {2451, 2452, 2453, 2454, 2455}, "n10": {2418},
	"b1": {2448}, "b8": {2450}, "j4": {2506}, "z1": {2701}, "z2": {2735},
	"n3": {2481}, "b3": {2415}, "b4": {2758, 2735}, "b6": {},
	"b9": {1357}, "s1": {2765}, "z3": {2770}, "z4": {2707},
	"j3":      {2551},
	"sa25_04": {1292, 13061, 13062, 13063, 13064, 19641, 19642, 19643, 19644},
}

func isFinalQuestItem(quest string, id int) bool {
	allowed, filtered := questFinalItems[quest]
	if !filtered {
		return true
	}
	for _, candidate := range allowed {
		if candidate == id {
			return true
		}
	}
	return false
}

// The four village scripts use consecutive voucher IDs, with food vouchers
// offset by 20. Match the voucher, not the translated NPC dialogue.
func commissionRewardScript(id string) (string, int) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || !strings.HasPrefix(parts[0], "wt0") || len(parts[2]) != 1 {
		return "", 0
	}
	village, letter := int(parts[0][3]-'1'), int(parts[2][0]-'A')
	if village < 0 || village > 3 || letter < 0 || letter > 5 || (parts[1] != "pet" && parts[1] != "food") {
		return "", 0
	}
	voucher := 20001 + village*30 + letter
	if parts[1] == "food" {
		voucher += 20
	}
	return fmt.Sprintf("npc/extra/event/M_%d", (village+1)*1000), voucher
}

func (b *builder) questRewards(e *Entry, q Quest) Table {
	table := Table{Title: "任务奖励", Columns: []string{"奖励", "数量 / 抽取次数", "获得方式与概率", "条件"}}
	fame := Table{Title: "声望奖励", Columns: []string{"奖励", "数量", "领取条件"}}
	script := questRewardScripts[q.ID]
	commission, voucher := commissionRewardScript(q.ID)
	if commission != "" {
		script = commission
	}
	if script == "" {
		switch q.ID {
		case "n11", "b7", "b11", "j2", "z5", "sa25_03", "faq01":
			table.Rows = append(table.Rows, []string{q.Reward, "资格 / 服务解锁", "非随机道具奖励", q.Prerequisites})
			return table
		}
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
			switch q.ID {
			case "sa25_01":
				if fields["EndSetFlg"] != "70" {
					continue
				}
				condition = "完成黑蛙王营救，回祖母处接走玛蕾菲雅；宠物和项链同时获得"
			case "news_02":
				if fields["TYPE"] != "ACCEPT" {
					continue
				}
				switch {
				case strings.HasSuffix(script, "team01"):
					condition = "岩之圣石阶段：交付等级恰好 30 的鲁乌和超特级采石鉴定书"
				case strings.HasSuffix(script, "team02"):
					condition = "水之圣石阶段：交付等级恰好 30 的指定宠物和采伐许可证"
				case strings.HasSuffix(script, "mary12"):
					condition = "火之圣石阶段：交付埋龙王守护的钥匙 [" + fields["DelItem"] + "]"
				case strings.HasSuffix(script, "team03"):
					condition = "风之圣石阶段：交付等级恰好 33 的加美和指定钓竿；圣石和宠物同时获得"
				default:
					condition = "集齐并交出四圣石，领取最终宠物；宠物栏需有空位"
				}
			case "mining-certificate":
				quantity := map[string]string{"2474*4": "14", "2474*3": "11～13", "2474*2": "8～10", "2474*1": "5～7"}[fields["GetItem"]]
				condition = "持有 " + quantity + " 份特级采石鉴定书，按档兑换；只领取满足的最高一档"
			case "sa25_04":
				switch {
				case strings.HasSuffix(script, "event69_11"):
					condition = "交齐四属性宝石和四色原石，领取挑战黑暗精灵王所需的铠甲"
				case strings.HasSuffix(script, "event69_14"):
					condition = "击败黑暗精灵王，到天空之岛接下后续任务后领取随机羽毛"
				case strings.HasSuffix(script, "event69_24"):
					condition = "完成天空之岛试炼并击败柯黑穆肯后领取；宠物栏需有空位"
				default:
					condition = "击败对应属性守护者并交谈，身上未持有该戒指时领取"
				}
			case "j1":
				brother := map[string]string{"2518": "利诺恩", "2519": "吉诺", "2520": "奇洛斯", "2526": "浦洛斯", "2525": "浦鲁德"}[fields["GetItem"]]
				condition = "向" + brother + "交出智之结晶；五个兄弟中只能选一个，同时获得该组装备和宠物"
			case "n14":
				condition = "完成猜谜专家试炼并交还会员证；会员证被收回，宠物栏需有空位"
				if fields["GetItem"] != "" {
					condition = "已完成试炼，携带等级恰好 77 的金布伊且未持有会员证或谜之箱子时，再领会员证"
				}
			case "news_01":
				condition = "完成英雄岛前传，向任务少年领取红暴；宠物栏需有空位"
				if fields["GetItem"] != "" {
					condition = "前传已完成，身上没有龙王的守护时，每次可领 2 份"
				}
			case "news_10":
				condition = "前传已完成，交出五块萨姆吉尔勾玉；首饰和五份祝福同时获得"
				if fields["GetItem"] == "18543" {
					if item, ok := b.items[atoi(fields["DelItem"])]; ok {
						condition = "前传已完成，单独交出" + item.Name + "，兑换 1 份祝福"
					}
				}
			case "n13":
				rod := atoi(strings.TrimSuffix(fields["DelItem"], "*1"))
				if item, ok := b.items[rod]; ok {
					condition = "交付" + item.Name + "；各档按钓竿兑换，不随机抽取"
				}
			case "j3":
				condition = "已通过成人仪式，并完成精灵相关前置"
				if fields["GetPet"] != "" {
					condition = "取得并交还真知之眼，完成精灵使者阶段；宠物栏需有空位"
				}
			case "j5":
				condition = "到方位之祠最上层石像处交出地、水、火、风四颗勾玉，随机领取一件装备"
			case "n3":
				_, quantity, ok := strings.Cut(fields["EVENT"], "ITEM=2468*")
				if !ok {
					continue
				}
				condition = "持有采伐许可证并交付至少 " + quantity + " 份木材；只结算满足的最高一档"
			case "b2":
				condition = "第一阶段：交付等级至少 15 的鲁尼帖斯，随机获得一件装备"
				if fields["GetPet"] != "" {
					condition = "完成第一阶段后，交付等级至少 30 的贝鲁卡"
				}
			case "b3":
				if fields["EndSetFlg"] != "2" {
					continue
				}
				condition = "完成送花往返并交出不可思议的贝壳"
			case "b4":
				condition = "玄黄试炼前领取手环凭证"
				if fields["GetItem"] == "2735" {
					condition = "通过成人仪式、完成玄黄洞窟并交还风之竖琴"
				}
			case "b6":
				condition = "鲨鱼阶段：按要求交齐指定等级 25 的宠物和捕捉证明"
				if fields["GetPet"] == "341" {
					condition = "鲨鱼阶段完成后的鸟类阶段：交齐指定等级 25 的宠物和捕捉证明"
				}
			case "s1":
				condition = "等级至少 60，交出四颗宝玉和结晶石"
			case "z3":
				condition = "等级至少 65，完成碧青洞窟并交出碧青的水晶"
			case "z4":
				condition = "已通过成人仪式，完成深红洞窟并交出不死鸟的羽毛"
			}
			if q.ID == "n8" {
				condition = "交还等级至少 15、名字为加特力奴的指定宠物；背包需有空位"
				if fields["Pet_Name"] == "" {
					condition = "交还等级至少 15 的指定宠物，但名字不符；背包需有空位"
				}
			}
			if voucher != 0 {
				if !strings.HasPrefix(fields["DelItem"], fmt.Sprintf("%d*1", voucher)) {
					continue
				}
				condition = "交还对应委托书并交付要求的宠物或料理；物品栏需有空位"
				if fields["GetStone"] != "" {
					condition = "交还对应委托书并交付要求的宠物或料理；石币余额可容纳本次奖励"
				}
				e.link("item", voucher, q.Name)
			}
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
			for _, flag := range ids(fields["EndSetFlg"]) {
				if points := b.fame[flag]; points > 0 {
					fame.Rows = append(fame.Rows, []string{"个人声望", fmt.Sprintf("%.2f", float64(points)/100), condition + "；对应阶段首次完成时获得，达到声望上限后不再增加"})
				}
			}
			if amount, err := strconv.Atoi(fields["GetStone"]); err == nil && amount > 0 {
				table.Rows = append(table.Rows, []string{"石币", text(amount), "固定获得（满足条件且发放成功）", condition})
			}
			for _, field := range []string{"GetItem", "GetRandItem", "GetPet"} {
				values := nativeRewardIDs(fields[field], field)
				if len(values) == 0 {
					continue
				}
				counts := map[int]int{}
				order := []int{}
				for index, id := range values {
					quantity := 1
					if field == "GetItem" {
						quantity = nativeFixedQuantity(fields[field], index)
					}
					if quantity <= 0 {
						continue
					}
					if counts[id] == 0 {
						order = append(order, id)
					}
					counts[id] += quantity
				}
				names := map[string]int{}
				for _, id := range order {
					if item, ok := b.items[id]; ok {
						names[item.Name]++
					}
				}
				for _, id := range order {
					if field != "GetPet" && !isFinalQuestItem(q.ID, id) {
						continue
					}
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
						if names[name] > 1 {
							name += fmt.Sprintf(" [%d]", id)
						}
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
	if voucher != 0 && len(table.Rows) == 0 {
		table.Rows = append(table.Rows, []string{"本服未启用发奖", "—", "发奖分支已停用", "当前不能兑换；委托资料保留供查阅"})
	}
	e.Sources = append(e.Sources, "npc/npc_exchangeman.c · NPC_RandItemGet / NPC_EventAddPet")
	if len(fame.Rows) > 0 {
		e.Tables = append(e.Tables, fame)
		e.Sources = append(e.Sources, "npc/npcutil.c · FMAdvTbl / NPC_EventSetFlg / AddFMAdv", "char/char_base.c · CHAR_earnFame")
	}
	return table
}

// Native quest completion awards hundredths of displayed personal fame.
// Read the checked-in table offline so encyclopedia values track this server.
func loadNativeQuestFame(dataDir string) map[int]int {
	raw, err := os.ReadFile(filepath.Join(dataDir, "..", "npc", "npcutil.c"))
	if err != nil {
		return nil
	}
	_, source, ok := strings.Cut(string(raw), "int FMAdvTbl[] = {")
	if !ok {
		return nil
	}
	source, _, ok = strings.Cut(source, "};")
	if !ok {
		return nil
	}
	var text strings.Builder
	for _, line := range strings.Split(source, "\n") {
		line, _, _ = strings.Cut(line, "//")
		text.WriteString(line)
		text.WriteByte('\n')
	}
	values := map[int]int{}
	for index, token := range strings.Split(text.String(), ",") {
		if amount, err := strconv.Atoi(strings.TrimSpace(token)); err == nil {
			values[index] = amount
		}
	}
	return values
}

func nativeFixedQuantity(value string, index int) int {
	if len(value) > 63 {
		value = value[:63]
	}
	token := strings.Split(value, ",")[index]
	_, quantity, specified := strings.Cut(token, "*")
	if !specified {
		return 1
	}
	return atoi(quantity)
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
