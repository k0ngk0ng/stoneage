package aiplanner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestBuildTaskCompilesVerifiedDefinition(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	plan, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{
		CharacterID: "acct:0", MaximumDeaths: 2, ReserveGold: 25,
		KnowledgeFingerprint: knowledge.Fingerprint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "quest" || plan.CharacterID != "acct:0" || plan.KnowledgeRevision != knowledge.Fingerprint() {
		t.Fatalf("unexpected plan identity: %#v", plan)
	}
	if len(plan.Steps) != 2 || plan.Steps[1].Action.Skill != "npc.window" || plan.Steps[1].MaximumCost != 5000 {
		t.Fatalf("steps were not copied: %#v", plan.Steps)
	}
	if plan.Steps[1].Action.ExpectedRevision != 0 || plan.Steps[1].Action.Arguments == nil {
		t.Fatalf("action was not compiled: %#v", plan.Steps[1].Action)
	}
	if plan.Budget.Minimum != 5000 || plan.Budget.ExpectedLow != 5000 || plan.Budget.ExpectedHigh != 5000 ||
		plan.Budget.MaximumSpend != 5000 || plan.Budget.Reserve != 25 || !plan.Budget.Known {
		t.Fatalf("budget was not compiled: %#v", plan.Budget)
	}
	if plan.MaximumSeconds != 50 || plan.MaximumDeaths != 2 {
		t.Fatalf("execution defaults/options were not applied: %#v", plan)
	}
}

func TestBuildTaskRejectsUnverifiedAndStaleDefinitions(t *testing.T) {
	tests := []struct {
		name string
		edit func(*aiknowledge.Knowledge, *aiknowledge.TaskDefinition)
		want error
	}{
		{name: "status", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) {
			task.Status = aiknowledge.TaskUnverified
		}, want: ErrTaskUnverified},
		{name: "execution assertion", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) { task.ExecutionVerified = false }, want: ErrExecutionUnverified},
		{name: "preparation review", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) { task.PreparationReviewed = false }, want: ErrPreparationUnreviewed},
		{name: "preparation evidence", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) { task.PreparationNotes = " " }, want: ErrPreparationUnreviewed},
		{name: "evidence assertion", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) { task.EvidenceVerified = false }, want: ErrFingerprintMismatch},
		{name: "cost", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) { task.Steps[1].CostKnown = false }, want: ErrInvalidCost},
		{name: "condition", edit: func(_ *aiknowledge.Knowledge, task *aiknowledge.TaskDefinition) {
			task.Steps[0].SuccessConditions[0].Kind = "model_guess"
		}, want: ErrInvalidCondition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			knowledge, task := verifiedFixture(t)
			tc.edit(knowledge, &task)
			knowledge.TaskDefinitions[0] = task
			_, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v does not contain %v", err, tc.want)
			}
		})
	}

	knowledge, task := verifiedFixture(t)
	if err := os.WriteFile(filepath.Join(knowledge.DataDir, "facts.conf"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	knowledge.TaskDefinitions[0] = task
	if _, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"}); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("stale source was accepted: %v", err)
	}
}

func TestBuildTaskRequiresSnapshotFingerprintAndIdentity(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	if _, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{}); !errors.Is(err, ErrInvalidTaskOptions) {
		t.Fatalf("missing identity was accepted: %v", err)
	}
	if _, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{
		CharacterID: "acct:0", KnowledgeFingerprint: strings.Repeat("b", 64),
	}); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("foreign snapshot was accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(knowledge).BuildTask(ctx, task.ID, TaskOptions{CharacterID: "acct:0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled compilation was accepted: %v", err)
	}
}

func TestBuildTaskRejectsCostOutsideBudget(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	task.Budget.GoldMax = 100
	task.Budget.GoldExpected = 100
	knowledge.TaskDefinitions[0] = task
	if _, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"}); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("budget overrun was accepted: %v", err)
	}
}

func TestAutomationPlanStillEnforcesAuthoritativeConditions(t *testing.T) {
	knowledge, task := verifiedFixture(t)
	plan, err := New(knowledge).BuildTask(context.Background(), task.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Completion[0].Match(automation.Observation{Connected: true, Ready: true, CharacterID: "acct:0", Floor: 1, X: 2, Y: 3}) {
		t.Fatal("compiled completion condition does not match authoritative observation")
	}
}

func verifiedFixture(t *testing.T) (*aiknowledge.Knowledge, aiknowledge.TaskDefinition) {
	t.Helper()
	dir := t.TempDir()
	raw := []byte("trainer=riderman\n")
	if err := os.WriteFile(filepath.Join(dir, "facts.conf"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ref := aiknowledge.SourceRef{Path: "facts.conf", Line: 1, SHA256: aiknowledge.SHA256Hex(raw)}
	condition := aiknowledge.MachineCondition{Kind: "position", Value: 1, X: 2, Y: 3}
	task := aiknowledge.TaskDefinition{
		ID: "verified-task", Name: "Verified task", Status: aiknowledge.TaskVerified,
		PreparationReviewed: true, PreparationNotes: "Synthetic unit fixture: no travel hazards or pet requirement.",
		Preconditions: []aiknowledge.Precondition{{MachineCondition: aiknowledge.MachineCondition{Kind: "alive"}, Evidence: []aiknowledge.SourceRef{ref}}},
		Steps: []aiknowledge.TaskStep{
			{ID: "move", Kind: "move", Description: "move", Action: aiknowledge.TaskAction{Skill: "move", Arguments: json.RawMessage(`{"floor":1,"x":2,"y":3}`)},
				SuccessConditions: []aiknowledge.MachineCondition{condition}, TimeoutSeconds: 20, CostKnown: true, MaximumCost: 0, Evidence: []aiknowledge.SourceRef{ref}},
			{ID: "confirm", Kind: "confirm", Description: "confirm", Action: aiknowledge.TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":110,"choice":"confirm"}`)},
				SuccessConditions: []aiknowledge.MachineCondition{{Kind: "character_skill_level", ID: "learn_ride", Value: 40}}, TimeoutSeconds: 30, CostKnown: true, MaximumCost: 5000, Evidence: []aiknowledge.SourceRef{ref}},
		},
		Success:  []aiknowledge.SuccessCondition{{MachineCondition: condition, Description: "arrived", Evidence: []aiknowledge.SourceRef{ref}}},
		Budget:   aiknowledge.Budget{GoldMin: 5000, GoldExpected: 5000, GoldMax: 5000, Evidence: []aiknowledge.SourceRef{ref}},
		Evidence: []aiknowledge.Evidence{{Source: ref, Claim: "fixture", Verified: true}}, EvidenceVerified: true, ExecutionVerified: true,
	}
	task.DataFingerprint = evidenceFingerprint(map[string][]byte{"facts.conf": raw})
	knowledge := &aiknowledge.Knowledge{DataDir: dir, Digest: strings.Repeat("a", 64), TaskDefinitions: []aiknowledge.TaskDefinition{task}}
	return knowledge, task
}
