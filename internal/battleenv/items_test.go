package battleenv

import (
	"context"
	"encoding/json"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"io"
	"os"
	"testing"
	"time"
)

func TestNativeItemsRecoverConsumeAndReset(t *testing.T) {
	var command []string
	if os.Getenv("STONEAGE_BATTLE_ENV_COMMAND") == "" {
		t.Skip("explicit native engine required")
	}
	if err := json.Unmarshal([]byte(os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, err := Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, tc := range []struct {
		mode        int
		pets, enemy bool
	}{{1, false, false}, {1, false, true}, {1, true, false}, {2, false, false}, {2, true, true}} {
		s := Scenario{Seed: 42, Level: 35, Mode: tc.mode, MaxTurns: 20, HealingItems: 3}
		for i := 0; i < 2*tc.mode; i++ {
			s.Builds = append(s.Builds, Build{30, 30, 30, 30})
			if tc.pets {
				s.PetBuilds = append(s.PetBuilds, Build{30, 30, 30, 30})
			}
		}
		state, e := engine.Reset(ctx, s)
		if e != nil {
			t.Fatal(e)
		}
		stock := func(want int) {
			t.Helper()
			for _, v := range state.Views {
				if !v.InventoryKnown || len(v.Inventory) != want {
					t.Fatal("bad public inventory", v.InventoryKnown, v.Inventory)
				}
				for _, item := range v.Inventory {
					if !item.TemplateIDKnown || item.TemplateID != 1234 {
						t.Fatal("unknown/wrong item", item)
					}
				}
				for _, c := range v.Candidates {
					if c.Kind == "item" && (want == 0 || !c.ItemTemplateIDKnown || c.ItemTemplateID != 1234) {
						t.Fatal("stale item action", c)
					}
				}
			}
		}
		stock(3)
		advance := func(item bool) {
			t.Helper()
			plans := make([][]aigame.BattleSelection, len(state.Views))
			for i, v := range state.Views {
				side := i / tc.mode
				targetSide := 1 - side
				if item && !tc.enemy {
					targetSide = side
				}
				target := int32(targetSide*10 + (i%tc.mode+1)%tc.mode)
				if tc.pets {
					target += 5
				}
				kind := "attack"
				if item {
					kind = "item"
				}
				for _, actor := range []string{"player", "pet"} {
					if actor == "pet" && !tc.pets {
						continue
					}
					found := false
					for _, c := range v.Candidates {
						ok := c.Actor == actor
						if actor == "player" {
							ok = ok && c.Kind == kind && c.Target == target
						} else {
							ok = ok && c.Kind == "wait"
						}
						if ok {
							plans[i] = append(plans[i], aigame.BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: c.ID})
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("missing %s/%s target %d", actor, kind, target)
					}
				}
			}
			state, e = engine.Advance(ctx, plans)
			if e != nil {
				t.Fatal(e)
			}
		}
		// Native consumption still happens when HP is already capped; the
		// public movie's nominal recovery must not invent extra current HP.
		advance(true)
		stock(2)
		for _, p := range state.Views[0].Battle.Participants {
			if p.HP != p.MaxHP {
				t.Fatal("full-HP item changed capped HP", p)
			}
		}
		for attempt := 0; attempt < 6; attempt++ {
			advance(false)
			wounded := true
			for _, p := range state.Views[0].Battle.Participants {
				if (p.BattleID%10 >= 5) == tc.pets {
					wounded = wounded && p.HP < p.MaxHP && p.HP > 0
				}
			}
			if wounded {
				break
			}
		}
		before := map[int32]int32{}
		for _, p := range state.Views[0].Battle.Participants {
			if (p.BattleID%10 >= 5) == tc.pets {
				if p.HP >= p.MaxHP {
					t.Fatal("attack failed to wound", p)
				}
				before[p.BattleID] = p.HP
			}
		}
		advance(true)
		stock(1)
		for _, p := range state.Views[0].Battle.Participants {
			if hp, ok := before[p.BattleID]; ok && p.HP <= hp {
				t.Fatal("item did not recover actual HP", p, hp)
			}
		}
		public := false
		for _, event := range state.Events[0].Events {
			for _, fx := range event.Effects {
				if fx.Kind == "BD" && fx.Resource == "hp" && fx.Delta != nil && *fx.Delta > 0 {
					public = true
				}
			}
		}
		if !public {
			t.Fatal("missing public recovery effect")
		}
		advance(true)
		stock(0)
		t.Logf("mode=%d pet target=%v enemy=%v: HP cap respected, HP restored and all three items consumed", tc.mode, tc.pets, tc.enemy)
	}
	// Repeated maximum-size resets would exhaust the 256-item native table if
	// any unconsumed inventory survived disposal. No account or hidden HP edits.
	s := Scenario{Seed: 1, Level: 35, Mode: 5, MaxTurns: 1, HealingItems: 15, HealingMagic: 20}
	for i := 0; i < 10; i++ {
		s.Builds = append(s.Builds, Build{30, 30, 30, 30})
	}
	for repeat := 0; repeat < 3; repeat++ {
		state, e := engine.Reset(ctx, s)
		if e != nil {
			t.Fatal("reset leaked native inventory", e)
		}
		for _, v := range state.Views {
			if len(v.Inventory) != 16 {
				t.Fatal("incomplete maximum loadout", len(v.Inventory))
			}
		}
	}
}
