package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type progressGame struct {
	*fakeGame
	run    func(context.Context, Action, StepExecution) error
	resume func(context.Context, Action, StepProgress) error
}

func TestOutputReferencesRejectUnknownFutureAndWrongSkill(t *testing.T) {
	for _, mode := range []string{"valid", "unknown", "future", "wrong-skill", "self-precondition", "stage-entry"} {
		t.Run(mode, func(t *testing.T) {
			p := Plan{Completion: []Condition{{Kind: "step_confirmed", ID: "deliver"}}, Steps: []Step{
				{ID: "collect", Action: Action{Skill: "pet.collect", Arguments: json.RawMessage(`{}`)}, Success: []Condition{{Kind: "pet_collection", ID: "collect", Value: 1}}},
				{ID: "deliver", Action: Action{Skill: "pet.deliver", Arguments: json.RawMessage(`{"pet_collection":"collect"}`)}},
			}}
			switch mode {
			case "unknown":
				p.Steps[1].Action.Arguments = json.RawMessage(`{"pet_collection":"other"}`)
			case "future":
				p.Steps[0].Action.Arguments = json.RawMessage(`{"pet_collection":"collect"}`)
			case "wrong-skill":
				p.Steps[0].Action.Skill = "npc.talk"
			case "self-precondition":
				p.Steps[0].Preconditions = p.Steps[0].Success
			case "stage-entry":
				p.Stages = []QuestStage{{StartStep: 0, EndStep: 2, Preconditions: p.Steps[0].Success}}
			}
			if err := validateOutputReferences(p); (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
}

func (g *progressGame) ExecuteStep(ctx context.Context, a Action, s StepExecution) error {
	return g.run(ctx, a, s)
}
func (g *progressGame) CanResumeStep(ctx context.Context, a Action, p StepProgress) error {
	if g.resume == nil {
		return errors.New("uncertain operation")
	}
	return g.resume(ctx, a, p)
}

func TestStepOutputsPersistAndBindOnlyConfirmedOwnedPets(t *testing.T) {
	e, base, p := fixture(t)
	p.Completion = []Condition{{Kind: "step_confirmed", ID: "deliver"}}
	p.Steps = []Step{
		{ID: "collect", Action: Action{Skill: "pet.collect", Arguments: json.RawMessage(`{}`)}, Success: []Condition{{Kind: "pet_collection", ID: "collect", Value: 1}}, CostKnown: true, TimeoutSeconds: 20},
		{ID: "deliver", Action: Action{Skill: "pet.deliver", Arguments: json.RawMessage(`{}`)}, Success: p.Completion, CostKnown: true, TimeoutSeconds: 20},
	}
	base.observation.Pets = []Entity{{ID: "original"}}
	base.observation.ConfirmedSteps = map[string]bool{"deliver": true}
	base.observation.StepPets = map[string][]string{"collect": {"original"}}
	g := &progressGame{fakeGame: base}
	e.Game = g
	g.run = func(ctx context.Context, a Action, s StepExecution) error {
		g.actions = append(g.actions, a)
		if s.StepID == "collect" {
			if len(s.Results) != 0 {
				t.Fatal("future output exposed")
			}
			if err := s.Save(ctx, StepProgress{State: json.RawMessage(`{"protected":["original"]}`)}); err != nil {
				return err
			}
			stored, err := e.Store.Load(ctx, p.ID)
			if err != nil || len(stored.StepProgress["collect"].State) == 0 {
				t.Fatal("baseline not durable", err)
			}
			g.observation.Pets = append(g.observation.Pets, Entity{ID: "captured"})
			return s.Save(ctx, StepProgress{State: json.RawMessage(`{"phase":"idle"}`), PetIDs: []string{"captured"}, Confirmed: true})
		}
		if got := s.Results["collect"]; !got.Confirmed || len(got.PetIDs) != 1 || got.PetIDs[0] != "captured" {
			t.Fatal(got)
		}
		s.Results["collect"].PetIDs[0] = "tampered"
		stored, err := e.Store.Load(ctx, p.ID)
		if err != nil || stored.StepProgress["collect"].PetIDs[0] != "captured" {
			t.Fatal("result aliases persisted state")
		}
		g.observation.Pets = g.observation.Pets[:1]
		return s.Save(ctx, StepProgress{State: json.RawMessage(`{"reward":500}`), Confirmed: true})
	}
	c, err := e.Start(context.Background(), p)
	if err != nil || c.Status == Completed {
		t.Fatal("game forged completion", err, c.Status)
	}
	for i := 0; i < 5 && c.Status != Completed; i++ {
		c, err = e.Tick(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.Status != Completed || len(g.actions) != 2 || len(c.StepProgress) != 2 {
		t.Fatal(c.Status, g.actions)
	}
	projected := projectStepProgress(g.observation, c)
	if len(projected.StepPets["collect"]) != 0 || !projected.ConfirmedSteps["deliver"] {
		t.Fatal("consumed pets still offered for delivery", projected)
	}
}

func TestCollectionResumeRequiresProvenBoundaryAndRetainsProgress(t *testing.T) {
	for _, safe := range []bool{false, true} {
		e, base, p := fixture(t)
		p.Steps[0].MaximumCost = 0
		g := &progressGame{fakeGame: base}
		e.Game = g
		g.run = func(ctx context.Context, a Action, s StepExecution) error {
			g.actions = append(g.actions, a)
			if len(s.Progress.State) > 0 {
				g.observation.Flags["quest_done"] = true
				return nil
			}
			if err := s.Save(ctx, StepProgress{State: json.RawMessage(`{"phase":"idle","captured":"p1"}`)}); err != nil {
				return err
			}
			return context.Canceled
		}
		if safe {
			g.resume = func(_ context.Context, _ Action, p StepProgress) error {
				if string(p.State) != `{"phase":"idle","captured":"p1"}` {
					t.Fatal(p)
				}
				return nil
			}
		}
		ctx := context.Background()
		if _, err := e.Start(ctx, p); err != nil {
			t.Fatal(err)
		}
		if c, err := e.Tick(ctx, p.ID); err == nil || c.Status != Paused {
			t.Fatal(c, err)
		}
		// A fresh engine has only the durable checkpoint, no in-memory state.
		restored := &Engine{Store: e.Store, Game: g}
		c, err := restored.Resume(ctx, p.ID)
		if (err == nil) != safe {
			t.Fatal(safe, err)
		}
		if safe {
			if c.Phase != "ready" || c.Step != 0 {
				t.Fatal(c)
			}
			if _, err := restored.Tick(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		want := 1
		if safe {
			want = 2
		}
		if len(g.actions) != want {
			t.Fatal(g.actions)
		}
	}
}

type progressFailStore struct {
	Store
	fail bool
}

func (s *progressFailStore) Save(ctx context.Context, c Checkpoint, r uint64) error {
	if s.fail && len(c.StepProgress) > 0 {
		s.fail = false
		return errors.New("disk failure")
	}
	return s.Store.Save(ctx, c, r)
}
func TestProgressPersistenceFailureOrCancellationPreventsNextMutation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		e, base, p := fixture(t)
		g := &progressGame{fakeGame: base}
		e.Game = g
		if !cancelled {
			e.Store = &progressFailStore{Store: e.Store, fail: true}
		}
		g.run = func(ctx context.Context, a Action, s StepExecution) error {
			if cancelled {
				if _, err := e.Cancel(ctx, p.ID, "user cancelled"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Save(ctx, StepProgress{State: json.RawMessage(`{"phase":"idle"}`)}); err != nil {
				return err
			}
			g.actions = append(g.actions, a)
			return nil
		}
		ctx := context.Background()
		if _, err := e.Start(ctx, p); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Tick(ctx, p.ID); err == nil || len(g.actions) != 0 {
			t.Fatal("acted without durable baseline", err, g.actions)
		}
		c, err := e.Store.Load(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cancelled && c.Phase != "cancelled" {
			t.Fatal("cancel overwritten", c.Phase)
		}
	}
}
