package automation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type departureGame struct {
	*fakeGame
	calls  []Action
	reject bool
}

func (g *departureGame) ValidateSkill(context.Context, Action) error { return nil }
func (g *departureGame) ValidateDeparture(_ context.Context, a Action, _ Observation) error {
	g.calls = append(g.calls, a)
	if g.reject {
		return errors.New("unreachable")
	}
	return nil
}

func TestUnreachableDepartureRejectedBeforeCheckpoint(t *testing.T) {
	e, g, p := fixture(t)
	validator := &departureGame{fakeGame: g, reject: true}
	e.Game = validator
	pre, err := e.Preflight(context.Background(), p)
	if err != nil || pre.Ready || !strings.Contains(strings.Join(pre.Problems, ""), "出发路线暂不可用") {
		t.Fatalf("bad departure accepted: %+v %v", pre, err)
	}
	if _, err := e.Start(context.Background(), p); err == nil {
		t.Fatal("unreachable task started")
	}
	if len(g.actions) != 0 || len(validator.calls) != 2 {
		t.Fatal("departure caused game actions or missed checking")
	}
	checkpoints, err := e.Store.(*SQLiteStore).List(context.Background(), p.CharacterID)
	if err != nil || len(checkpoints) != 0 {
		t.Fatalf("departure persisted checkpoints: %+v %v", checkpoints, err)
	}
}

func TestDepartureSelectsFirstUnfinishedStage(t *testing.T) {
	_, g, p := fixture(t)
	first := p.Steps[0]
	first.ID = "prerequisite"
	first.Action.Skill = "earlier"
	first.Success = []Condition{{Kind: "flag_set", ID: "prerequisite-done"}}
	p.Steps = append([]Step{first}, p.Steps...)
	p.Stages = []QuestStage{
		{ID: "prerequisite", StartStep: 0, EndStep: 1, Completion: first.Success},
		{ID: "final", Dependencies: []string{"prerequisite"}, StartStep: 1, EndStep: 2, Completion: p.Completion},
	}
	g.observation.Flags["prerequisite-done"] = true
	validator := &departureGame{fakeGame: g}
	pre, err := EvaluatePreflight(context.Background(), p, g.observation, validator)
	if err != nil || !pre.Ready || len(validator.calls) != 1 || validator.calls[0].Skill != p.Steps[1].Action.Skill {
		t.Fatalf("wrong stage checked: %+v %+v %v", pre, validator.calls, err)
	}
	g.observation.Flags["quest_done"] = true
	pre, err = EvaluatePreflight(context.Background(), p, g.observation, validator)
	if err != nil || !pre.AlreadyComplete || len(validator.calls) != 1 {
		t.Fatal("completed task planned a departure")
	}
}
