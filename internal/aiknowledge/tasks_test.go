package aiknowledge

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTasksRejectsProseOnlySuccess(t *testing.T) {
	dataDir, taskDir := taskFixtureDirs(t)
	task := validTask()
	task.Steps[0].SuccessConditions = nil
	task.Steps[0].Success = []string{"the move worked"}
	writeTaskJSON(t, taskDir, "prose.json", task)

	tasks, _, _, issues, err := loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnsupported {
		t.Fatalf("expected one unsupported task, got %#v", tasks)
	}
	if !hasIssue(issues, "task_schema") {
		t.Fatalf("expected task_schema issue, got %#v", issues)
	}
}

func TestLoadTasksRejectsIncompleteWindowArguments(t *testing.T) {
	dataDir, taskDir := taskFixtureDirs(t)
	task := validTask()
	task.Steps[0] = TaskStep{
		ID: "window", Kind: "select", Description: "select",
		Action:            TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":"100"}`)},
		SuccessConditions: []MachineCondition{{Kind: "window_sequence", ID: "trainer", Value: 110}},
		TimeoutSeconds:    30, CostKnown: true, MaximumCost: 0,
		Evidence: []SourceRef{{Path: "facts.conf", Line: 1}},
	}
	writeTaskJSON(t, taskDir, "bad-window.json", task)
	tasks, _, _, issues, err := loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnsupported || !hasIssue(issues, "task_schema") {
		t.Fatalf("incomplete window action was accepted: tasks=%#v issues=%#v", tasks, issues)
	}
}

func TestLoadTasksRejectsUnsupportedWindowArguments(t *testing.T) {
	dataDir, taskDir := taskFixtureDirs(t)
	task := validTask()
	task.Steps[0] = TaskStep{
		ID: "window", Kind: "confirm", Description: "confirm",
		Action:            TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":110,"choice":"confirm","gold_cost":5000}`)},
		SuccessConditions: []MachineCondition{{Kind: "flag_set", ID: "done"}},
		TimeoutSeconds:    30, CostKnown: true, MaximumCost: 5000,
		Evidence: []SourceRef{{Path: "facts.conf", Line: 1}},
	}
	writeTaskJSON(t, taskDir, "unsupported-window.json", task)
	tasks, _, _, issues, err := loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnsupported || !hasIssue(issues, "task_schema") {
		t.Fatalf("unsupported window argument was accepted: tasks=%#v issues=%#v", tasks, issues)
	}
}

