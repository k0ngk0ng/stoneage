package battletrain

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Collect runs one simultaneous game with two immutable network snapshots.
// Both policies act before Advance; neither sees the other's selected plan.
// ruleDigest must identify the actual engine/tables used by the caller, not a
// protocol compatibility constant. A failed/uncertain match returns no sample.
func Collect(ctx context.Context, engine *battleenv.Native, scenario battleenv.Scenario, models [2]*battlenet.Model[float32], rng [2]*rand.Rand, group, ruleDigest string) ([2]Trajectory, error) {
	return CollectPolicies(ctx, engine, scenario, [2]Policy{{Model: models[0]}, {Model: models[1]}}, rng, group, ruleDigest)
}

func CollectPolicies(ctx context.Context, engine *battleenv.Native, scenario battleenv.Scenario, policies [2]Policy, rng [2]*rand.Rand, group, ruleDigest string) ([2]Trajectory, error) {
	var result [2]Trajectory
	if engine == nil {
		return result, fmt.Errorf("engine, experiment group and rules digest required")
	}
	canonical := ScenarioGroup(scenario)
	if group != "" && group != canonical {
		return result, fmt.Errorf("experiment group must match canonical initial configuration")
	}
	group = canonical
	metadata := engine.Metadata()
	if e := metadata.Validate(); e != nil {
		return result, e
	}
	if ruleDigest != "" && ruleDigest != metadata.Rules {
		return result, fmt.Errorf("requested rules digest differs from running engine")
	}
	ruleDigest = metadata.Rules
	scenarioDigest, e := Digest(scenario)
	if e != nil {
		return result, e
	}
	var versions [2]string
	for side, p := range policies {
		if p.Model == nil && p.Features == "" && metadata.Scenario == "controlled-battle-v7" {
			p.Features = battlepolicy.RecipientFeatureVersion
			policies[side] = p
		}
		if e = battlepolicy.ValidateFeatureEnvironment(p.features(), metadata.Scenario); e != nil {
			return result, e
		}
		versions[side], e = p.Version()
		if e != nil {
			return result, e
		}
		if rng[side] == nil && p.Kind() == "network-sampled" {
			return result, fmt.Errorf("sampling RNG required")
		}
	}
	state, e := engine.Reset(ctx, scenario)
	if e != nil {
		return result, e
	}
	for side := range result {
		result[side] = Trajectory{Schema: TrajectorySchema, Match: state.Match, Group: group, Policy: versions[side], Opponent: versions[1-side], Rules: ruleDigest, Platform: metadata.Platform, Environment: metadata.Scenario, Scenario: scenarioDigest, Setup: scenario, Side: side, Mode: scenario.Mode}
		result[side].PolicyKind = policies[side].Kind()
	}
	var memory [2][]float32
	var streams [2]string
	var cursors [2]uint64
	frame := func(side int) (battlepolicy.Frame, error) {
		i := side * scenario.Mode
		return battlepolicy.EncodeVersion(state.Views[i:i+scenario.Mode], battlepolicy.History{Batch: &state.Events[i], PreviousStream: streams[side], PreviousCursor: cursors[side], First: state.Turn == 0}, policies[side].features())
	}
	for !state.Terminated && !state.Truncated {
		var joint [][]aigame.BattleSelection
		for side, p := range policies {
			f, e := frame(side)
			if e != nil {
				return [2]Trajectory{}, e
			}
			if f.Events[12] != 0 {
				return [2]Trajectory{}, fmt.Errorf("history gap excludes this rollout")
			}
			o, e := p.decide(ctx, f, memory[side], rng[side])
			if e != nil {
				return [2]Trajectory{}, e
			}
			choices, e := f.Selections(o.choices)
			if e != nil {
				return [2]Trajectory{}, e
			}
			joint = append(joint, choices...)
			i := side * scenario.Mode
			result[side].Steps = append(result[side].Steps, Transition{Frame: f, Choices: o.choices, ConditionalLogProbs: o.conditional, LogProb: o.logProb, Value: o.value, Observations: state.Views[i : i+scenario.Mode], History: state.Events[i]})
			memory[side] = o.memory
			streams[side], cursors[side] = state.Events[i].Stream, state.Events[i].Cursor
		}
		state, e = engine.Advance(ctx, joint)
		if e != nil {
			return [2]Trajectory{}, e
		}
	}
	for side, p := range policies {
		t := &result[side]
		t.Terminated, t.Truncated, t.Winner = state.Terminated, state.Truncated, state.Winner
		i := side * scenario.Mode
		t.FinalObservations, t.FinalHistory = state.Views[i:i+scenario.Mode], state.Events[i]
		if t.Truncated {
			f, e := frame(side)
			if e != nil {
				return [2]Trajectory{}, fmt.Errorf("cannot bootstrap truncation: %w", e)
			}
			value, e := p.value(ctx, f, memory[side])
			if e != nil {
				return [2]Trajectory{}, e
			}
			t.Next, t.Bootstrap = &f, value
		} else if t.Winner >= 0 {
			t.Steps[len(t.Steps)-1].Reward = -1
			if t.Winner == side {
				t.Steps[len(t.Steps)-1].Reward = 1
			}
		}
		if e = t.Validate(); e != nil {
			return [2]Trajectory{}, e
		}
		version, e := p.Version()
		if e != nil {
			return [2]Trajectory{}, e
		}
		if version != versions[side] {
			return [2]Trajectory{}, fmt.Errorf("policy changed during collection")
		}
	}
	return result, nil
}
