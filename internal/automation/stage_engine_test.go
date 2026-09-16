package automation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func stagedFixture(t *testing.T, maximumSpend int64) (*Engine, *fakeGame, Plan) {
	t.Helper()
	e, game, _ := fixture(t)
	plan := Plan{
		ID:                "quest-chain",
		CharacterID:       "account:0",
		Mode:              "quest",
		KnowledgeRevision: "test-v1",
		Title:             "chain",
		MaximumSeconds:    3600,
		MaximumDeaths:     2,
		Budget:            Budget{Known: true, MaximumSpend: maximumSpend},
		Completion:        []Condition{{Kind: "flag_set", ID: "c-done"}},
		Steps: []Step{
			{ID: "a", Action: Action{Skill: "test.a"}, Success: []Condition{{Kind: "flag_set", ID: "a-step"}}, TimeoutSeconds: 10, CostKnown: true, MaximumCost: 10},
			{ID: "b", Action: Action{Skill: "test.b"}, Success: []Condition{{Kind: "flag_set", ID: "b-step"}}, TimeoutSeconds: 10, CostKnown: true, MaximumCost: 10},
			{ID: "c", Action: Action{Skill: "test.c"}, Success: []Condition{{Kind: "flag_set", ID: "c-step"}}, TimeoutSeconds: 10, CostKnown: true, MaximumCost: 10},
		},
		Stages: []QuestStage{
			{ID: "a", Title: "A", StartStep: 0, EndStep: 1, Completion: []Condition{{Kind: "flag_set", ID: "a-done"}}},
			{ID: "b", Title: "B", Dependencies: []string{"a"}, StartStep: 1, EndStep: 2, Preconditions: []Condition{{Kind: "flag_set", ID: "a-done"}}, Completion: []Condition{{Kind: "flag_set", ID: "b-done"}}},
			{ID: "c", Title: "C", Dependencies: []string{"b"}, StartStep: 2, EndStep: 3, Preconditions: []Condition{{Kind: "flag_set", ID: "b-done"}}, Completion: []Condition{{Kind: "flag_set", ID: "c-done"}}},
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	game.observation.Flags = map[string]bool{}
	return e, game, plan
}

func TestStagedBoundaryWaitsForAuthoritativeCompletion(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 30)
	if _, err := e.Start(ctx, plan); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 0 || !checkpoint.StageEntered || checkpoint.Phase != "submitted" {
		t.Fatalf("first stage did not submit: %+v %v", checkpoint, err)
	}
	game.observation.Flags["a-step"] = true
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 0 || !checkpoint.StageEntered || checkpoint.Step != 1 || checkpoint.Status != Running {
		t.Fatalf("last step advanced the stage without completion: %+v %v", checkpoint, err)
	}
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 0 || checkpoint.Step != 1 || checkpoint.Status != Running || len(game.actions) != 1 {
		t.Fatalf("stage boundary did not wait: %+v %v actions=%d", checkpoint, err, len(game.actions))
	}
	game.observation.Flags["a-done"] = true
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 1 || checkpoint.StageEntered || checkpoint.Step != 1 || checkpoint.Status != Running {
		t.Fatalf("stage completion did not advance durable boundary: %+v %v", checkpoint, err)
	}
}

func TestStagedEntryGuardsAreCheckedBeforeFirstSubmission(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 30)
	plan.Stages[0].Preconditions = []Condition{{Kind: "flag_set", ID: "entry-ready"}}
	if _, err := e.Start(ctx, plan); err == nil {
		t.Fatal("missing stage entry guard was accepted")
	}
	if _, err := e.Store.Load(ctx, plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed stage entry created a checkpoint: %v", err)
	}
	game.observation.Flags["entry-ready"] = true
	checkpoint, err := e.Start(ctx, plan)
	if err != nil || checkpoint.Stage != 0 || checkpoint.StageEntered {
		t.Fatalf("valid stage entry did not start unentered: %+v %v", checkpoint, err)
	}
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 0 || !checkpoint.StageEntered || len(game.actions) != 1 {
		t.Fatalf("stage entry did not precede first action: %+v %v actions=%d", checkpoint, err, len(game.actions))
	}
}

