package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestHumanQuestChainParameterIsExplicit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		raw := `{"mode":"quest","task_id":"target"}`
		if enabled {
			raw = `{"mode":"quest","task_id":"target","include_dependencies":true}`
		}
		input, err := decodeAutomationStart(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(raw)))
		if err != nil {
			t.Fatal(err)
		}
		request, err := (&webAutomationHandle{}).questRequest(AutomationConfig{TaskID: input.TaskID, IncludeDependencies: input.IncludeDependencies})
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		if value, exists := request.Parameters["include_dependencies"]; exists {
			if err := json.Unmarshal(value, &got); err != nil {
				t.Fatal(err)
			}
		}
		if got != enabled {
			t.Fatalf("chain flag=%v want=%v", got, enabled)
		}
	}
	for _, raw := range []string{`{"mode":"leveling","include_dependencies":true}`, `{"mode":"quest","include_dependencies":"true"}`} {
		if _, err := decodeAutomationStart(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(raw))); err == nil {
			t.Fatal("invalid chain request accepted")
		}
	}
}

func TestHumanQuestObservationKeepsServerCompletionEvidence(t *testing.T) {
	snapshot := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld}
	snapshot.Player.HasStatus = true
	snapshot.Player.HP = 100
	snapshot.AI.Received = true
	snapshot.AI.EndEvents[0] = 16
	snapshot.AI.ItemsKnown = true
	snapshot.AI.Items = []aigame.AIInventoryItem{{TemplateID: 2417}}
	snapshot.ActiveWindow = &aigame.WindowSnapshot{Open: true, Submitted: true, ObjectID: 42, Sequence: 1}
	observed := webAutomationObservation(snapshot, "account:0")
	for _, c := range []automation.Condition{{Kind: "flag_set", ID: "end:4"}, {Kind: "item_count", ID: "item:2417", Value: 1}, {Kind: "backpack_free_slots", Value: 14}} {
		if !c.Match(observed) {
			t.Fatalf("lost quest condition: %+v", c)
		}
	}
	if observed.UnlimitedFunds || len(observed.Windows) != 0 {
		t.Fatal("human funding or actionable window invented")
	}
	snapshot.AI.Received = false
	unknown := webAutomationObservation(snapshot, "account:0")
	if (automation.Condition{Kind: "flag_clear", ID: "end:4"}).Match(unknown) {
		t.Fatal("missing progress treated as false")
	}
}

func TestHumanChainPreviewAndStartCompilerAgreeWithoutAI(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 35, 10000)
	dir := t.TempDir()
	raw := []byte("synthetic quest contract")
	if err := os.WriteFile(filepath.Join(dir, "facts.conf"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ref := aiknowledge.SourceRef{Path: "facts.conf", Line: 1, SHA256: aiknowledge.SHA256Hex(raw)}
	hash := aiknowledge.SHA256Hex(append(append([]byte("facts.conf\x00"), raw...), 0))
	makeTask := func(id string) aiknowledge.TaskDefinition {
		condition := aiknowledge.MachineCondition{Kind: "flag_set", ID: id + "-done"}
		return aiknowledge.TaskDefinition{
			ID: id, Name: id, Status: aiknowledge.TaskVerified, PreparationReviewed: true, PreparationNotes: "Synthetic component fixture.",
			EvidenceVerified: true, ExecutionVerified: true, DataFingerprint: hash,
			Preconditions: []aiknowledge.Precondition{{MachineCondition: aiknowledge.MachineCondition{Kind: "character_level", Value: 30}}},
			Steps:         []aiknowledge.TaskStep{{ID: "step", Action: aiknowledge.TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"fixture-guide","window_sequence":20,"choice":1}`)}, SuccessConditions: []aiknowledge.MachineCondition{condition}, TimeoutSeconds: 30, CostKnown: true, MaximumCost: 10}},
			Success:       []aiknowledge.SuccessCondition{{MachineCondition: condition}},
			Budget:        aiknowledge.Budget{GoldMin: 10, GoldExpected: 10, GoldMax: 10, Evidence: []aiknowledge.SourceRef{ref}},
		}
	}
	dep, root := makeTask("prepare"), makeTask("target")
	root.Dependencies = []string{dep.ID}
	knowledge := &aiknowledge.Knowledge{DataDir: dir, Digest: strings.Repeat("a", 64), TaskDefinitions: []aiknowledge.TaskDefinition{root, dep}}
	fixture.executor.config.Knowledge = knowledge
	fixture.executor.config.NPCs = aiservice.MustNPCRegistry([]aiservice.NPCSpec{{
		Alias: "fixture-guide", Name: "Guide", Floor: 100, X: 31, Y: 30, TalkRange: 1,
		Verified: true, SourceFingerprint: knowledge.Fingerprint(),
		Windows: []aiservice.NPCWindowSpec{{Type: 0, Sequence: 20, WindowObjectFromActor: true,
			Choices: map[int]aiservice.NPCChoice{1: {Button: 1, MaximumCost: 10}}}},
	}})
	for _, chain := range []bool{false, true} {
		config := AutomationConfig{TaskID: root.ID, IncludeDependencies: chain, Budget: AutomationBudget{Reserve: 17}}
		preview, err := fixture.executor.Preview(context.Background(), fixture.session, AutomationStartRequest{Generation: fixture.session.generation, Mode: aicontrol.Quest, Config: config})
		if err != nil {
			t.Fatal(err)
		}
		binding, err := webAutomationBinding(fixture.tcp.authoritativeSnapshot(), fixture.session.generation)
		if err != nil {
			t.Fatal(err)
		}
		req, err := (&webAutomationHandle{}).questRequest(config)
		if err != nil {
			t.Fatal(err)
		}
		builder := aiservice.QuestPlans{Planner: aiplanner.New(knowledge), CharacterID: binding.CharacterID}
		plan, err := builder.Task(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if preview.Budget != webAutomationBudget(plan.Budget) || preview.Ready != chain || preview.AlreadyComplete {
			t.Fatalf("preview differs: %+v plan=%+v", preview, plan)
		}
		if chain && (len(preview.TaskOrder) != 2 || preview.TaskOrder[0] != "prepare" || plan.Budget.MaximumSpend != 20 || plan.Budget.Reserve != 17) {
			t.Fatalf("chain preview mismatch: %+v", preview)
		}
	}
}
