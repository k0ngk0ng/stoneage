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
