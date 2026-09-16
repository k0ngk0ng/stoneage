package aiplanner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type preparationGame struct {
	observation automation.Observation
	writes      int
}

func (g *preparationGame) Observe(context.Context) (automation.Observation, error) {
	return g.observation, nil
}
func (g *preparationGame) Execute(context.Context, automation.Action) error {
	g.writes++
	return nil
}

// Exercises knowledge -> compiler -> real engine/checkpoint boundary, so
// preparation constraints cannot disappear when compiling a reusable task.
func TestCompiledTaskEnforcesBothCharacterAndPetLevels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		character int
		pet       int
		ready     bool
	}{
		{"character too low", 9, 8, false},
		{"pet too low", 10, 7, false},
		{"unknown pet level", 10, 0, false},
		{"both qualified", 10, 8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			knowledge, task := verifiedFixture(t)
			ref := task.Preconditions[0].Evidence
			task.Preconditions = append(task.Preconditions,
				aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "character_level", Value: 10}, Evidence: ref},
				aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "pet_level", ID: "stable-pet-a", Value: 8}, Evidence: ref})
			knowledge.TaskDefinitions[0] = task
			plan, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"})
			if err != nil {
				t.Fatal(err)
			}
			store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			game := &preparationGame{observation: automation.Observation{
				CharacterID: "acct:0", Connected: true, Ready: true, Gold: 6000,
				Character: automation.Entity{Level: tc.character, HP: 100},
				Pets:      []automation.Entity{{ID: "stable-pet-a", Level: tc.pet, HP: 100}},
			}}
			engine := automation.Engine{Store: store, Game: game}
			_, err = engine.Start(context.Background(), plan)
			if (err == nil) != tc.ready {
				t.Fatalf("ready=%v, start error=%v", tc.ready, err)
			}
			if !tc.ready {
				if _, err := store.Load(context.Background(), plan.ID); err == nil {
					t.Fatal("rejected task acquired a checkpoint")
				}
			}
			if game.writes != 0 {
				t.Fatal("preflight attempted gameplay")
			}
		})
	}
}
