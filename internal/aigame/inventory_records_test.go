package aigame

import (
	"strings"
	"testing"
)

func TestInventoryRecordEscapesAndReplacement(t *testing.T) {
	for _, full := range []bool{false, true} {
		s := newGameState(false)
		s.applyIndexedInventory("19|old||0||1|0|1|0|0")
		record := `meat\zname|literal\yz|0|memo\zpipe\nline|24008|0|1|0|0`
		if full {
			s.applySystem("I" + strings.Repeat("|||||||||", 5) + record + "|next||0||2|0|1|0|0")
		} else {
			s.applyIndexedInventory("5|" + record + "|6|next||0||2|0|1|0|0")
		}
		item := s.inventory[5]
		if item.Name != "meat|name" || item.Name2 != `literal\z` || item.Memo != "memo|pipe\nline" || item.Graphic != 24008 || item.Target != 1 || s.inventory[6].Name != "next" {
			t.Fatalf("full=%v: escaped field shifted inventory: %+v", full, s.inventory)
		}
		_, old := s.inventory[19]
		if old == full {
			t.Fatal("full must replace, delta must retain unrelated slots")
		}
		s.applyIndexedInventory("5|||||||||")
		if _, exists := s.inventory[5]; exists || s.inventory[6].Name != "next" {
			t.Fatal("consumption tombstone damaged inventory")
		}
	}
}

func TestBattleItemIdentityTracksInventory(t *testing.T) {
	s := decisionFixture()
	s.AI.ItemsKnown = true
	s.Inventory = []InventoryItem{{Index: 5, Name: "meat", TemplateID: 1234, TemplateIDKnown: true, Field: 0, Target: 1}}
	v := NewBattleView(s)
	if !v.InventoryKnown {
		t.Fatal("missing inventory knownness")
	}
	count := 0
	for _, c := range v.Candidates {
		if c.Kind != "item" {
			continue
		}
		if c.ItemTemplateID != 1234 || !c.ItemTemplateIDKnown || c.Index != 5 || c.Target == 11 {
			t.Fatal("bad item candidate", c)
		}
		count++
	}
	if count != 3 {
		t.Fatal("missing own/enemy/pet targets", count)
	}
	s.Inventory[0].TemplateIDKnown = false
	s.AI.ItemsKnown = false
	unknown := NewBattleView(s)
	if unknown.ID == v.ID || unknown.InventoryKnown {
		t.Fatal("stale identity did not invalidate observation")
	}
}
