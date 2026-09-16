package aiplanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestBuildTaskResolvesSelectedPetAcrossConditionsAndDependencies(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	ref := root.Preconditions[0].Evidence
	petCondition := aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8}

	root.Preconditions = append(root.Preconditions, aiknowledge.Precondition{
		MachineCondition: petCondition,
		Evidence:         ref,
	})
	root.Success = append(root.Success, aiknowledge.SuccessCondition{
		MachineCondition: petCondition,
		Description:      "selected pet reached the reviewed level",
		Evidence:         ref,
	})
	root.Steps[0].Preconditions = append(root.Steps[0].Preconditions, petCondition)
	root.Steps[0].SuccessConditions = append(root.Steps[0].SuccessConditions, petCondition)

	dependency := dependencyTask(root, "prepare-pet", nil)
	dependency.Success = []aiknowledge.SuccessCondition{{
		MachineCondition: petCondition,
		Description:      "dependency completed for the selected pet",
		Evidence:         ref,
	}}
	root.Dependencies = []string{dependency.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, dependency}

	plan, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{
		CharacterID: "acct:0", SelectedPetID: "stable-pet-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	petCount := 0
	check := func(condition automation.Condition) {
		t.Helper()
		if condition.Kind == "pet_level" {
			petCount++
			if condition.ID != "stable-pet-a" || condition.Value != 8 {
				t.Errorf("pet condition was not resolved: %+v", condition)
			}
		}
		if condition.ID == "$selected_pet" || condition.ID == "$foo" {
			t.Errorf("unresolved condition reference persisted: %+v", condition)
		}
	}
	for _, condition := range plan.Preconditions {
		check(condition)
	}
	for _, condition := range plan.Completion {
		check(condition)
	}
	for _, step := range plan.Steps {
		for _, condition := range step.Preconditions {
			check(condition)
		}
		for _, condition := range step.Success {
			check(condition)
		}
	}
	// The root has one pet precondition, one completion condition and one in
	// each of the first step's precondition/success lists. The dependency's
	// completion is injected as the second root entry guard.
	if petCount != 5 {
		t.Fatalf("resolved pet condition count = %d, want 5; plan=%+v", petCount, plan)
	}

	// Resolution is a compiler concern. The reusable definitions retain the
	// placeholder so the same task can be bound to another owned pet later.
	if got := root.Preconditions[len(root.Preconditions)-1].ID; got != "$selected_pet" {
		t.Fatalf("root knowledge precondition mutated: %q", got)
	}
	if got := root.Success[len(root.Success)-1].ID; got != "$selected_pet" {
		t.Fatalf("root knowledge completion mutated: %q", got)
	}
	if got := root.Steps[0].Preconditions[len(root.Steps[0].Preconditions)-1].ID; got != "$selected_pet" {
		t.Fatalf("root knowledge step precondition mutated: %q", got)
	}
	if got := root.Steps[0].SuccessConditions[len(root.Steps[0].SuccessConditions)-1].ID; got != "$selected_pet" {
		t.Fatalf("root knowledge step success mutated: %q", got)
	}
	if got := dependency.Success[0].ID; got != "$selected_pet" {
		t.Fatalf("dependency knowledge completion mutated: %q", got)
	}
}

func TestBuildTaskRejectsUnresolvedPetReferences(t *testing.T) {
	tests := []struct {
		name       string
		condition  aiknowledge.MachineCondition
		selectedID string
	}{
		{
			name:       "missing binding",
			condition:  aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
			selectedID: "",
		},
		{
			name:       "whitespace binding",
			condition:  aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
			selectedID: "   ",
		},
		{
			name:       "binding with surrounding whitespace",
			condition:  aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
			selectedID: " stable-pet-a ",
		},
		{
			name:       "unknown reference",
			condition:  aiknowledge.MachineCondition{Kind: "pet_level", ID: "$other_pet", Value: 8},
			selectedID: "stable-pet-a",
		},
		{
			name:       "placeholder on non-pet condition",
			condition:  aiknowledge.MachineCondition{Kind: "flag_set", ID: "$selected_pet"},
			selectedID: "stable-pet-a",
		},
		{
			name:       "unresolved binding reference",
			condition:  aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
			selectedID: "$other_pet",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			knowledge, task := verifiedFixture(t)
			ref := task.Preconditions[0].Evidence
			task.Preconditions = append(task.Preconditions, aiknowledge.Precondition{
				MachineCondition: tc.condition,
				Evidence:         ref,
			})
			knowledge.TaskDefinitions[0] = task
			_, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{
				CharacterID: "acct:0", SelectedPetID: tc.selectedID,
			})
			if !errors.Is(err, ErrInvalidCondition) {
				t.Fatalf("error %v does not contain %v", err, ErrInvalidCondition)
			}
		})
	}
}

