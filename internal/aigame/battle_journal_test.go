package aigame

import (
	"strings"
	"testing"
	"time"
)

func TestSharedBattleJournal(t *testing.T) {
	s := &Session{}
	record := func(fn, raw string) {
		e := stringEvent(fn, raw)
		if fn == "EN" {
			e.Fields = []Field{{Kind: FieldInt, Int: 1}}
		}
		e.At = time.Now()
		s.applyEvent(e)
	}
	record("EN", "")
	record("B", "BC|0|0|玩家||1|14|3E8|3E8|4|1|骑宠|14|12C|190|A|敌人||2|A|1F4|1F4|0|0||0|0|0|")
	record("B", "BP|0|0|32")
	record("B", "BVS|0|32|64|")
	record("B", "BH|a0|rA|f2|d64|p14|rA|f2|d50|pA|rA|f20|d0|p0|r0|f2|counter1E|p5|FF|")
	b := s.BattleJournal().Battles[0]
	if len(b.Logs) != 5 || *b.Roster[0].MP != 50 || b.Roster[0].PetHP != 300 {
		t.Fatalf("bad journal: %+v", b)
	}
	if !strings.Contains(b.Logs[1].Text, "第 1/3 段，体力 −100，骑宠体力 −20") || !strings.Contains(b.Logs[3].Text, "闪避") || b.Logs[4].Text != "敌人 → 玩家：反击，体力 −30，骑宠体力 −5" {
		t.Fatalf("bad hits: %+v", b.Logs)
	}
	record("B", "BD|rA|0|0|d64|p14|FF|")
	if len(s.BattleJournal().Battles[0].Logs) != 5 {
		t.Fatal("duplicated damage")
	}
	record("B", "BD|r0|0|1|d64|p0|FF|")
	record("B", "BE|eA|f1|FF|")
	record("B", "bg|A|FF|")
	record("B", "BA|0|2")
	record("B", "BM|A|1|")
	b = s.BattleJournal().Battles[0]
	if b.Logs[len(b.Logs)-1].Text != "敌人：中毒" || b.Logs[len(b.Logs)-1].Turn != 2 {
		t.Fatal(b.Logs)
	}
	*b.Roster[0].MP = 999
	b.Roster[0].Name = "mutated"
	b.Logs[0].Text = "mutated"
	*b.MyNo = 9
	fresh := s.BattleJournal().Battles[0]
	if *fresh.Roster[0].MP != 50 || fresh.Roster[0].Name == "mutated" || *fresh.MyNo != 0 || fresh.Logs[0].Text == "mutated" {
		t.Fatal("snapshot aliases journal")
	}
	record("B", "BU")
	record("RS", "")
	if s.BattleJournal().Battles[0].Result != "已结算" {
		t.Fatal("settlement")
	}
	for i := 0; i < 25; i++ {
		record("EN", "")
	}
	for i := 0; i < 350; i++ {
		record("B", "BH|a0|rA|f2|d1|p0|FF|")
	}
	j := s.BattleJournal()
	if len(j.Battles) != 20 || len(j.Battles[0].Logs) != 300 || !j.Battles[0].Trimmed {
		t.Fatal("limits")
	}
	record("CharLogin", "successful")
	if len(s.BattleJournal().Battles) != 0 {
		t.Fatal("login reset")
	}
}
func TestJournalUnknownMagicDoesNotInventEscape(t *testing.T) {
	j := battleJournal{}
	j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	j.record(stringEvent("B", "BJ|a0|iBC614E|rA|FF|A|BE|BE|0|12345678|"))
	if len(j.battles[0].Logs) != 2 || j.battles[0].Logs[1].Kind != "unknown" {
		t.Fatal(j.battles)
	}
}

func TestJournalNativeConcatenatedEffects(t *testing.T) {
	j := battleJournal{}
	j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	// Native ordinary spells terminate BJ with FF, but BD, bg and BM have
	// positional boundaries. BE is also a valid hexadecimal damage amount.
	j.record(stringEvent("B", "BJ|a0|m5C|e188F8|e188F9|r0|FF|BD|r0|0|1|d41|p0|bg|a|"+
		"BJ|a1|m5C|r1|FF|BD|r1|0|1|d32|p0|BM|A|1|BM|B|4|"+
		"BD|rA|0|0|BE|BD|rB|0|0|FF|BH|a0|rA|f2|d10|p0|FF|BD|rA|0|0|d10|p0|"))
	logs := j.battles[0].Logs[1:]
	want := []string{"unknown", "BD", "bg", "unknown", "BD", "BM", "BM", "BD", "BD", "attack"}
	if len(logs) != len(want) {
		t.Fatalf("effects swallowed or duplicated: %+v", logs)
	}
	for i, kind := range want {
		if logs[i].Kind != kind {
			t.Fatalf("effect %d: %+v, want %s", i, logs[i], kind)
		}
	}
	if *logs[1].Delta != 65 || *logs[4].Delta != 50 || *logs[7].Delta != -190 || *logs[8].Delta != -255 || logs[7].Target != 10 || *logs[6].Status != 4 {
		t.Fatal("incorrect native effect values", logs)
	}
}

func TestJournalUnsupportedResourceModesRemainUnknown(t *testing.T) {
	for _, raw := range []string{"BD|r0|0|2|d41|", "BD|r0|2|1|d41|", "BD|r0|0|1|dZZ|", "BD|r14|0|1|d41|"} {
		j := battleJournal{}
		j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
		j.record(stringEvent("B", raw))
		if logs := j.battles[0].Logs; len(logs) != 2 || logs[1].Kind != "unknown" || logs[1].Delta != nil {
			t.Fatalf("invented resource change from %q: %+v", raw, logs)
		}
	}
}

func TestJournalNativePetSwitchSequence(t *testing.T) {
	j := battleJournal{}
	j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	// bn and BS have positional tails, with no FF separator. A summon name
	// itself can be a valid marker; do not search inside the record for one.
	j.record(stringEvent("B", "bn|f|BH|a5|rA|fA|d0|p0|FF|bg|a|BS|s0|f0|BS|s0|f1|g187AA|l23|hD2|FF|mD2|BH|aA|r0|f2|dA|p0|FF|"))
	want := []string{"wait", "attack", "bg", "pet_recall", "pet_summon", "attack"}
	logs := j.battles[0].Logs[1:]
	if len(logs) != len(want) {
		t.Fatalf("native effects swallowed: %+v", logs)
	}
	for i, kind := range want {
		if logs[i].Kind != kind {
			t.Fatal(i, logs[i], kind)
		}
	}
	if logs[0].Actor != 15 || logs[1].Actor != 5 || logs[3].Actor != 0 || logs[3].Target != 5 || logs[4].Target != 5 || logs[5].Damage != 10 {
		t.Fatal(logs)
	}
	for _, packet := range []string{"BS|sF|f0|", "BS|s0|f1|g0|l1|h2|name|mZZ|", "BS|s0|f1|g0|l1|h2|name|", "BS|s0|f2|", "bn|ZZ|"} {
		j.record(stringEvent("B", packet))
		logs = j.battles[0].Logs
		if logs[len(logs)-1].Kind != "unknown" {
			t.Fatal("malformed switch invented an effect", packet, logs[len(logs)-1])
		}
	}
}
