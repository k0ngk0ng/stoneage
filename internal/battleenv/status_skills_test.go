package battleenv

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Exercise actual skill submission and observed status effects, not a table
// label or a synthetic successful result. Keep the victim guarded and healthy
// so conditional status success has repeated opportunities across fixed seeds.
func TestNativeStatusSkills(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
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
		skill  int32
		status int
	}{{60, 1}, {80, 4}, {90, 6}, {110, 3}} {
		observed := false
		for seed := 1; seed <= 12 && !observed; seed++ {
			// An almost all-vitality target has zero native status chance; keep
			// enough HP while retaining a nonzero chance under the real formula.
			scenario := Scenario{Seed: seed, Mode: 1, Level: 35, MaxTurns: 16, Builds: []Build{{50, 20, 25, 25}, {50, 20, 25, 25}}, PetBuilds: []Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
			state, e := engine.Reset(ctx, scenario)
			if e != nil {
				t.Fatal(e)
			}
			for side, view := range state.Views {
				found := map[int32]aigame.BattleCandidate{}
				var guard string
				for _, c := range view.Candidates {
					if c.Actor == "player" && c.Kind == "guard" && c.Ready {
						guard = c.ID
					}
					if c.Actor == "pet" && c.Kind == "skill" {
						found[c.SkillID] = c
					}
				}
				for _, skill := range []int32{1, 2, 3, 60, 80, 90, 110} {
					c, ok := found[skill]
					if !ok || guard == "" {
						t.Fatalf("side %d missing skill %d or player guard: %+v", side, skill, view.Candidates)
					}
					// Pet readiness follows the player's submission. Validate
					// each complete sequential plan with the real shared resolver.
					plan := []aigame.BattleSelection{
						{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: guard},
						{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: c.ID},
					}
					if _, e := engine.replay[side].ResolvePlan(int32(state.Turn), plan); e != nil {
						t.Fatalf("side %d cannot execute skill %d after guard: %v", side, skill, e)
					}
				}
			}
			for !state.Terminated && !state.Truncated && !observed {
				choices := make([][]aigame.BattleSelection, 2)
				for side, view := range state.Views {
					for _, actor := range []string{"player", "pet"} {
						id := ""
						for _, c := range view.Candidates {
							if c.Actor == actor && c.Kind == "wait" {
								id = c.ID
							}
						}
						for _, c := range view.Candidates {
							if c.Actor != actor {
								continue
							}
							if actor == "player" && c.Kind == "guard" || actor == "pet" && (side == 0 && c.SkillID == tc.skill && c.Target == 10 || side == 1 && c.SkillID == 2) {
								id = c.ID
								break
							}
						}
						if id == "" {
							t.Fatalf("no %s candidate side %d turn %d", actor, side, state.Turn)
						}
						choices[side] = append(choices[side], aigame.BattleSelection{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: id})
					}
				}
				state, e = engine.Advance(ctx, choices)
				if e != nil {
					t.Fatal(e)
				}
				for _, event := range state.Events[0].Events {
					for _, effect := range event.Effects {
						if effect.Kind == "BM" && effect.Target == 10 && effect.Status != nil && *effect.Status == tc.status {
							observed = true
							t.Logf("skill=%d public status=%d seed=%d turn=%d", tc.skill, tc.status, seed, state.Turn)
						}
					}
				}
			}
		}
		if !observed {
			t.Fatalf("skill %d never produced public status %d", tc.skill, tc.status)
		}
	}
}
