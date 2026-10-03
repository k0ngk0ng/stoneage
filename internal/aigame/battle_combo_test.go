package aigame

import (
	"strings"
	"testing"
)

func TestComboJournalAndEventStream(t *testing.T) {
	s := &Session{}
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 2}}})
	s.applyEvent(stringEvent("B", "BA|0|0|"))
	raw := "BP|BY|rA|a0|f2|d3|p0|a5|f2|d1|p0|FF|BH|aA|r0|f2|d4|p0|FF|"
	s.applyEvent(stringEvent("B", raw))
	logs := s.BattleJournal().Battles[0].Logs
	if len(logs) != 4 {
		t.Fatalf("missing hits: %+v", logs)
	}
	for i, actor := range []int{0, 5} {
		e := logs[i+1]
		if e.Kind != "attack" || e.Actor != actor || e.Target != 10 || e.Recipient == nil || *e.Recipient != 10 || e.Hit != i+1 || e.Hits != 2 || e.Turn != 1 || !strings.Contains(e.Text, "合击") || !strings.HasPrefix(e.Raw, "BY|") {
			t.Fatalf("combo hit: %+v", e)
		}
	}
	if logs[1].Damage != 3 || logs[2].Damage != 1 {
		t.Fatal(logs)
	}
	batch := s.BattleEvents("", 0)
	effects := batch.Events[len(batch.Events)-1].Effects
	if len(effects) != 3 || effects[0].Text != logs[1].Text || effects[1].Hit != 2 {
		t.Fatal(effects)
	}
	// Matching damage echoes must not count a second time in training/history.
	s.applyEvent(stringEvent("B", "BD|rA|0|0|d3|p0|BD|rA|0|0|d1|p0|"))
	if len(s.BattleJournal().Battles[0].Logs) != 4 {
		t.Fatal("duplicate combo damage")
	}
	s.applyEvent(stringEvent("B", "BA|0|1|"))
	s.applyEvent(stringEvent("B", "BY|r0|aA|f2|d2|p0|aF|f3|d5|p0|FF|"))
	// The server emits the next menu before announcing the completed battle.
	s.applyEvent(stringEvent("B", "BA|0|2|"))
	s.applyEvent(stringEvent("B", "BU"))
	b := s.BattleJournal().Battles[0]
	if !b.Ended || b.Turn != 2 || b.Logs[4].Turn != 2 || b.Logs[5].Turn != 2 {
		t.Fatal(b)
	}
}

func TestComboReactionsAndMalformedRecords(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		recipient int
		known     bool
	}{
		{"BY|rA|a0|f402|d3|p1|FF|", 0, true},
		{"BY|rA|a0|f202|d3|p1|gF|FF|", 15, true},
		{"BY|rA|a0|f802|d3|p1|FF|", 10, true},
		{"BY|rA|a0|f20|d0|p0|FF|", 0, false},
	} {
		j := battleJournal{}
		j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 2}}})
		j.record(stringEvent("B", tc.raw))
		e := j.battles[0].Logs[1]
		if e.Kind != "attack" || (e.Recipient != nil) != tc.known || tc.known && *e.Recipient != tc.recipient {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{"BY|rA|", "BY|r14|a0|f2|d3|", "BY|rA|a14|f2|d3|", "BY|rA|a0|d3|", "BY|rA|a0|f2|dZZ|", "BY|rA|a0|f2|d3|a5|f2|", "BY|rA|a0|f2|d3|rB|", "BY|rA|a0|f2|d3|d4|"} {
		j := battleJournal{}
		j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 2}}})
		j.record(stringEvent("B", raw))
		if logs := j.battles[0].Logs; len(logs) != 2 || logs[1].Kind != "unknown" {
			t.Fatalf("partial/fabricated combo %q: %+v", raw, logs)
		}
	}
}

func TestPetDisobedienceDoesNotSwallowFollowingAttack(t *testing.T) {
	j := battleJournal{}
	j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 2}}})
	j.record(stringEvent("B", "BX|F|BH|aF|r5|f2|d7|p0|FF|"))
	logs := j.battles[0].Logs
	if len(logs) != 3 || logs[1].Kind != "disobedience" || logs[1].Actor != 15 || logs[2].Kind != "attack" || logs[2].Damage != 7 || logs[2].Target != 5 {
		t.Fatal(logs)
	}
}
