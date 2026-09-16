package automation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLevelConditionsRequirePositiveKnownLevels(t *testing.T) {
	for _, condition := range []Condition{
		{Kind: "character_level", Value: 0},
		{Kind: "character_level", Value: -1},
		{Kind: "pet_level", ID: "pet-a", Value: 0},
		{Kind: "pet_level", ID: "pet-a", Value: -1},
	} {
		if err := condition.Validate(); err == nil {
			t.Fatalf("invalid level condition accepted: %+v", condition)
		}
	}
	if (Condition{Kind: "character_level", Value: 1}).Match(Observation{Character: Entity{Level: 0}}) {
		t.Fatal("unknown character level satisfied a minimum")
	}
	if (Condition{Kind: "pet_level", ID: "pet-a", Value: 1}).Match(Observation{Pets: []Entity{{ID: "pet-a", Level: 0}}}) {
		t.Fatal("unknown pet level satisfied a minimum")
	}
}

func TestStartRejectsUnderleveledTaskWithActualAndRequiredLevels(t *testing.T) {
	e, g, p := fixture(t)
	p.Preconditions = []Condition{{Kind: "character_level", Value: 11}}

	pre, err := e.Preflight(context.Background(), p)
	if err != nil || pre.Ready {
		t.Fatalf("underleveled preflight accepted: %+v %v", pre, err)
	}
	joined := strings.Join(pre.Problems, "\n")
	if !strings.Contains(joined, "当前 10") || !strings.Contains(joined, "要求至少 11") {
		t.Fatalf("preflight omitted actual and required levels: %v", pre.Problems)
	}
	if _, err = e.Start(context.Background(), p); err == nil {
		t.Fatal("underleveled task started")
	}
	if _, err = e.Store.Load(context.Background(), p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("underleveled task created a checkpoint: %v", err)
	}
	if len(g.actions) != 0 {
		t.Fatal("preflight sent a game action")
	}
}

func TestStartRejectsUnderleveledPetWithActualAndRequiredLevels(t *testing.T) {
	e, g, p := fixture(t)
	p.Preconditions = []Condition{{Kind: "pet_level", ID: "pet-a", Value: 5}}
	g.observation.Pets = []Entity{{ID: "pet-a", Level: 4}}

	pre, err := e.Preflight(context.Background(), p)
	if err != nil || pre.Ready {
		t.Fatalf("underleveled pet preflight accepted: %+v %v", pre, err)
	}
	joined := strings.Join(pre.Problems, "\n")
	if !strings.Contains(joined, "当前 4") || !strings.Contains(joined, "要求至少 5") {
		t.Fatalf("preflight omitted actual and required pet levels: %v", pre.Problems)
	}
	if _, err = e.Start(context.Background(), p); err == nil {
		t.Fatal("underleveled pet task started")
	}
}

func TestTickPausesWhenTaskLevelFallsOrPetChanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pre        Condition
		prepare    func(*fakeGame)
		invalidate func(*fakeGame)
	}{
		{
			name: "character level falls",
			pre:  Condition{Kind: "character_level", Value: 10},
			invalidate: func(g *fakeGame) {
				g.observation.Character.Level = 9
			},
		},
		{
			name: "stable pet is replaced",
			pre:  Condition{Kind: "pet_level", ID: "pet-a", Value: 5},
			prepare: func(g *fakeGame) {
				g.observation.Pets = []Entity{{ID: "pet-a", Level: 5}}
			},
			invalidate: func(g *fakeGame) {
				g.observation.Pets = []Entity{{ID: "pet-b", Level: 5}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, g, p := fixture(t)
			p.Preconditions = []Condition{tc.pre}
			if tc.prepare != nil {
				tc.prepare(g)
			}
			started, err := e.Start(context.Background(), p)
			if err != nil || started.Status != Running {
				t.Fatalf("valid level gate did not start: %+v %v", started, err)
			}
			tc.invalidate(g)
			checkpoint, err := e.Tick(context.Background(), p.ID)
			if err != nil || checkpoint.Status != Paused {
				t.Fatalf("invalid level gate did not pause: %+v %v", checkpoint, err)
			}
			if !strings.Contains(checkpoint.Reason, "等级") || len(g.actions) != 0 {
				t.Fatalf("unsafe action was attempted: %+v actions=%d", checkpoint, len(g.actions))
			}
		})
	}
}

