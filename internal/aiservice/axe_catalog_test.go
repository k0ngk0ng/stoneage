package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestAxeDraftHasExecutableWindowContractsAndOrderedDependencies(t *testing.T) {
	data := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data")
	if _, err := os.Stat(filepath.Join(data, "enemybase.txt")); os.IsNotExist(err) {
		t.Skip("native data absent")
	}
	k, err := aiknowledge.LoadDataDir(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := k.TaskOrder("axe-return")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"axe-request", "axe-delivery", "axe-letter-request", "axe-return"}
	if len(tasks) != 4 {
		t.Fatal(tasks)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "ai", "catalogs", "drafts", "axe-2.5.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document npcRegistryDocument
	if err := json.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	var specs []NPCSpec
	for _, entry := range document.NPCs {
		if entry.Verified {
			t.Fatal("draft promoted before live acceptance")
		}
		spec := entry.toNPCSpec()
		spec.Verified = true
		specs = append(specs, spec)
	}
	registry, err := NewNPCRegistry(specs)
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := gameFixture(t)
	backend.Knowledge = k
	skill := NewNPCSkill(backend, registry)
	for i, task := range tasks {
		if task.ID != want[i] || task.Status != aiknowledge.TaskUnverified || task.ExecutionVerified || task.PreparationReviewed {
			for _, issue := range k.Issues {
				if issue.Source != nil && issue.Source.Path == task.Source.Path {
					t.Log(issue.Code, issue.Message)
				}
			}
			t.Fatalf("invalid task state: %s %s", task.ID, task.Status)
		}
		for _, step := range task.Steps {
			if step.Action.Skill == "npc.dialogue" {
				if err := skill.ValidateSkill(context.Background(), automation.Action{Skill: step.Action.Skill, Arguments: step.Action.Arguments, MaximumCost: step.MaximumCost}); err != nil {
					t.Fatalf("%s/%s: %v", task.ID, step.ID, err)
				}
			}
		}
	}
	// The proposed approach cells must exist in the real collision maps.
	nav, err := ainavigation.LoadDataDir(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][3]int{{1400, 69, 88}, {1300, 18, 41}} {
		if !nav.Walkable(p[0], p[1], p[2]) {
			t.Fatalf("blocked NPC approach: %v", p)
		}
	}
}
