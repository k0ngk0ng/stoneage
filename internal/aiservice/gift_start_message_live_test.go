package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Executes the embedded informational step only. The whole delivery remains
// unverified; this does not bypass the production planner's verification gate.
func confirmGiftStartMessage(t *testing.T, ctx context.Context, root string, backend *GameBackend, registry NPCRegistry, skill *NPCSkill) json.RawMessage {
	t.Helper()
	task, ok := backend.Knowledge.FindTask("hometown-0-gift-exchange")
	if !ok || task.ExecutionVerified {
		t.Fatal("expected unverified gift task")
	}
	var step automation.Step
	for _, source := range task.Steps {
		if source.ID != "confirm-start-message" {
			continue
		}
		convert := func(input []aiknowledge.MachineCondition) []automation.Condition {
			var out []automation.Condition
			for _, c := range input {
				out = append(out, automation.Condition{Kind: c.Kind, ID: c.ID, Value: c.Value, X: c.X, Y: c.Y})
			}
			return out
		}
		step = automation.Step{ID: source.ID, Description: source.Description, Action: automation.Action{Skill: source.Action.Skill, Arguments: source.Action.Arguments}, Preconditions: convert(source.Preconditions), Success: convert(source.SuccessConditions), TimeoutSeconds: source.TimeoutSeconds, CostKnown: source.CostKnown, MaximumCost: source.MaximumCost}
	}
	if step.ID == "" || len(step.Success) != 1 || step.Success[0].Kind != "window_submitted" {
		t.Fatal("missing explicit STARTMSG submission step")
	}
	store, err := automation.OpenStore(filepath.Join(root, "start-message.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	game := &AutomationGame{Backend: backend, NPCs: registry, Skills: skill}
	engine := &automation.Engine{Game: game, Store: store}
	plan := automation.Plan{ID: "qa-start-message", CharacterID: backend.Binding.CharacterID, Mode: "quest", Title: "STARTMSG submission", KnowledgeRevision: backend.Knowledge.Fingerprint(), MaximumSeconds: 60, Budget: automation.Budget{Known: true}, Preconditions: step.Preconditions, Completion: step.Success, Steps: []automation.Step{step}}
	started, err := engine.Start(ctx, plan)
	if err != nil || started.Status != automation.Running {
		t.Fatalf("STARTMSG was skipped or unavailable: %s %v", started.Status, err)
	}
	submitted, err := engine.Tick(ctx, plan.ID)
	if err != nil || submitted.Phase != "submitted" {
		t.Fatalf("STARTMSG not submitted: %s %v", submitted.Phase, err)
	}
	done, err := engine.Tick(ctx, plan.ID)
	if err != nil || done.Status != automation.Completed || done.Confirmation == nil || done.Confirmation.SubmittedWindows["sainasu-himiko"] != 231 {
		t.Fatalf("STARTMSG lacks local submission checkpoint: %s %v", done.Status, err)
	}
	observed, err := game.Observe(ctx)
	if err != nil || observed.Windows["sainasu-himiko"] != 0 {
		t.Fatal("submitted STARTMSG became actionable")
	}
	t.Log("embedded STARTMSG step executed and checkpointed local submission, without assuming server acknowledgement")
	return step.Action.Arguments
}