func TestVerifiedTaskLevelsAllowSubmissionAndResumeDoesNotResend(t *testing.T) {
	e, g, p := fixture(t)
	p.Preconditions = []Condition{
		{Kind: "character_level", Value: 10},
		{Kind: "pet_level", ID: "pet-a", Value: 5},
	}
	g.observation.Pets = []Entity{{ID: "pet-a", Level: 5}}
	if _, err := e.Start(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	g.observation.Character.Level = 9
	paused, err := e.Tick(context.Background(), p.ID)
	if err != nil || paused.Status != Paused || len(g.actions) != 0 {
		t.Fatalf("low-level run was not fenced: %+v %v", paused, err)
	}
	if _, err = e.Resume(context.Background(), p.ID); err == nil {
		t.Fatal("resume ignored the level gate")
	}
	if stored, loadErr := e.Store.Load(context.Background(), p.ID); loadErr != nil || stored.Status != Paused {
		t.Fatalf("failed resume changed checkpoint: %+v %v", stored, loadErr)
	}

	g.observation.Character.Level = 10
	resumed, err := e.Resume(context.Background(), p.ID)
	if err != nil || resumed.Status != Running {
		t.Fatalf("qualified resume failed: %+v %v", resumed, err)
	}
	if len(g.actions) != 0 {
		t.Fatal("resume submitted an action")
	}
	checkpoint, err := e.Tick(context.Background(), p.ID)
	if err != nil || checkpoint.Status != Running || len(g.actions) != 1 {
		t.Fatalf("qualified run did not submit exactly once: %+v %v actions=%d", checkpoint, err, len(g.actions))
	}
}

func TestResumeReconcilesObservedPendingSuccessBeforeLevelPause(t *testing.T) {
	for _, phase := range []string{"prepared", "submitted"} {
		t.Run(phase, func(t *testing.T) {
			e, g, p := fixture(t)
			p.Preconditions = []Condition{{Kind: "character_level", Value: 10}}
			p.Completion = []Condition{{Kind: "flag_set", ID: "quest_done"}}
			p.Steps[0].Success = []Condition{{Kind: "flag_set", ID: "step_done"}}
			if _, err := e.Start(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if phase == "prepared" {
				g.execute = func(Action) error { return errors.New("response lost") }
				if _, err := e.Tick(context.Background(), p.ID); err == nil {
					t.Fatal("prepared action unexpectedly succeeded")
				}
			} else {
				if _, err := e.Tick(context.Background(), p.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Pause(context.Background(), p.ID, "test pause"); err != nil {
					t.Fatal(err)
				}
			}
			g.observation.Flags["step_done"] = true
			g.observation.Character.Level = 9
			reconciled, err := e.Resume(context.Background(), p.ID)
			if err == nil || reconciled.Status != Paused || reconciled.Step != 1 || reconciled.Phase != "ready" {
				t.Fatalf("pending success was not durably reconciled before level pause: %+v %v", reconciled, err)
			}
			if len(g.actions) != 1 {
				t.Fatalf("reconciliation replayed the pending action: %d", len(g.actions))
			}
			stored, err := e.Store.Load(context.Background(), p.ID)
			if err != nil || stored.Status != Paused || stored.Step != 1 || stored.Phase != "ready" {
				t.Fatalf("reconciled checkpoint was not persisted: %+v %v", stored, err)
			}
		})
	}
}

func TestConsumedTaskItemIsNotRecheckedOnNextTick(t *testing.T) {
	e, g, p := fixture(t)
	p.Preconditions = []Condition{{Kind: "item_count", ID: "item:quest", Value: 1}}
	g.observation.Inventory["item:quest"] = 1
	g.execute = func(Action) error {
		g.observation.Inventory["item:quest"] = 0
		return nil
	}
	if _, err := e.Start(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := e.Tick(context.Background(), p.ID)
	if err != nil || checkpoint.Status != Running || len(g.actions) != 1 {
		t.Fatalf("consumed task precondition was incorrectly rechecked: %+v %v actions=%d", checkpoint, err, len(g.actions))
	}
}
