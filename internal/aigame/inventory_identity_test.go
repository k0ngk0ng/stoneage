package aigame

import "testing"

func TestNativeInventoryChangesInvalidateTemplateIdentity(t *testing.T) {
	for _, kind := range []string{"indexed", "full", "swap"} {
		state := newGameState(true)
		state.applySystem("AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=80|items=9,4")
		if !state.snapshot.AI.ItemsKnown {
			t.Fatal("missing initial template")
		}
		switch kind {
		case "indexed":
			state.applyIndexedInventory("9|Other||0||1|1|0|1|0")
		case "full":
			state.applyUnindexedInventory("Other||0||1|1|0|1|0")
		case "swap":
			state.swapInventory(9, 10)
		}
		if state.snapshot.AI.ItemsKnown || len(state.snapshot.AI.Items) != 0 {
			t.Fatalf("%s kept stale template", kind)
		}
		if !state.snapshot.AI.Received || state.snapshot.AI.LearnRide != 80 {
			t.Fatal("unrelated own-state erased")
		}
		state.applySystem("AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=80|items=10,4")
		if !state.snapshot.AI.ItemsKnown || state.snapshot.AI.Items[0].Slot != 10 {
			t.Fatal("fresh template response was not restored")
		}
	}
}
