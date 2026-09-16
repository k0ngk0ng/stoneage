package automation

import (
	"context"
	"strings"
	"testing"
)

func TestPreflightExplainsDeparturePreparationWithoutActions(t *testing.T) {
	for _, test := range []struct {
		name      string
		condition Condition
		edit      func(*Observation)
		want      string
	}{
		{"unknown inventory", Condition{Kind: "backpack_free_slots", Value: 15}, func(o *Observation) { o.OwnProgress = map[string]int{"backpack_used_slots": 0} }, "背包资料尚未同步"},
		{"invalid occupancy", Condition{Kind: "backpack_free_slots", Value: 15}, func(o *Observation) {
			o.Flags["inventory:known"] = true
			o.OwnProgress = map[string]int{"backpack_used_slots": -1}
		}, "背包资料尚未同步"},
		{"occupied backpack", Condition{Kind: "backpack_free_slots", Value: 15}, func(o *Observation) {
			o.Flags["inventory:known"] = true
			o.OwnProgress = map[string]int{"backpack_used_slots": 3}
		}, "当前 12，要求至少 15"},
		{"unknown party", Condition{Kind: "flag_set", ID: "party:solo"}, func(o *Observation) {}, "队伍状态尚未同步"},
		{"in party", Condition{Kind: "flag_set", ID: "party:solo"}, func(o *Observation) { o.Flags["party:solo"] = false }, "请先退出队伍"},
		{"injured", Condition{Kind: "character_hp_full"}, func(o *Observation) { o.Character.HP = 60 }, "当前 60/100，请先恢复"},
		{"unknown health", Condition{Kind: "character_hp_percent", Value: 80}, func(o *Observation) { o.Character.MaxHP = 0 }, "生命资料尚未同步"},
		{"health threshold", Condition{Kind: "character_hp_percent", Value: 80}, func(o *Observation) { o.Character.HP = 60 }, "要求至少 80%"},
		{"dead", Condition{Kind: "alive"}, func(o *Observation) { o.Dead = true; o.Character.HP = 0 }, "请先复活"},
		{"battle", Condition{Kind: "not_battle"}, func(o *Observation) { o.Battle = true }, "请结束战斗"},
		{"gold", Condition{Kind: "gold_at_least", Value: 600}, func(o *Observation) {}, "当前 500，要求至少 600"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, g, p := fixture(t)
			p.Preconditions = []Condition{test.condition, test.condition}
			test.edit(&g.observation)
			pre, err := EvaluatePreflight(context.Background(), p, g.observation, nil)
			if err != nil || pre.Ready || !strings.Contains(strings.Join(pre.Problems, "\n"), test.want) {
				t.Fatalf("preparation missing: %+v %v", pre, err)
			}
			if strings.Count(strings.Join(pre.Problems, "\n"), test.want) != 1 {
				t.Fatal("duplicate preparation message")
			}
			if _, err := e.Start(context.Background(), p); err == nil {
				t.Fatal("unprepared plan started")
			}
			if len(g.actions) != 0 {
				t.Fatal("preparation submitted gameplay")
			}
		})
	}
}

func TestSatisfiedPreparationDoesNotInventProblems(t *testing.T) {
	_, g, p := fixture(t)
	p.Preconditions = []Condition{{Kind: "backpack_free_slots", Value: 15}, {Kind: "flag_set", ID: "party:solo"}, {Kind: "character_hp_full"}, {Kind: "gold_at_least", Value: 600}}
	g.observation.OwnProgress = map[string]int{"backpack_used_slots": 0}
	g.observation.Flags["inventory:known"] = true
	g.observation.Flags["party:solo"] = true
	g.observation.UnlimitedFunds = true
	pre, err := EvaluatePreflight(context.Background(), p, g.observation, nil)
	if err != nil || !pre.Ready || len(pre.Problems) != 0 {
		t.Fatalf("ready plan rejected: %+v %v", pre, err)
	}
}