func TestLoadTasksCarriesExecutableActionAndConditions(t *testing.T) {
	dataDir, taskDir := taskFixtureDirs(t)
	task := validTask()
	task.Steps = []TaskStep{
		{
			ID: "talk", Kind: "talk", Description: "talk", NPC: "trainer",
			Action:            TaskAction{Skill: "npc.talk", Arguments: json.RawMessage(`{"npc":"trainer","floor":1,"x":2,"y":3}`)},
			SuccessConditions: []MachineCondition{{Kind: "window_sequence", ID: "trainer", Value: 100}},
			TimeoutSeconds:    30, CostKnown: true, MaximumCost: 0,
			Evidence: []SourceRef{{Path: "facts.conf", Line: 1}},
		},
		{
			ID: "choose", Kind: "select", Description: "choose", NPC: "trainer",
			Action:            TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":1}`)},
			SuccessConditions: []MachineCondition{{Kind: "window_sequence", ID: "trainer", Value: 110}},
			TimeoutSeconds:    30, CostKnown: true, MaximumCost: 0,
			Evidence: []SourceRef{{Path: "facts.conf", Line: 1}},
		},
	}
	task.Evidence = []Evidence{{Source: SourceRef{Path: "facts.conf", Line: 1}, Claim: "fixture", Verified: false}}
	writeTaskJSON(t, taskDir, "executable.json", task)

	tasks, files, rawTasks, issues, err := loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("valid task produced issues: %#v", issues)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnverified {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
	if tasks[0].Steps[1].SuccessConditions[0].Value != 110 {
		t.Fatalf("window sequence was not retained: %#v", tasks[0].Steps[1].SuccessConditions)
	}
	if string(tasks[0].Steps[1].Action.Arguments) == "" || len(files) != 1 || len(rawTasks) != 1 {
		t.Fatalf("task bytes/digest missing: files=%#v bytes=%#v", files, rawTasks)
	}
}

func TestVerifiedTaskRequiresEvidenceFingerprintAndExecutionAssertion(t *testing.T) {
	dataDir, taskDir := taskFixtureDirs(t)
	raw := []byte("authoritative source\n")
	if err := os.WriteFile(filepath.Join(dataDir, "verified.conf"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{Path: "verified.conf", Line: 1, SHA256: SHA256Hex(raw)}
	task := validTask()
	task.Status = TaskVerified
	task.ExecutionVerified = true
	task.PreparationReviewed = true
	task.PreparationNotes = "Synthetic fixture: no route hazard or pet requirement."
	task.Evidence = []Evidence{{Source: ref, Claim: "verified fixture", Verified: true}}
	task.Steps[0].Evidence = []SourceRef{ref}
	task.Success[0].Evidence = []SourceRef{ref}
	task.Budget.Evidence = []SourceRef{ref}
	task.DataFingerprint = evidenceFingerprint(map[string][]byte{"verified.conf": raw})
	writeTaskJSON(t, taskDir, "verified.json", task)

	tasks, _, _, issues, err := loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 || len(tasks) != 1 {
		t.Fatalf("verified fixture produced load issues: tasks=%#v issues=%#v", tasks, issues)
	}
	if tasks[0].Status != TaskVerified || !tasks[0].EvidenceVerified || !tasks[0].ExecutionVerified {
		t.Fatalf("verification flags were not preserved: %#v", tasks[0])
	}
	for _, missing := range []string{"review", "notes"} {
		unreviewed := task
		if missing == "review" {
			unreviewed.PreparationReviewed = false
		} else {
			unreviewed.PreparationNotes = " "
		}
		writeTaskJSON(t, taskDir, "verified.json", unreviewed)
		loaded, _, _, problems, loadErr := loadTasks(taskDir, dataDir, nil, testNPC(), false)
		if loadErr != nil || len(loaded) != 1 || loaded[0].Status != TaskUnverified || !hasIssue(problems, "task_not_verified") {
			t.Fatalf("missing preparation %s remained executable: %+v %v %v", missing, loaded, problems, loadErr)
		}
		if _, _, _, _, strictErr := loadTasks(taskDir, dataDir, nil, testNPC(), true); strictErr == nil {
			t.Fatalf("strict loading accepted missing preparation %s", missing)
		}
	}
	writeTaskJSON(t, taskDir, "verified.json", task)

	if err := os.WriteFile(filepath.Join(dataDir, "verified.conf"), []byte("changed source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, _, _, issues, err = loadTasks(taskDir, dataDir, nil, testNPC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != TaskUnsupported || !hasIssue(issues, "task_evidence") {
		t.Fatalf("stale evidence was accepted: tasks=%#v issues=%#v", tasks, issues)
	}
}

func TestEmbeddedRidingTaskHasExecutableShape(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "npc", "family"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The embedded definition is checked structurally here. Its static source
	// paths are validated by the actual-data integration test when available.
	task, err := decodeTask(mustEmbeddedTask(t), "tasks/riding-basic.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTaskShape(task, NPCKnowledge{Templates: []NPCTemplate{{Name: "riderman"}}}); err != nil {
		t.Fatal(err)
	}
	if len(task.Steps) != 5 {
		t.Fatalf("riding task must contain move, talk, and three window transitions: %#v", task.Steps)
	}
	if task.Steps[0].Action.Skill != "move" || len(task.Steps[0].SuccessConditions) != 1 {
		t.Fatalf("riding task is not executable: %#v", task.Steps[0])
	}
	if task.Steps[0].Coordinates == nil || task.Steps[0].Coordinates.X != 60 || task.Steps[0].Coordinates.Y != 45 {
		t.Fatalf("riding task does not use the trainer interaction tile: %#v", task.Steps[0].Coordinates)
	}
	var moveArgs struct {
		Floor int `json:"floor"`
		X     int `json:"x"`
		Y     int `json:"y"`
	}
	if err := json.Unmarshal(task.Steps[0].Action.Arguments, &moveArgs); err != nil {
		t.Fatal(err)
	}
	if moveArgs.Floor != 1040 || moveArgs.X != 60 || moveArgs.Y != 45 {
		t.Fatalf("move action does not use the trainer interaction tile: %#v", moveArgs)
	}
	var talkArgs struct {
		Floor int `json:"floor"`
		X     int `json:"x"`
		Y     int `json:"y"`
	}
	if err := json.Unmarshal(task.Steps[1].Action.Arguments, &talkArgs); err != nil {
		t.Fatal(err)
	}
	if talkArgs.Floor != 1040 || talkArgs.X != 61 || talkArgs.Y != 45 {
		t.Fatalf("talk action does not identify the trainer's entity tile: %#v", talkArgs)
	}
	type windowAction struct {
		WindowSequence int             `json:"window_sequence"`
		Choice         json.RawMessage `json:"choice"`
	}
	wantSequences := []int{101, 200, 210}
	wantChoices := []string{`4`, `1`, `"confirm"`}
	for index, wantSequence := range wantSequences {
		var action windowAction
		if err := json.Unmarshal(task.Steps[index+2].Action.Arguments, &action); err != nil {
			t.Fatal(err)
		}
		if action.WindowSequence != wantSequence || string(action.Choice) != wantChoices[index] {
			t.Fatalf("riding window %d has sequence/choice %d/%s, want %d/%s", index+1, action.WindowSequence, action.Choice, wantSequence, wantChoices[index])
		}
		if len(task.Steps[index+2].SuccessConditions) != 1 {
			t.Fatalf("riding window %d has unexpected success conditions: %#v", index+1, task.Steps[index+2].SuccessConditions)
		}
	}
	if task.Steps[2].SuccessConditions[0].Value != 200 || task.Steps[3].SuccessConditions[0].Value != 210 {
		t.Fatalf("riding window transitions are incomplete: %#v", task.Steps[2:4])
	}
}

func TestActualDataLoadKeepsRidingTaskUnverified(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data", "enemybase.txt")); err != nil {
		t.Skip("2.5 source data is not present")
	}
	k, err := Load(context.Background(), Options{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := k.FindTask("riding-basic")
	if !ok {
		t.Fatal("embedded riding task was not loaded")
	}
	if task.Status != TaskUnverified || task.EvidenceVerified {
		t.Fatalf("riding task should require runtime verification: %#v", task)
	}
	if len(k.EnemyBases) < 900 || len(k.Encounters) < 600 || len(k.NPC.Files) < 1000 {
		t.Fatalf("actual 2.5 snapshot was unexpectedly incomplete: bases=%d encounters=%d npc=%d", len(k.EnemyBases), len(k.Encounters), len(k.NPC.Files))
	}
}

func TestGiftTaskIncludesBoundedSuppliesAndRemainsUnverified(t *testing.T) {
	raw, err := embeddedTasks.ReadFile("tasks/hometown-0-gift-exchange.json")
	if err != nil {
		t.Fatal(err)
	}
	task, err := decodeTask(raw, "tasks/hometown-0-gift-exchange.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTaskShape(task, NPCKnowledge{}); err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskUnverified || task.ExecutionVerified {
		t.Fatal("supplies certified the quest")
	}
	step := task.Steps[0]
	if step.Kind != "stock" || step.Action.Skill != "item.stock" || step.MaximumCost != 60 || task.Budget.GoldMax != 60 || step.TimeoutSeconds != 600 {
		t.Fatal("supplies omitted from task cost/time")
	}
	var args struct {
		Item    string `json:"item"`
		Target  int    `json:"target_count"`
		Reserve int    `json:"reserve_slots"`
	}
	if json.Unmarshal(step.Action.Arguments, &args) != nil || args.Item != "small-meat" || args.Target != 5 || args.Reserve != 2 {
		t.Fatal("unexpected preparation policy")
	}
	capacity := false
	for _, c := range step.SuccessConditions {
		if c.Kind == "backpack_free_slots" && c.Value == 2 {
			capacity = true
		}
	}
	if !capacity {
		t.Fatal("stocked but full backpack could skip preparation")
	}
}

func taskFixtureDirs(t *testing.T) (string, string) {
	t.Helper()
	dataDir := t.TempDir()
	taskDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "facts.conf"), []byte("fact=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir, taskDir
}

func validTask() TaskDefinition {
	ref := SourceRef{Path: "facts.conf", Line: 1}
	return TaskDefinition{
		ID: "fixture-task", Name: "Fixture task", Status: TaskUnverified,
		Steps: []TaskStep{{
			ID: "move", Kind: "move", Description: "move",
			Action:            TaskAction{Skill: "move", Arguments: json.RawMessage(`{"floor":1,"x":2,"y":3}`)},
			SuccessConditions: []MachineCondition{{Kind: "position", Value: 1, X: 2, Y: 3}},
			TimeoutSeconds:    60, CostKnown: true, MaximumCost: 0, Evidence: []SourceRef{ref},
		}},
		Success:  []SuccessCondition{{MachineCondition: MachineCondition{Kind: "position", Value: 1, X: 2, Y: 3}, Description: "arrived", Evidence: []SourceRef{ref}}},
		Budget:   Budget{GoldMin: 0, GoldExpected: 0, GoldMax: 0, Evidence: []SourceRef{ref}},
		Evidence: []Evidence{{Source: ref, Claim: "fixture source", Verified: false}},
	}
}

func testNPC() NPCKnowledge {
	return NPCKnowledge{Templates: []NPCTemplate{{Name: "trainer"}}}
}

func writeTaskJSON(t *testing.T, taskDir, name string, task TaskDefinition) {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustEmbeddedTask(t *testing.T) []byte {
	t.Helper()
	raw, err := fs.ReadFile(embeddedTasks, "tasks/riding-basic.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestGiftTaskConfirmsStartMessageBeforeTravel(t *testing.T) {
	raw, err := embeddedTasks.ReadFile("tasks/hometown-0-gift-exchange.json")
	if err != nil {
		t.Fatal(err)
	}
	task, err := decodeTask(raw, "tasks/hometown-0-gift-exchange.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range task.Steps {
		if step.ID != "request-flower" {
			continue
		}
		if i+2 >= len(task.Steps) || task.Steps[i+1].ID != "confirm-start-message" || task.Steps[i+2].ID != "go-to-yayoi" {
			t.Fatal("gift pickup must submit STARTMSG before travel")
		}
		confirm := task.Steps[i+1]
		var args struct {
			NPC      string `json:"npc"`
			Sequence int    `json:"window_sequence"`
			Choice   int    `json:"choice"`
		}
		if json.Unmarshal(confirm.Action.Arguments, &args) != nil || confirm.Action.Skill != "npc.window" || args.NPC != "sainasu-himiko" || args.Sequence != 231 || args.Choice != 1 {
			t.Fatal("STARTMSG does not use the reviewed OK contract")
		}
		if len(confirm.SuccessConditions) != 1 || confirm.SuccessConditions[0].Kind != "window_submitted" || confirm.SuccessConditions[0].ID != args.NPC || confirm.SuccessConditions[0].Value != 231 {
			t.Fatal("existing flower must not satisfy the STARTMSG step")
		}
		if task.ExecutionVerified {
			t.Fatal("pickup does not certify full delivery")
		}
		return
	}
	t.Fatal("gift pickup step missing")
}