func TestBuildTaskKeepsLiteralPetIDWithoutBinding(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	ref := task.Preconditions[0].Evidence
	task.Preconditions = append(task.Preconditions, aiknowledge.Precondition{
		MachineCondition: aiknowledge.MachineCondition{Kind: "pet_level", ID: "stable-pet-a", Value: 8},
		Evidence:         ref,
	})
	knowledge.TaskDefinitions[0] = task

	plan, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Preconditions[len(plan.Preconditions)-1]; got != (automation.Condition{Kind: "pet_level", ID: "stable-pet-a", Value: 8}) {
		t.Fatalf("literal stable pet ID changed: %+v", got)
	}
}

func TestResolvedPetIDIsPersistedAndEngineGatesMissingOrUnderleveledPet(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	ref := task.Preconditions[0].Evidence
	task.Preconditions = append(task.Preconditions, aiknowledge.Precondition{
		MachineCondition: aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
		Evidence:         ref,
	})
	knowledge.TaskDefinitions[0] = task
	plan, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{
		CharacterID: "acct:0", SelectedPetID: "stable-pet-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		pets  []automation.Entity
		start bool
	}{
		{
			name:  "bound pet meets level",
			pets:  []automation.Entity{{ID: "stable-pet-a", Level: 8, HP: 100}},
			start: true,
		},
		{
			name:  "bound pet is missing",
			pets:  []automation.Entity{{ID: "other-pet", Level: 99, HP: 100}},
			start: false,
		},
		{
			name:  "bound pet is underleveled",
			pets:  []automation.Entity{{ID: "stable-pet-a", Level: 7, HP: 100}},
			start: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			game := &preparationGame{observation: automation.Observation{
				CharacterID: "acct:0", Connected: true, Ready: true, Gold: 6000,
				Character: automation.Entity{ID: "acct:0", Level: 10, HP: 100},
				Pets:      tc.pets,
			}}
			engine := automation.Engine{Store: store, Game: game}
			checkpoint, startErr := engine.Start(context.Background(), plan)
			if (startErr == nil) != tc.start {
				t.Fatalf("start=%v, checkpoint=%+v, error=%v", tc.start, checkpoint, startErr)
			}
			stored, loadErr := store.Load(context.Background(), plan.ID)
			if tc.start {
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				for _, condition := range stored.Plan.Preconditions {
					if condition.Kind == "pet_level" && condition.ID != "stable-pet-a" {
						t.Fatalf("persisted pet condition was not concrete: %+v", condition)
					}
				}
				// Continue through a fresh engine loading the durable plan;
				// another high-level pet must not satisfy the original binding.
				resumedEngine := automation.Engine{Store: store, Game: game}
				game.observation.Pets = []automation.Entity{{ID: "other-pet", Level: 99, HP: 100}}
				paused, err := resumedEngine.Tick(context.Background(), plan.ID)
				if err != nil || paused.Status != automation.Paused {
					t.Fatalf("changed pet did not pause before submission: %+v %v", paused, err)
				}
				for _, pets := range [][]automation.Entity{
					{{ID: "other-pet", Level: 99, HP: 100}},
					{{ID: "stable-pet-a", Level: 7, HP: 100}},
				} {
					game.observation.Pets = pets
					checkpoint, err := resumedEngine.Resume(context.Background(), plan.ID)
					if err == nil || checkpoint.Status != automation.Paused {
						t.Fatalf("resumed with missing/underleveled original pet: %+v %v", checkpoint, err)
					}
				}
				game.observation.Pets = []automation.Entity{{ID: "stable-pet-a", Level: 8, HP: 100}}
				checkpoint, err := resumedEngine.Resume(context.Background(), plan.ID)
				if err != nil || checkpoint.Status != automation.Running {
					t.Fatalf("original pet could not resume: %+v %v", checkpoint, err)
				}
			} else if !errors.Is(loadErr, automation.ErrNotFound) {
				t.Fatalf("rejected start created a checkpoint: %v", loadErr)
			}
			if game.writes != 0 {
				t.Fatal("preflight attempted gameplay")
			}
		})
	}
}
