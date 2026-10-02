package battlepolicy

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"testing"
)

func TestHealingItemStockVisibility(t *testing.T) {
	for _, side := range []int{0, 1} {
		v := fixture(2, side)
		v[0].InventoryKnown = true
		v[0].Inventory = []aigame.InventoryItem{{Index: 5, TemplateID: 1234, TemplateIDKnown: true}, {Index: 6, TemplateID: 1234, TemplateIDKnown: true}}
		v[1].InventoryKnown = true // an observed empty inventory, not missing data
		check := func(count, known float32) {
			t.Helper()
			f, e := Encode(v, History{First: true})
			if e != nil {
				t.Fatal(e)
			}
			for _, x := range f.Entities {
				if x[eAlly] == 0 || x[ePet] == 1 {
					if x[eHealingItems] != 0 || x[eHealingItemsKnown] != 0 {
						t.Fatal("private stock leaked", x)
					}
					continue
				}
				if x[eSeat] == 0 {
					if x[eHealingItems] != count || x[eHealingItemsKnown] != known {
						t.Fatal("wrong stock", x)
					}
				} else if x[eHealingItems] != 0 || x[eHealingItemsKnown] != 1 {
					t.Fatal("known empty inventory lost", x)
				}
			}
		}
		check(float32(2)/15, 1)
		v[0].Inventory[1].TemplateIDKnown = false
		check(0, 0)
		v[0].Inventory[1].TemplateIDKnown = true
		v[0].InventoryKnown = false
		check(0, 0)
	}
}

func TestHealingItemSemantics(t *testing.T) {
	rows := map[int32]int{0: 0, 5: 1, 10: 2}
	e := make([][EntityFeatures]float32, 3)
	for i := range e {
		e[i][eAlive], e[i][eHPKnown], e[i][eHPRatio] = 1, 1, .5
	}
	c := aigame.BattleCandidate{ID: "item", Actor: "player", Kind: "item", Index: 5, Target: 5, ItemTemplateID: 1234, ItemTemplateIDKnown: true}
	x := encodeCandidate(c, 0, 0, rows, e)
	if !x.Supported || x.Target != 1 || x.Features[29] != 1 || x.Features[22] != 1 || x.Features[23] != scale(20) || x.Features[24] != 0 || x.Features[4] != 0 {
		t.Fatal(x)
	}
	for _, mutate := range []func(*aigame.BattleCandidate){
		func(c *aigame.BattleCandidate) { c.ItemTemplateIDKnown = false },
		func(c *aigame.BattleCandidate) { c.ItemTemplateID = 2344 },
		func(c *aigame.BattleCandidate) { c.Index = 1 },
		func(c *aigame.BattleCandidate) { c.Index = 20 },
		func(c *aigame.BattleCandidate) { c.Actor = "pet" },
		func(c *aigame.BattleCandidate) { c.Target = 20 },
	} {
		bad := c
		mutate(&bad)
		if encodeCandidate(bad, 0, 0, rows, e).Supported {
			t.Fatal("unsupported item admitted", bad)
		}
	}
	e[1][eAlive] = 0
	if encodeCandidate(c, 0, 0, rows, e).Supported {
		t.Fatal("healing dead target")
	}
}