func TestStagedBudgetIsCumulativeAcrossStages(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 20)
	if _, err := e.Start(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	game.observation.Flags["a-step"] = true
	game.observation.Flags["a-done"] = true
	if _, err := e.Tick(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if len(game.actions) != 2 {
		t.Fatalf("second stage was not submitted: %d", len(game.actions))
	}
	game.observation.Flags["b-step"] = true
	game.observation.Flags["b-done"] = true
	checkpoint, err := e.Tick(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 2 || checkpoint.ReservedSpend != 20 {
		t.Fatalf("second stage completion lost cumulative reservation: %+v %v", checkpoint, err)
	}
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Status != Paused || checkpoint.ReservedSpend != 20 || len(game.actions) != 2 {
		t.Fatalf("third stage exceeded whole-chain budget: %+v %v actions=%d", checkpoint, err, len(game.actions))
	}
}

func TestStagedCompletedStagesAndConsumedAncestorAreSkippedByDAGClosure(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 40)
	// In a linear chain, B's observed completion proves A even when A's
	// completion flag was consumed. C is still the next stage to execute.
	game.observation.Flags["b-done"] = true
	checkpoint, err := e.Start(ctx, plan)
	if err != nil || checkpoint.Stage != 2 || checkpoint.Step != 2 || checkpoint.StageEntered {
		t.Fatalf("completed descendant did not skip consumed ancestor: %+v %v", checkpoint, err)
	}
	checkpoint, err = e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 2 || len(game.actions) != 1 {
		t.Fatalf("next stage was not executed after closure skip: %+v %v actions=%d", checkpoint, err, len(game.actions))
	}

	// In a diamond, completion of one sibling branch proves only its own
	// ancestors. The other sibling must retain its direct prerequisite guard.
	e, game, plan = stagedFixture(t, 40)
	plan.Steps = append(plan.Steps,
		Step{ID: "d", Action: Action{Skill: "test.d"}, Success: []Condition{{Kind: "flag_set", ID: "d-step"}}, TimeoutSeconds: 10, CostKnown: true, MaximumCost: 10})
	plan.Completion = []Condition{{Kind: "flag_set", ID: "d-done"}}
	plan.Stages = []QuestStage{
		{ID: "a", StartStep: 0, EndStep: 1, Completion: []Condition{{Kind: "flag_set", ID: "a-done"}}},
		{ID: "b", Dependencies: []string{"a"}, StartStep: 1, EndStep: 2, Preconditions: []Condition{{Kind: "flag_set", ID: "a-done"}}, Completion: []Condition{{Kind: "flag_set", ID: "b-done"}}},
		{ID: "c", Dependencies: []string{"a"}, StartStep: 2, EndStep: 3, Preconditions: []Condition{{Kind: "flag_set", ID: "a-done"}}, Completion: []Condition{{Kind: "flag_set", ID: "c-done"}}},
		{ID: "d", Dependencies: []string{"b", "c"}, StartStep: 3, EndStep: 4, Preconditions: []Condition{{Kind: "flag_set", ID: "b-done"}, {Kind: "flag_set", ID: "c-done"}}, Completion: []Condition{{Kind: "flag_set", ID: "d-done"}}},
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	game.observation.Flags["b-done"] = true
	if _, err := e.Start(ctx, plan); err == nil {
		t.Fatal("sibling branch waived its direct consumed prerequisite")
	}
	if _, err := e.Store.Load(ctx, plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed sibling precondition created a checkpoint: %v", err)
	}
}

func TestStagedUnknownDeliveryCannotBeSkippedByDescendantOrReplayed(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 30)
	game.execute = func(Action) error { return errors.New("response lost") }
	if _, err := e.Start(ctx, plan); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := e.Tick(ctx, plan.ID)
	if err == nil || checkpoint.Status != Paused || checkpoint.Stage != 0 || !checkpoint.StageEntered || checkpoint.Phase != "prepared" {
		t.Fatalf("unknown delivery was not fenced: %+v %v", checkpoint, err)
	}
	game.observation.Flags["c-done"] = true
	if _, err = e.Resume(ctx, plan.ID); err == nil {
		t.Fatal("descendant completion incorrectly skipped entered unknown stage")
	}
	if len(game.actions) != 1 {
		t.Fatalf("unknown delivery was replayed: %d", len(game.actions))
	}
}

func TestStagedDynamicLevelAndPetReplacementFenceTickAndResume(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 30)
	plan.Stages[0].Preconditions = []Condition{{Kind: "character_level", Value: 10}, {Kind: "pet_level", ID: "pet-a", Value: 5}}
	game.observation.Pets = []Entity{{ID: "pet-a", Level: 5}}
	if _, err := e.Start(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	game.observation.Character.Level = 9
	checkpoint, err := e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Status != Paused || len(game.actions) != 1 {
		t.Fatalf("level drop did not fence active stage: %+v %v", checkpoint, err)
	}
	if _, err = e.Resume(ctx, plan.ID); err == nil {
		t.Fatal("resume ignored level fence")
	}
	game.observation.Character.Level = 10
	game.observation.Pets = []Entity{{ID: "pet-b", Level: 5}}
	if _, err = e.Resume(ctx, plan.ID); err == nil {
		t.Fatal("resume ignored stable pet replacement")
	}
	if len(game.actions) != 1 {
		t.Fatalf("dynamic guard attempted another action: %d", len(game.actions))
	}
}

func TestStagedCheckpointRejectsInFlightUnenteredState(t *testing.T) {
	ctx := context.Background()
	e, _, plan := stagedFixture(t, 30)
	checkpoint, err := e.Start(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Phase = "prepared"
	checkpoint.Revision++
	if err := e.Store.Save(ctx, checkpoint, checkpoint.Revision-1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(ctx, plan.ID); err == nil {
		t.Fatal("in-flight unentered checkpoint was accepted")
	}
}

func TestStagedPlanValidation(t *testing.T) {
	_, _, plan := stagedFixture(t, 30)
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{name: "gap", mutate: func(p *Plan) { p.Stages[1].StartStep++ }},
		{name: "future dependency", mutate: func(p *Plan) { p.Stages[0].Dependencies = []string{"b"} }},
		{name: "duplicate dependency", mutate: func(p *Plan) { p.Stages[1].Dependencies = []string{"a", "a"} }},
		{name: "window submission completion", mutate: func(p *Plan) {
			p.Stages[2].Completion = []Condition{{Kind: "window_submitted", ID: "window", Value: 1}}
			p.Completion = append([]Condition(nil), p.Stages[2].Completion...)
		}},
		{name: "final mismatch", mutate: func(p *Plan) { p.Completion = []Condition{{Kind: "flag_set", ID: "other"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := plan
			candidate.Stages = append([]QuestStage(nil), plan.Stages...)
			tc.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid staged plan accepted")
			}
		})
	}
}

func TestStagedStoreSurvivesCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	e, game, plan := stagedFixture(t, 30)
	oldStore, ok := e.Store.(*SQLiteStore)
	if !ok {
		t.Fatal("fixture store type changed")
	}
	path := filepath.Join(t.TempDir(), "chain.db")
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	if _, err := e.Start(ctx, plan); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := e.Tick(ctx, plan.ID)
	if err != nil || checkpoint.Stage != 0 || checkpoint.Phase != "submitted" {
		t.Fatalf("reopened store did not run chain: %+v %v", checkpoint, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	loaded, err := reopened.Load(ctx, plan.ID)
	if err != nil || loaded.Stage != 0 || !loaded.StageEntered || len(game.actions) != 1 {
		t.Fatalf("stage checkpoint did not survive store: %+v %v actions=%d", loaded, err, len(game.actions))
	}
}
