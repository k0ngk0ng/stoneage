package aiplanner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func adultTaskFixture(t *testing.T) (*aiknowledge.Knowledge, aiknowledge.TaskDefinition) {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data", "enemybase.txt")); os.IsNotExist(err) {
		t.Skip("source data not installed")
	}
	k, err := aiknowledge.Load(context.Background(), aiknowledge.Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := k.FindTask("adult-ceremony")
	if !ok || task.Status != aiknowledge.TaskUnverified {
		t.Fatalf("adult contract missing or invalid: %+v", task)
	}
	return k, task
}

func TestAdultCeremonyCannotExecuteWithoutPreparationAndAcceptance(t *testing.T) {
	k, task := adultTaskFixture(t)
	if task.ExecutionVerified || task.PreparationReviewed || task.EvidenceVerified {
		t.Fatal("draft was promoted")
	}
	_, err := New(k).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "human:0"})
	if !errors.Is(err, ErrTaskUnverified) {
		t.Fatalf("draft is executable: %v", err)
	}
}

func TestAdultCeremonyCompiledGuardsAndCompletion(t *testing.T) {
	k, task := adultTaskFixture(t)
	// Local test copy only: test the complete declaration through the compiler
	// and evaluator without recording a runtime acceptance claim or game writes.
	task.Status = aiknowledge.TaskVerified
	task.ExecutionVerified = true
	task.PreparationReviewed = true
	task.EvidenceVerified = true
	for i := range task.Evidence {
		task.Evidence[i].Verified = true
	}
	k.TaskDefinitions = []aiknowledge.TaskDefinition{task}
	plan, err := New(k).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "human:0"})
	if err != nil {
		t.Fatal(err)
	}
	makeObservation := func() automation.Observation {
		return automation.Observation{CharacterID: "human:0", Connected: true, Ready: true, Character: automation.Entity{Level: 35, HP: 100}, Flags: map[string]bool{"party:solo": true, "inventory:known": true, "now:4": false, "end:4": false}, OwnProgress: map[string]int{"backpack_used_slots": 0}, Inventory: map[string]int{}}
	}
	for _, tc := range []struct {
		name  string
		edit  func(*automation.Observation)
		ready bool
	}{
		{"qualified", func(o *automation.Observation) {}, true},
		{"native entry level is below departure recommendation", func(o *automation.Observation) { o.Character.Level = 30 }, false},
		{"unlimited funds do not waive level", func(o *automation.Observation) { o.Character.Level = 29; o.UnlimitedFunds = true }, false},
		{"party leader", func(o *automation.Observation) { o.Flags["party:solo"] = false }, false},
		{"unknown party", func(o *automation.Observation) { delete(o.Flags, "party:solo") }, false},
		{"one occupied slot", func(o *automation.Observation) { o.OwnProgress["backpack_used_slots"] = 1 }, false},
		{"unknown inventory", func(o *automation.Observation) { delete(o.Flags, "inventory:known") }, false},
		{"already started", func(o *automation.Observation) { o.Flags["now:4"] = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := makeObservation()
			tc.edit(&o)
			p, err := automation.EvaluatePreflight(context.Background(), plan, o, nil)
			if err != nil {
				t.Fatal(err)
			}
			if p.Ready != tc.ready {
				t.Fatalf("ready=%v want %v: %+v", p.Ready, tc.ready, p)
			}
		})
	}
	o := makeObservation()
	o.Flags["end:4"] = true
	o.Inventory["item:2418"] = 1
	if !plan.Complete(o) {
		t.Fatal("authoritative reward/flag/consumption should complete")
	}
	o.Inventory["item:2417"] = 15
	if plan.Complete(o) {
		t.Fatal("retained ritual items incorrectly accepted")
	}
	delete(o.Inventory, "item:2417")
	delete(o.Flags, "inventory:known")
	if plan.Complete(o) {
		t.Fatal("unknown inventory incorrectly proves consumption")
	}
	for _, s := range task.Steps {
		if s.Action.Skill == "item.stock" {
			t.Fatal("generic stocking conflicts with 15 empty slots")
		}
		if s.ID == "receive-ritual-items" {
			var args map[string]any
			if err := json.Unmarshal(s.Action.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			if args["window_sequence"] != float64(430) || args["choice"] != float64(1) {
				t.Fatal("batch grant must confirm native acceptance dialog")
			}
			for _, want := range []aiknowledge.MachineCondition{{Kind: "flag_set", ID: "party:solo"}, {Kind: "backpack_free_slots", Value: 15}, {Kind: "item_absent", ID: "item:2417"}} {
				found := false
				for _, c := range s.Preconditions {
					if c == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("grant missing guard %+v", want)
				}
			}
		}
	}
}
