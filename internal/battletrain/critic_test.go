package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func criticTestConfig(t Trajectory) CriticConfig {
	return CriticConfig{Epochs: 16, BatchSize: 2, LearningRate: .001, GradientClip: .5, Seed: 2411, MaxTurns: 100, TrainingGroups: []string{t.Group}}
}
func TestCriticCalibrationAtomicDeterministicAndFreshPPO(t *testing.T) {
	m := testModel(t)
	tr := testTrajectory(t, m, 5)
	c := criticTestConfig(tr)
	before, _ := ModelDigest(m)
	source, _ := Digest(tr)
	frozen, _ := Digest(criticParameters(m, false))
	bytes, _ := json.Marshal(m)
	var second battlenet.Model[float32]
	if e := json.Unmarshal(bytes, &second); e != nil {
		t.Fatal(e)
	}
	r, e := CalibrateValueHead(context.Background(), m, []Trajectory{tr}, c)
	if e != nil {
		t.Fatal(e)
	}
	if r.Schema != "critic-calibration-v1" || r.BeforePolicy != before || r.AfterPolicy == before || r.OptimizerSteps != 48 || r.Episodes != 1 || r.Before.Turns != 5 || r.After.Turns != 5 || r.Orders != 5 || r.After.MSE >= r.Before.MSE {
		t.Fatalf("bad report %+v", r)
	}
	afterFrozen, _ := Digest(criticParameters(m, false))
	afterSource, _ := Digest(tr)
	if frozen != afterFrozen || frozen != r.FrozenParameters || source != afterSource || !reflect.DeepEqual(r.Sources, []string{source}) || !reflect.DeepEqual(r.Groups, []string{tr.Group}) {
		t.Fatal("source/actor provenance changed")
	}
	rr, e := CalibrateValueHead(context.Background(), &second, []Trajectory{tr}, c)
	if e != nil || !reflect.DeepEqual(r, rr) {
		t.Fatal("non-deterministic calibration", e)
	}
	// Full identity changes even though every actor output was preserved. Old
	// trajectories MUST NOT be silently accepted by the existing PPO entry point.
	adam := &battlenet.Adam[float32]{}
	if _, e = Train(context.Background(), m, adam, []Trajectory{tr}, DefaultPPOConfig()); e == nil {
		t.Fatal("PPO accepted pre-calibration batch")
	}
	if adam.Step != 0 {
		t.Fatal("rejected old batch changed PPO optimizer")
	}
	fresh := testTrajectory(t, m, 5)
	if _, e = Train(context.Background(), m, adam, []Trajectory{fresh}, DefaultPPOConfig()); e != nil {
		t.Fatal("fresh behavior rejected", e)
	}
}
func TestCriticCalibrationRejectsInvalidSourcesWithoutMutation(t *testing.T) {
	for _, kind := range []string{"value", "probability", "outside-training", "duplicate", "rule", "truncated", "turn-limit", "unknown-group", "bad-rate", "canceled", "missing-observation"} {
		t.Run(kind, func(t *testing.T) {
			m := testModel(t)
			tr := testTrajectory(t, m, 3)
			c := criticTestConfig(tr)
			episodes := []Trajectory{tr}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "value":
				tr.Steps[1].Value += .02
			case "probability":
				tr.Steps[1].LogProb -= .1
				tr.Steps[1].ConditionalLogProbs[0] -= .1
			case "outside-training":
				c.TrainingGroups = []string{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
			case "duplicate":
				episodes = append(episodes, tr)
			case "rule":
				episodes[0].PolicyKind = "network-greedy"
			case "truncated":
				episodes[0].Terminated = false
				episodes[0].Truncated = true
			case "turn-limit":
				c.MaxTurns = 2
			case "unknown-group":
				c.TrainingGroups = []string{"invalid"}
			case "bad-rate":
				c.LearningRate = math.NaN()
			case "canceled":
				cancel()
			case "missing-observation":
				tr.Steps[1].Observations = nil
			}
			before, _ := ModelDigest(m)
			source, _ := Digest(episodes)
			_, e := CalibrateValueHead(ctx, m, episodes, c)
			if e == nil {
				t.Fatal("invalid fit accepted")
			}
			if kind == "canceled" && !errors.Is(e, context.Canceled) {
				t.Fatal("cancellation identity lost", e)
			}
			after, _ := ModelDigest(m)
			afterSource, _ := Digest(episodes)
			if before != after || source != afterSource {
				t.Fatal("failure mutated caller input")
			}
		})
	}
}

type countedCriticContext struct {
	context.Context
	calls, limit int
}

func (c *countedCriticContext) Err() error {
	c.calls++
	if c.limit > 0 && c.calls >= c.limit {
		return context.Canceled
	}
	return nil
}
func TestCriticCalibrationLateCancellationRollsBack(t *testing.T) {
	m := testModel(t)
	tr := testTrajectory(t, m, 5)
	c := criticTestConfig(tr)
	probe := &countedCriticContext{Context: context.Background()}
	if _, e := CalibrateValueHead(probe, m, []Trajectory{tr}, c); e != nil {
		t.Fatal(e)
	}
	m = testModel(t)
	before, _ := ModelDigest(m)
	late := &countedCriticContext{Context: context.Background(), limit: probe.calls - 1}
	r, e := CalibrateValueHead(late, m, []Trajectory{tr}, c)
	if !errors.Is(e, context.Canceled) || r.Before.Turns != 5 {
		t.Fatalf("did not cancel after cache/update: %+v %v", r, e)
	}
	after, _ := ModelDigest(m)
	if after != before {
		t.Fatal("late failure committed partial fit")
	}
}
func TestCriticMinibatchAveragesGradients(t *testing.T) {
	a, b := testModel(t), testModel(t)
	w := a.Config.Width
	x, y := make([]float32, w), make([]float32, w)
	x[0], y[1] = 1, 1
	rows := []criticRow{{global: x, memory: y, target: -1}, {global: x, memory: y, target: -1}}
	// Disable clipping: clipping and Adam's first-step scale invariance can
	// otherwise conceal missing batch averaging in a weights-only assertion.
	c := CriticConfig{LearningRate: .001, GradientClip: 1000}
	one, two := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	if e := criticBatch(a, one, rows, []int{0}, c); e != nil {
		t.Fatal(e)
	}
	if e := criticBatch(b, two, rows, []int{0, 1}, c); e != nil {
		t.Fatal(e)
	}
	if len(one.Moments) != 4 || len(two.Moments) != 4 {
		t.Fatal("missing value-head moments")
	}
	nonzero := 0
	for name, moments := range one.Moments {
		other := two.Moments[name]
		if len(moments.First) != len(other.First) || len(moments.Second) != len(other.Second) {
			t.Fatal("moment shape mismatch")
		}
		for i, v := range moments.First {
			if math.Abs(float64(v)) > 1e-5 {
				nonzero++
			}
			if math.Abs(float64(v-other.First[i])) > 1e-7 || math.Abs(float64(moments.Second[i]-other.Second[i])) > 1e-7 {
				t.Fatalf("averaged optimizer moments differ: %s[%d]", name, i)
			}
		}
	}
	if nonzero == 0 {
		t.Fatal("degenerate gradient cannot test averaging")
	}
	for name, p := range a.Parameters {
		for i, v := range p.Values {
			if math.Abs(float64(v-b.Parameters[name].Values[i])) > 1e-7 {
				t.Fatalf("batch normalization differs: %s[%d]", name, i)
			}
		}
	}
}
