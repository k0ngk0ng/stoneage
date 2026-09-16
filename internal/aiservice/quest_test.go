package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestSubmittedWindowIsVisibleButNotActionableTaskEvidence(t *testing.T) {
	snapshot := aigame.Snapshot{ActiveWindow: &aigame.WindowSnapshot{ObjectID: 42, Sequence: 231, Open: true, Submitted: true}}
	o := ProjectObservation(aimcp.Binding{}, snapshot)
	if o.ActiveWindow == nil || !o.ActiveWindow.Submitted || !o.ActiveWindow.Open {
		t.Fatal("local submission was lost or replaced with invented closure")
	}
	o.Ready = true
	if len(projectAutomationObservation(o).Windows) != 0 {
		t.Fatal("submitted window became an actionable task window")
	}
}

func TestEmbeddedRidingTalkMatchesNPCExecutorContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "aiknowledge", "tasks", "riding-basic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task aiknowledge.TaskDefinition
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	// riderman.create places the actor at (61,45). Movement targets the
	// adjacent interaction tile, while npc.talk identifies the actor itself.
	spec := NPCSpec{Alias: "riderman", Floor: 1040, X: 61, Y: 45}
	found := false
	for _, step := range task.Steps {
		if step.Action.Skill != "npc.talk" {
			continue
		}
		found = true
		args, err := decodeNPCTalk(step.Action.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateTalkArguments(spec, args); err != nil {
			t.Fatalf("embedded talk action is rejected by the NPC executor: %v", err)
		}
	}
	if !found {
		t.Fatal("riding task has no NPC conversation")
	}
}

func TestAutomationProjectionPreservesAuthoritativeTaskConditions(t *testing.T) {
	observed := aimcp.Observation{
		Ready: true, CharacterID: "player", Character: aimcp.Entity{ID: "player", HP: 10, Alive: true},
		Skills: map[string]int{"learn_ride": 40}, Flags: map[string]bool{"end:7": true, "now:8": false},
		OwnProgress:  map[string]int{"end:0": 128},
		Pets:         []aimcp.Entity{{ID: "pet-stable", Level: 5, Skills: []string{"attack"}}, {Level: 99}},
		ActiveWindow: &aimcp.WindowState{ObjectID: 42, Sequence: 110, Open: true},
		Windows:      []aimcp.WindowState{{ObjectID: 99, Sequence: 100, Open: true}},
	}
	projected := projectAutomationObservation(observed)
	for _, condition := range []automation.Condition{
		{Kind: "character_skill_level", ID: "learn_ride", Value: 40},
		{Kind: "flag_set", ID: "end:7"},
		{Kind: "flag_clear", ID: "now:8"},
		{Kind: "window_sequence", ID: "42", Value: 110},
		{Kind: "pet_level", ID: "pet-stable", Value: 5},
	} {
		if !condition.Match(projected) {
			t.Fatalf("confirmed condition was lost: %+v", condition)
		}
	}
	if (automation.Condition{Kind: "window_sequence", ID: "99", Value: 100}).Match(projected) {
		t.Fatal("historical window became current task evidence")
	}
	if (automation.Condition{Kind: "flag_clear", ID: "unobserved"}).Match(projected) {
		t.Fatal("missing flag became confirmed false")
	}
	if len(projected.Pets) != 1 || projected.OwnProgress["end:0"] != 128 {
		t.Fatalf("pet identity or own progress lost: %+v", projected)
	}
	projected.Skills["learn_ride"] = 99
	projected.Pets[0].Skills[0] = "changed"
	if observed.Skills["learn_ride"] != 40 || observed.Pets[0].Skills[0] != "attack" {
		t.Fatal("automation mutated the source observation")
	}
}

func TestAutomationProjectionDoesNotConfirmClosedOrUnreadyWindow(t *testing.T) {
	for _, observed := range []aimcp.Observation{
		{Ready: true, ActiveWindow: &aimcp.WindowState{ObjectID: 42, Sequence: 110}},
		{ActiveWindow: &aimcp.WindowState{ObjectID: 42, Sequence: 110, Open: true}},
	} {
		if len(projectAutomationObservation(observed).Windows) != 0 {
			t.Fatal("unavailable window became task evidence")
		}
	}
}

func TestQuestSelectedPetRequiresOwnedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pets     []aigame.PetSnapshot
		accepted bool
	}{
		{"owned", []aigame.PetSnapshot{{StableID: "owned", IdentityKnown: true}}, true},
		{"unknown", []aigame.PetSnapshot{{StableID: "owned"}}, false},
		{"foreign", []aigame.PetSnapshot{{StableID: "other", IdentityKnown: true}}, false},
		{"duplicate", []aigame.PetSnapshot{{StableID: "owned", IdentityKnown: true}, {StableID: "owned", IdentityKnown: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, game := gameFixture(t)
			game.snapshot.Pets = tc.pets
			builder := &QuestPlans{Backend: backend, CharacterID: backend.Binding.CharacterID, Planner: aiplanner.New(&aiknowledge.Knowledge{})}
			_, err := builder.Task(context.Background(), aimcp.TaskRequest{TaskID: "missing-task", Parameters: map[string]json.RawMessage{"selected_pet_id": json.RawMessage(`"owned"`)}})
			// Reaching the compiler's missing-task error proves the identity gate passed.
			if errors.Is(err, aiplanner.ErrTaskNotFound) != tc.accepted || err == nil {
				t.Fatalf("accepted=%v err=%v", tc.accepted, err)
			}
			if game.writes != 0 {
				t.Fatal("pet binding wrote gameplay")
			}
		})
	}
}

func TestQuestPetBindingRejectsUnavailableCharacter(t *testing.T) {
	for _, observed := range []automation.Observation{
		{CharacterID: "c", Ready: true, Pets: []automation.Entity{{ID: "p"}}},
		{CharacterID: "c", Connected: true, Pets: []automation.Entity{{ID: "p"}}},
		{CharacterID: "other", Connected: true, Ready: true, Pets: []automation.Entity{{ID: "p"}}},
	} {
		if err := ValidateQuestPetBinding(observed, "c", "p"); err == nil {
			t.Fatal("accepted stale or other character state")
		}
	}
}

func TestQuestChainParameterIsStrictBoolean(t *testing.T) {
	builder := &QuestPlans{Planner: aiplanner.New(&aiknowledge.Knowledge{}), CharacterID: "account:0"}
	for _, raw := range []string{`null`, `"true"`, `1`, `{}`} {
		_, err := builder.Task(context.Background(), aimcp.TaskRequest{TaskID: "missing", Parameters: map[string]json.RawMessage{"include_dependencies": json.RawMessage(raw)}})
		if !errors.Is(err, aimcp.ErrInvalidParams) {
			t.Fatalf("%s accepted: %v", raw, err)
		}
	}
	for _, raw := range []string{`true`, `false`} {
		_, err := builder.Task(context.Background(), aimcp.TaskRequest{TaskID: "missing", Parameters: map[string]json.RawMessage{"include_dependencies": json.RawMessage(raw)}})
		if err == nil || errors.Is(err, aimcp.ErrInvalidParams) {
			t.Fatalf("%s did not reach compiler: %v", raw, err)
		}
	}
}
