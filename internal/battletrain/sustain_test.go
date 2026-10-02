package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestRuleOpponentPoolPreservesOldResume(t *testing.T) {
	c := DefaultRunConfig()
	if len(c.ruleOpponents()) != 5 {
		t.Fatal("new runs omitted sustain")
	}
	c.RuleOpponents = nil
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	var restored RunConfig
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(restored.ruleOpponents(), []string{"basic", "focus", "guard-break", "defensive"}) {
		t.Fatal("old resume silently changed opponents")
	}
	for _, names := range [][]string{{"sustain", "sustain"}, {"made-up"}} {
		restored.RuleOpponents = names
		if restored.Validate() == nil {
			t.Fatal("invalid pool accepted")
		}
	}
	version, e := (Policy{Rule: "sustain"}).Version()
	if e != nil {
		t.Fatal(e)
	}
	old, _ := (Policy{Rule: "focus"}).Version()
	if version == old {
		t.Fatal("teacher identities collide")
	}
}

func TestNativeSustainTeacherAndImitation(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	counts := map[string]int{}
	for _, mode := range []int{1, 2, 3, 4, 5} {
		var episodes []Trajectory
		for _, magic := range []int{0, 10, 20} {
			c := DefaultRunConfig()
			c.Mode = mode
			c.HealingMagic = magic
			c.HealingItems = 6
			c.ReservePets = 2
			c.MaxTurns = 100
			s, group := scenarioFor(c, uint64(magic+mode)*4)
			tr, e := CollectPolicies(ctx, engine, s, [2]Policy{{Rule: "sustain"}, {Rule: "focus"}}, [2]*rand.Rand{}, group, engine.Metadata().Rules)
			if e != nil {
				t.Fatal(e)
			}
			for _, step := range tr[0].Steps {
				for i, j := range step.Choices {
					c := step.Frame.Slots[i].Candidates[j]
					if c.Features[30] == 1 {
						counts["switch"]++
					}
					if c.Features[29] == 1 {
						counts["item"]++
					} else if c.Features[22] == 1 {
						if c.Features[25] == 1 {
							counts["group-heal"]++
						} else {
							counts["single-heal"]++
						}
					}
				}
			}
			// Native public effects demonstrate actual recovery and switching,
			// rather than counting only proposed candidate IDs.
			batches := []Trajectory{tr[0]}
			for _, trajectory := range batches {
				for _, step := range trajectory.Steps {
					for _, event := range step.History.Events {
						for _, fx := range event.Effects {
							if fx.Resource == "hp" && fx.Delta != nil && *fx.Delta > 0 {
								counts["public-recovery"]++
							}
							if fx.Kind == "pet_summon" {
								counts["public-summon"]++
							}
						}
					}
				}
			}
			episodes = append(episodes, tr[0])
			t.Logf("mode=%d magic=%d turns=%d terminated=%v", mode, magic, len(tr[0].Steps), tr[0].Terminated)
		}
		model := testModel(t)
		adam := &battlenet.Adam[float32]{}
		config := DefaultWarmupConfig().Update
		config.SequenceLength = 4
		report, e := Imitate(ctx, model, adam, episodes, config)
		if e != nil || report.AfterPolicy == report.BeforePolicy {
			t.Fatal("teacher trajectories failed to train", e)
		}
	}
	for _, kind := range []string{"switch", "item", "single-heal", "group-heal", "public-recovery", "public-summon"} {
		if counts[kind] == 0 {
			t.Fatal("native coverage missing", kind, counts)
		}
	}
	t.Logf("real teacher actions and public effects: %v", counts)
}
