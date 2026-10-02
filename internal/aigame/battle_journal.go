package aigame

// Battle journal is a read-only projection shared by the Web bridge and sactl.
// It never changes command readiness or animation state.
import (
	"fmt"
	"strconv"
	"strings"
)

type BattleLogEntry struct {
	Turn      int    `json:"turn"`
	Kind      string `json:"kind"`
	Actor     int    `json:"actor"`
	Target    int    `json:"target"`
	Damage    int    `json:"damage"`
	PetDamage int    `json:"petDamage"`
	Flags     int    `json:"flags"`
	Hit       int    `json:"hit,omitempty"`
	Hits      int    `json:"hits,omitempty"`
	Text      string `json:"text"`
	Raw       string `json:"raw,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Delta     *int   `json:"delta,omitempty"`
	Status    *int   `json:"status,omitempty"`
	// Recipient and Guardian are battle slots (0..19), never persistent IDs.
	// Nil means absent/unresolved; slot zero is a valid observed participant.
	Recipient *int `json:"recipient,omitempty"`
	Guardian  *int `json:"guardian,omitempty"`
}
type BattleLogPerson struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Level    int    `json:"level"`
	HP       int    `json:"hp"`
	MaxHP    int    `json:"maxHp"`
	MP       *int   `json:"mp,omitempty"`
	MaxMP    *int   `json:"maxMp,omitempty"`
	Player   bool   `json:"player"`
	Ride     bool   `json:"ride"`
	PetName  string `json:"petName"`
	PetHP    int    `json:"petHp"`
	PetMaxHP int    `json:"petMaxHp"`
}
type BattleLog struct {
	ID      int               `json:"id"`
	Started int64             `json:"started"`
	Turn    int               `json:"turn"`
	MyNo    *int              `json:"myNo"`
	Roster  []BattleLogPerson `json:"roster"`
	Logs    []BattleLogEntry  `json:"logs"`
	Ended   bool              `json:"ended"`
	Result  string            `json:"result"`
	Trimmed bool              `json:"trimmed"`
}
type BattleJournal struct {
	Battles []BattleLog `json:"battles"`
}
type battleJournal struct {
	battles []BattleLog
	serial  int
	pending []string
	emitted []BattleLogEntry
}

func journalNumber(s string) int { n, _ := strconv.ParseInt(s, 16, 32); return int(n) }

func journalSlot(s string) (int, bool) {
	n, err := strconv.ParseUint(s, 16, 8)
	return int(n), err == nil && n < 20
}
func (s *Session) BattleJournal() BattleJournal {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	out := BattleJournal{Battles: append([]BattleLog{}, s.journal.battles...)}
	for i := range out.Battles {
		b := &out.Battles[i]
		b.Roster = append([]BattleLogPerson{}, b.Roster...)
		b.Logs = cloneBattleLogEntries(b.Logs)
		if b.MyNo != nil {
			v := *b.MyNo
			b.MyNo = &v
		}
		for k := range b.Roster {
			p := &b.Roster[k]
			if p.MP != nil {
				v := *p.MP
				p.MP = &v
			}
			if p.MaxMP != nil {
				v := *p.MaxMP
				p.MaxMP = &v
			}
		}
	}
	return out
}
func (j *battleJournal) add(e BattleLogEntry) {
	b := &j.battles[0]
	e.Turn = b.Turn
	j.emitted = append(j.emitted, e)
	b.Logs = append(b.Logs, e)
	if len(b.Logs) > 300 {
		b.Logs = append([]BattleLogEntry(nil), b.Logs[len(b.Logs)-300:]...)
		b.Trimmed = true
	}
}
func (j *battleJournal) name(id int) string {
	name := ""
	count := 0
	for _, p := range j.battles[0].Roster {
		if p.ID == id {
			name = p.Name
		}
	}
	for _, p := range j.battles[0].Roster {
		if p.Name == name {
			count++
		}
	}
	if name == "" {
		return fmt.Sprintf("%d号位", id+1)
	}
	if count > 1 {
		return fmt.Sprintf("%s（%d号位）", name, id+1)
	}
	return name
}
func (j *battleJournal) record(e Event) {
	switch e.Function {
	case "EN", "B", "BC", "RS", "RD", "CharLogin", "CharLogout":
	default:
		return
	}
	raw := eventText(e, 0)
	if (e.Function == "CharLogin" || e.Function == "CharLogout") && raw == "successful" {
		*j = battleJournal{}
		return
	}
	if e.Function == "EN" {
		if eventInt(e, 0, 0) <= 0 {
			if len(j.battles) > 0 {
				j.battles[0].Ended = true
				if j.battles[0].Result == "进行中" {
					j.battles[0].Result = "已结束"
				}
			}
			return
		}
		j.serial++
		if len(j.battles) > 0 && !j.battles[0].Ended {
			j.battles[0].Ended = true
			j.battles[0].Result = "已离开"
		}
		j.battles = append([]BattleLog{{ID: j.serial, Started: e.At.UnixMilli(), Turn: 1, Result: "进行中", Roster: []BattleLogPerson{}, Logs: []BattleLogEntry{}}}, j.battles...)
		if len(j.battles) > 20 {
			j.battles = j.battles[:20]
		}
		j.pending = nil
		j.add(BattleLogEntry{Kind: "start", Text: "战斗开始"})
		return
	}
	if len(j.battles) == 0 {
		return
	}
	b := &j.battles[0]
	if e.Function == "RS" || e.Function == "RD" {
		b.Ended = true
		b.Result = "已结算"
		j.add(BattleLogEntry{Kind: "end", Text: "战斗结束，奖励请查看游戏结算窗口"})
		return
	}
	if e.Function != "B" && e.Function != "BC" {
		return
	}
	if e.Function == "BC" && !strings.HasPrefix(raw, "BC|") {
		raw = "BC|" + raw
	}
	p := strings.Split(strings.TrimRight(raw, "|"), "|")
	n := journalNumber
	switch p[0] {
	case "BU":
		b.Ended = true
		if b.Result == "进行中" {
			b.Result = "已结束"
		}
		return
	case "BA":
		if len(p) > 2 {
			b.Turn = max(1, n(p[2]))
		}
		j.pending = nil
		return
	case "BVS":
		for i := 1; i+2 < len(p); i += 3 {
			for k := range b.Roster {
				r := &b.Roster[k]
				if r.ID == n(p[i]) && r.Player {
					mp, mx := n(p[i+1]), n(p[i+2])
					r.MP = &mp
					r.MaxMP = &mx
				}
			}
		}
		return
	case "BC":
		if len(p) < 2 {
			return
		}
		body := p[2:]
		width := 13
		if len(body)%13 != 0 {
			width = 8
		}
		if len(body)%width != 0 || len(body) > 260 {
			return
		}
		b.Roster = nil
		for i := 0; i < len(body); i += width {
			v := body[i : i+width]
			r := BattleLogPerson{ID: n(v[0]), Name: strings.NewReplacer(`\z`, "|", `\y`, `\`).Replace(v[1]), Level: n(v[4]), HP: n(v[5]), MaxHP: n(v[6]), Player: n(v[7])&4 != 0}
			if width == 13 {
				r.Ride = n(v[8]) == 1
				r.PetName = v[9]
				r.PetHP = n(v[11])
				r.PetMaxHP = n(v[12])
			}
			b.Roster = append(b.Roster, r)
		}
		return
	case "BP":
		if len(p) == 4 {
			if _, err := strconv.ParseUint(p[1], 16, 8); err == nil {
				v := n(p[1])
				b.MyNo = &v
				return
			}
		}
	}
	// Only known records are decoded. In particular BJ's positional magic tail
	// can contain tokens such as BE: never search that tail for command markers.
	for i := 0; i < len(p); {
		m := p[i]
		i++
		if m == "BP" || m == "" || m == "FF" {
			continue
		}
		start := i
		fixed := map[string]int{"BD": 4, "BM": 2, "BG": 1, "bg": 1, "bn": 1}[m]
		if m == "BS" && start+1 < len(p) {
			if p[start+1] == "f0" {
				fixed = 2
			}
			if p[start+1] == "f1" {
				fixed = 7
			}
		}
		if fixed > 0 {
			// Native BD/BM/bg are concatenated without FF. Consume their
			// defined positional fields, never search numeric payloads for
			// marker-looking hex values such as BE or BD.
			for i < len(p) && i < start+fixed {
				i++
			}
			if i-start != fixed {
				m = "unknown"
			}
			if m == "BD" {
				for i < len(p) && (strings.HasPrefix(p[i], "p") || strings.HasPrefix(p[i], "m")) {
					i++
				}
			}
		} else {
			for i < len(p) && p[i] != "FF" {
				i++
			}
		}
		v := p[start:i]
		if i < len(p) && p[i] == "FF" {
			i++
		}
		segment := strings.Join(append([]string{m}, v...), "|")
		j.movie(m, v, segment)
		extended := m == "B$"
		if m == "BJ" {
			for _, value := range v {
				if strings.HasPrefix(value, "i") {
					extended = true
				}
			}
		}
		if extended {
			break
		}
	}
}
func (j *battleJournal) movie(m string, v []string, raw string) {
	n := journalNumber
	field := func(key string) int {
		for _, s := range v {
			if strings.HasPrefix(s, key) {
				return n(strings.TrimPrefix(s, key))
			}
		}
		return 0
	}
	if strings.Contains("|BH|BI|BB|Bb|Bd|Bh|Bp|", "|"+m+"|") {
		type hit struct {
			target, flags, damage, pet, guardian   int
			counter                                bool
			targetKnown, flagsKnown, guardianKnown bool
		}
		var hits []hit
		actor := field("a")
		actorKnown := false
		for _, s := range v {
			if strings.HasPrefix(s, "a") {
				_, actorKnown = journalSlot(s[1:])
				break
			}
		}
		for _, s := range v {
			if strings.HasPrefix(s, "r") {
				_, known := journalSlot(s[1:])
				hits = append(hits, hit{target: n(s[1:]), targetKnown: known, guardian: -1})
				continue
			}
			if len(hits) == 0 {
				continue
			}
			h := &hits[len(hits)-1]
			switch {
			case strings.HasPrefix(s, "counter"):
				h.counter = true
				h.damage = n(s[7:])
			case strings.HasPrefix(s, "f"):
				h.flags = n(s[1:])
				_, err := strconv.ParseUint(s[1:], 16, 31)
				h.flagsKnown = err == nil
			case strings.HasPrefix(s, "d"):
				h.damage = n(s[1:])
			case strings.HasPrefix(s, "p"):
				h.pet = n(s[1:])
			case strings.HasPrefix(s, "g"):
				h.guardian = n(s[1:])
				_, h.guardianKnown = journalSlot(s[1:])
			}
		}
		count := 0
		for _, h := range hits {
			if !h.counter {
				count++
			}
		}
		previous := actor
		previousKnown := actorKnown
		for i, h := range hits {
			a := actor
			aKnown := actorKnown
			kind, action := "attack", "攻击"
			if count > 1 {
				action = fmt.Sprintf("攻击 · 第 %d/%d 段", i+1, count)
			}
			if h.counter {
				a = previous
				aKnown = previousKnown
				kind = "counter"
				action = "反击"
			}
			previous = h.target
			previousKnown = h.targetKnown
			sign := "−"
			if h.flags&2048 != 0 {
				sign = "+"
			}
			out := fmt.Sprintf("体力 %s%d", sign, h.damage)
			if h.pet != 0 {
				out += fmt.Sprintf("，骑宠体力 %s%d", sign, h.pet)
			}
			if h.flags&32 != 0 {
				out = "闪避"
			}
			if h.flags&4096 != 0 {
				out = "消失"
			}
			var tags []string
			for _, t := range []struct {
				bit  int
				text string
			}{{4, "暴击"}, {8, "防御"}, {512, "忠犬保护"}, {1024, "反射"}, {1, "倒下"}} {
				if h.flags&t.bit != 0 {
					tags = append(tags, t.text)
				}
			}
			if h.guardian >= 0 && h.flags&512 != 0 {
				tags = append(tags, "保护者："+j.name(h.guardian))
			}
			if len(tags) > 0 {
				out += "（" + strings.Join(tags, "、") + "）"
			}
			entry := BattleLogEntry{Kind: kind, Actor: a, Target: h.target, Damage: h.damage, PetDamage: h.pet, Flags: h.flags, Hit: i + 1, Hits: count, Raw: raw, Text: fmt.Sprintf("%s → %s：%s，%s", j.name(a), j.name(h.target), action, out)}
			if h.flagsKnown && h.flags&512 != 0 && h.guardianKnown {
				guardian := h.guardian
				entry.Guardian = &guardian
			}
			if h.flagsKnown && h.flags&(32|4096) == 0 {
				recipient, known := h.target, h.targetKnown
				// BATTLE_Attack redirects to the guardian first. DamageSub may
				// reflect back to this hit's attacker (also for a counter).
				if h.flags&1024 != 0 {
					recipient, known = a, aKnown
				} else if h.flags&512 != 0 {
					recipient, known = h.guardian, h.guardianKnown
				}
				if known {
					entry.Recipient = &recipient
				}
			}
			j.add(entry)
			if h.flags&(32|4096) == 0 {
				target := h.target
				if h.flags&1024 != 0 {
					target = a
				} else if h.flags&512 != 0 && h.guardian >= 0 {
					target = h.guardian
				}
				s := 0
				if h.flags&2048 != 0 {
					s = 1
				}
				j.pending = append(j.pending, fmt.Sprint(target, ":0:", s, ":", h.damage, ":", h.pet))
				if len(j.pending) > 64 {
					j.pending = j.pending[1:]
				}
			}
		}
		if len(hits) > 0 {
			return
		}
	}
	e := BattleLogEntry{Kind: m, Raw: raw, Actor: -1, Target: -1}
	switch m {
	case "bn":
		if len(v) == 1 {
			actor, err := strconv.ParseUint(v[0], 16, 8)
			if err == nil && actor < 20 {
				e.Kind, e.Actor, e.Text = "wait", int(actor), j.name(int(actor))+"：等待"
			}
		}
	case "BS":
		if len(v) != 2 && len(v) != 7 || !strings.HasPrefix(v[0], "s") {
			break
		}
		actor, err := strconv.ParseUint(v[0][1:], 16, 8)
		if err != nil || actor >= 15 || actor%10 >= 5 {
			break
		}
		if len(v) == 2 && v[1] == "f0" {
			e.Kind, e.Actor, e.Target, e.Text = "pet_recall", int(actor), int(actor)+5, j.name(int(actor))+"：收回宠物"
		} else if len(v) == 7 && v[1] == "f1" {
			valid := true
			for _, field := range []struct {
				index  int
				prefix string
			}{{2, "g"}, {3, "l"}, {4, "h"}, {6, "m"}} {
				value := v[field.index]
				if !strings.HasPrefix(value, field.prefix) {
					valid = false
					break
				}
				if _, err := strconv.ParseUint(value[1:], 16, 32); err != nil {
					valid = false
					break
				}
			}
			if valid {
				e.Kind, e.Actor, e.Target, e.Text = "pet_summon", int(actor), int(actor)+5, j.name(int(actor))+"：召唤宠物"
			}
		}
	case "BD":
		if len(v) < 4 || !strings.HasPrefix(v[0], "r") {
			break
		}
		// Poison/periodic native records can omit the d prefix. Signs 2..6
		// describe other mechanics, not ordinary positive HP recovery.
		values := []string{strings.TrimPrefix(v[0], "r"), v[1], v[2], strings.TrimPrefix(v[3], "d")}
		valid := true
		for _, value := range values {
			if _, err := strconv.ParseUint(value, 16, 32); err != nil {
				valid = false
			}
		}
		target, kind, sign, amount, pet := n(values[0]), n(values[1]), n(values[2]), n(values[3]), field("p")
		if !valid || target < 0 || target >= 20 || kind > 1 || sign > 1 {
			break
		}
		key := fmt.Sprint(target, ":", kind, ":", sign, ":", amount, ":", pet)
		for i, k := range j.pending {
			if key == k {
				j.pending = append(j.pending[:i], j.pending[i+1:]...)
				return
			}
		}
		label, symbol := "体力", "−"
		if kind == 1 {
			label = "气力"
		}
		if sign != 0 {
			symbol = "+"
		}
		e.Target = target
		e.Damage = amount
		e.PetDamage = pet
		e.Resource = "hp"
		if kind == 1 {
			e.Resource = "mp"
		}
		delta := -amount
		if sign != 0 {
			delta = amount
		}
		e.Delta = &delta
		e.Text = fmt.Sprintf("%s：%s %s%d", j.name(target), label, symbol, amount)
		if pet != 0 {
			e.Text += fmt.Sprintf("，骑宠体力 %s%d", symbol, pet)
		}
	case "BM":
		if len(v) >= 2 {
			target, status := n(v[0]), n(v[1])
			labels := []string{"状态解除", "中毒", "麻痹", "睡眠", "石化", "酒醉", "混乱"}
			label := "特殊状态变化"
			if status >= 0 && status < len(labels) {
				label = labels[status]
			}
			e.Target = target
			e.Status = &status
			e.Text = j.name(target) + "：" + label
		}
	case "BG", "bg":
		if len(v) > 0 {
			e.Actor = n(v[0])
			e.Text = j.name(e.Actor) + "：防御动作"
		}
	case "BE":
		e.Actor = field("e")
		result := "失败"
		if field("f") == 1 {
			result = "成功"
		}
		e.Text = j.name(e.Actor) + "：逃跑" + result
	}
	if e.Text == "" {
		e.Kind = "unknown"
		e.Text = "发生特殊战斗动作，暂未转换具体技能或效果"
	}
	j.add(e)
}
