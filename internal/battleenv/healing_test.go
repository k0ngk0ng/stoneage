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

func TestNativeHealingEffectsAndMP(t *testing.T) {
	var command []string
	if os.Getenv("STONEAGE_BATTLE_ENV_COMMAND") == "" {
		t.Skip("explicit native engine required")
	}
	if err := json.Unmarshal([]byte(os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mode := range []int{1, 2} {
		for _, magic := range []int{10, 20} {
			s := Scenario{Seed: 1, Level: 35, Mode: mode, MaxTurns: 30, HealingMagic: magic}
			for i := 0; i < 2*mode; i++ {
				s.Builds = append(s.Builds, Build{30, 30, 30, 30})
			}
			state, e := engine.Reset(ctx, s)
			if e != nil {
				t.Fatal(e)
			}
			for _, view := range state.Views {
				if view.Own.MP != 100 || len(view.Magic) != 1 || !view.Magic[0].IDKnown || view.Magic[0].ID != int32(magic) {
					t.Fatal("loadout not publicly observed", view.Own, view.Magic)
				}
			}
			// First exchange actual attacks, then heal. No hidden HP injection.
			cost := int32(8)
			if magic == 20 {
				cost = 20
			}
			advance := func(kind string, oneCaster bool) {
				t.Helper()
				plans := make([][]aigame.BattleSelection, len(state.Views))
				for i, v := range state.Views {
					action := kind
					if kind == "magic" && (v.Own.MP < cost || oneCaster && i%mode != 0) {
						action = "guard"
					}
					target := int32((1-i/mode)*10 + i%mode)
					if action == "magic" {
						target = v.Battle.MyNo
						if magic == 20 {
							target = 20 + int32(i/mode)
						}
					}
					id := ""
					for _, c := range v.Candidates {
						if c.Actor == "player" && c.Kind == action && (action == "guard" || c.Target == target) {
							id = c.ID
							break
						}
					}
					if id == "" {
						t.Fatalf("missing %s for member %d: %+v", kind, i, v.Candidates)
					}
					plans[i] = []aigame.BattleSelection{{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: id}}
				}
				state, e = engine.Advance(ctx, plans)
				if e != nil {
					t.Fatal(e)
				}
			}
			advance("attack", false)
			before := make([]int32, len(state.Views))
			for i, v := range state.Views {
				before[i] = v.Own.HP
				if before[i] >= v.Own.MaxHP {
					t.Fatal("damage setup did not wound member")
				}
			}
			// One caster must recover the entire side, not merely themselves.
			advance("magic", magic == 20)
			for i, v := range state.Views {
				wantMP := int32(100) - cost
				if magic == 20 && i%mode != 0 {
					wantMP = 100
				}
				if v.Own.HP <= before[i] || v.Own.MP != wantMP {
					t.Fatalf("heal %d mode %d member %d didn't restore HP/consume MP: %+v", magic, mode, i, v.Own)
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
				t.Fatal("no public healing effect")
			}
			// Repeated casts exhaust MP; the live shared candidate mask must then
			// remove magic, even though the equipment and spell ID remain known.
			for {
				affordable := false
				for _, v := range state.Views {
					affordable = affordable || v.Own.MP >= cost
				}
				if !affordable {
					break
				}
				advance("magic", false)
			}
			for _, v := range state.Views {
				for _, c := range v.Candidates {
					if c.Kind == "magic" {
						t.Fatal("unaffordable spell remained", c)
					}
				}
			}
			t.Logf("mode=%d magic=%d HP recovery, public effect and MP exhaustion passed", mode, magic)
		}
	}
}
