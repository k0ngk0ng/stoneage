package aiservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestTaskKnowledgeDoesNotAdvertiseUnreviewedPreparationAsExecutable(t *testing.T) {
	b, _ := gameFixture(t)
	task := aiknowledge.TaskDefinition{ID: "fixture", Status: aiknowledge.TaskVerified, EvidenceVerified: true, ExecutionVerified: true}
	b.Knowledge = &aiknowledge.Knowledge{TaskDefinitions: []aiknowledge.TaskDefinition{task}}
	check := func(want bool) {
		t.Helper()
		r, err := b.QueryKnowledge(context.Background(), b.Binding, aimcp.KnowledgeQuery{Kind: "task", ID: task.ID})
		if err != nil || len(r.Entries) != 1 || r.Entries[0].Verified != want {
			t.Fatalf("knowledge: %+v %v", r, err)
		}
	}
	check(false)
	b.Knowledge.TaskDefinitions[0].PreparationReviewed = true
	check(false)
	b.Knowledge.TaskDefinitions[0].PreparationNotes = "Synthetic source-mapped fixture"
	check(true)
	b.Knowledge.TaskDefinitions[0].Status = aiknowledge.TaskUnverified
	check(false)
}

func TestTaskKnowledgeIncludesDependenciesAndRejectsUnverifiedAncestors(t *testing.T) {
	b, _ := gameFixture(t)
	root := aiknowledge.TaskDefinition{ID: "root", Dependencies: []string{"prerequisite"}, Status: aiknowledge.TaskVerified, EvidenceVerified: true, ExecutionVerified: true, PreparationReviewed: true, PreparationNotes: "Reviewed fixture"}
	dep := root
	dep.ID, dep.Dependencies, dep.ExecutionVerified = "prerequisite", nil, false
	b.Knowledge = &aiknowledge.Knowledge{TaskDefinitions: []aiknowledge.TaskDefinition{root, dep}}
	check := func(want bool) {
		t.Helper()
		r, err := b.QueryKnowledge(context.Background(), b.Binding, aimcp.KnowledgeQuery{Kind: "task", ID: "root"})
		if err != nil || len(r.Entries) != 1 || r.Entries[0].Verified != want {
			t.Fatalf("knowledge=%+v %v", r, err)
		}
		var decoded aiknowledge.TaskDefinition
		if err := json.Unmarshal(r.Entries[0].Data, &decoded); err != nil || len(decoded.Dependencies) != 1 || decoded.Dependencies[0] != "prerequisite" {
			t.Fatalf("dependency not discoverable: %+v %v", decoded, err)
		}
	}
	check(false)
	b.Knowledge.TaskDefinitions[1].ExecutionVerified = true
	check(true)
	b.Knowledge.TaskDefinitions[1].Dependencies = []string{"root"}
	check(false)
}
