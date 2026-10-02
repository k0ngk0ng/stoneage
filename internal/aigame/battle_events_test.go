package aigame

import (
	"reflect"
	"strings"
	"testing"
)

func TestBattleEffectsPreserveWireOrderAndSuppressDamageEcho(t *testing.T) {
	s := &Session{}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 0}}})
	s.applyEvent(stringEvent("B", "BP|0|0|14"))
	s.applyEvent(stringEvent("B", "BC|0|0|Hero||1|1|64|64|4|A|Enemy||1|1|64|64|0|"))
	before := s.BattleEvents("", 0)
	s.applyEvent(stringEvent("B", "BD|r0|0|1|d14|p0|FF|BH|aA|r0|f0|dA|p0|FF|BM|0|3|FF|"))
	// This is the hit's matching resource echo, not a second damage effect.
	s.applyEvent(stringEvent("B", "BD|r0|0|0|dA|p0|FF|"))
	s.applyEvent(stringEvent("B", "BD|r0|0|1|d5|p0|FF|"))
	batch := s.BattleEvents(before.Stream, before.Cursor)
	var kinds []string
	var delta []int
	for _, event := range batch.Events {
		for _, effect := range event.Effects {
			kinds = append(kinds, effect.Kind)
			if effect.Delta != nil {
				delta = append(delta, *effect.Delta)
			}
		}
	}
	if !reflect.DeepEqual(kinds, []string{"BD", "attack", "BM", "BD"}) || !reflect.DeepEqual(delta, []int{20, 5}) {
		t.Fatal("public effect order or resource echo changed", kinds, delta)
	}
}

func TestBattleEventCursorPrivacyAndReconnect(t *testing.T) {
	s := &Session{}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	s.applyEvent(stringEvent("TK", "private chat"))
	s.applyEvent(stringEvent("B", "BP|0|0|14"))
	first := s.BattleEvents("", 0)
	if first.Stream == "" || first.Gap || len(first.Events) != 1 || first.Events[0].Function != "B" {
		t.Fatal(first)
	}
	s.applyEvent(stringEvent("B", "BA|0|1"))
	next := s.BattleEvents(first.Stream, first.Cursor)
	if next.Gap || len(next.Events) != 1 || next.Cursor != first.Cursor+1 {
		t.Fatal(next)
	}
	if len(s.BattleEvents(next.Stream, next.Cursor).Events) != 0 {
		t.Fatal("duplicate events")
	}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	reset := s.BattleEvents(next.Stream, next.Cursor)
	if !reset.Gap || reset.Stream == next.Stream || len(reset.Events) != 0 {
		t.Fatal("reconnect stream not reset", reset)
	}
}

func TestBattleEventEvictionIsExplicit(t *testing.T) {
	s := &Session{}
	s.applyEvent(stringEvent("CharLogin", "successful"))
	first := s.BattleEvents("", 0)
	for i := 0; i < 4100; i++ {
		s.applyEvent(stringEvent("B", "unknown"))
	}
	b := s.BattleEvents(first.Stream, 0)
	if !b.Gap || len(b.Events) != 4096 || b.Cursor != 4100 {
		t.Fatal("eviction", b.Gap, len(b.Events), b.Cursor)
	}
	s.applyEvent(stringEvent("B", strings.Repeat("x", 65537)))
	b = s.BattleEvents(b.Stream, b.Cursor)
	if !b.Gap || len(b.Events) != 0 {
		t.Fatal("oversize not reported")
	}
}

func TestBattleEventSharedEffectsAreImmutable(t *testing.T) {
	s := &Session{}
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 0}}})
	s.applyEvent(stringEvent("B", "BP|0|0|14"))
	s.applyEvent(stringEvent("B", "BC|0|0|Hero||1|1|64|64|4|A|Enemy||1|1|64|64|0|"))
	s.applyEvent(stringEvent("B", "BM|a|2|"))
	batch := s.BattleEvents("", 0)
	var last BattleEvent
	for _, event := range batch.Events {
		if len(event.Effects) > 0 {
			last = event
		}
	}
	if len(last.Effects) == 0 || last.Effects[0].Status == nil {
		t.Fatal("missing structured status effect", batch)
	}
	*last.Effects[0].Status = 999
	for _, event := range s.BattleEvents("", 0).Events {
		for _, effect := range event.Effects {
			if effect.Status != nil && *effect.Status == 999 {
				t.Fatal("caller mutated shared journal")
			}
		}
	}
}
