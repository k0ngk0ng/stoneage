package automation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEvaluatePreflightRejectsCancelledObservationRequest(t *testing.T) {
	_, game, plan := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluatePreflight(ctx, plan, game.observation, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight returned %v", err)
	}
}

func TestEvaluatePreflightMatchesEnginePreflight(t *testing.T) {
	e, g, p := fixture(t)

	fromEngine, err := e.Preflight(context.Background(), p)
	if err != nil {
		t.Fatalf("engine preflight: %v", err)
	}
	fromObservation, err := EvaluatePreflight(context.Background(), p, g.observation, nil)
	if err != nil {
		t.Fatalf("observation preflight: %v", err)
	}
	if !reflect.DeepEqual(fromObservation, fromEngine) {
		t.Fatalf("preflight results differ:\nengine=%+v\nobservation=%+v", fromEngine, fromObservation)
	}
}

func TestEvaluatePreflightCompletePlanStillRequiresLiveIdentityAndReadiness(t *testing.T) {
	_, g, p := fixture(t)
	p.Completion = []Condition{{Kind: "flag_set", ID: "quest_done"}}
	g.observation.Flags["quest_done"] = true

	for _, tc := range []struct {
		name string
		edit func(*Observation)
		want string
	}{
		{
			name: "disconnected",
			edit: func(o *Observation) { o.Connected = false },
			want: "游戏状态尚未同步",
		},
		{
			name: "not ready",
			edit: func(o *Observation) { o.Ready = false },
			want: "游戏状态尚未同步",
		},
		{
			name: "foreign character",
			edit: func(o *Observation) { o.CharacterID = "other:0" },
			want: "角色身份不一致",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := g.observation
			tc.edit(&o)
			pre, err := EvaluatePreflight(context.Background(), p, o, nil)
			if err != nil {
				t.Fatal(err)
			}
			if pre.AlreadyComplete || pre.Ready {
				t.Fatalf("completed plan bypassed live guard: %+v", pre)
			}
			if !strings.Contains(strings.Join(pre.Problems, "\n"), tc.want) {
				t.Fatalf("missing %q in problems: %v", tc.want, pre.Problems)
			}
		})
	}

	ready, err := EvaluatePreflight(context.Background(), p, g.observation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ready.AlreadyComplete || !ready.Ready || len(ready.Problems) != 0 {
		t.Fatalf("valid completed plan was not accepted: %+v", ready)
	}
}

func TestEvaluatePreflightReportsLevelDetailsAndUnavailableSkill(t *testing.T) {
	_, g, p := fixture(t)
	p.Preconditions = []Condition{
		{Kind: "character_level", Value: 11},
		{Kind: "pet_level", ID: "pet-a", Value: 5},
	}
	g.observation.Pets = []Entity{{ID: "pet-a", Level: 4}}
	validator := unavailableSkillGame{g}

	pre, err := EvaluatePreflight(context.Background(), p, g.observation, validator)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Ready {
		t.Fatalf("unsafe plan accepted: %+v", pre)
	}
	joined := strings.Join(pre.Problems, "\n")
	if !strings.Contains(joined, "当前 10") || !strings.Contains(joined, "要求至少 11") {
		t.Fatalf("preflight omitted actual and required levels: %v", pre.Problems)
	}
	if !strings.Contains(joined, "宠物 pet-a") || !strings.Contains(joined, "当前 4") || !strings.Contains(joined, "要求至少 5") {
		t.Fatalf("preflight omitted actual and required pet levels: %v", pre.Problems)
	}
	if !strings.Contains(joined, "步骤暂不可执行：deliver") {
		t.Fatalf("preflight omitted unavailable skill: %v", pre.Problems)
	}
}
