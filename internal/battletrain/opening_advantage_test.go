package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Numerical fixtures exercise estimation/replay, not native settlement.
func openingFixture(t *testing.T, m *battlenet.Model[float32], returns []float64) []OpeningSample {
	t.Helper()
	var out []OpeningSample
	for i, ret := range returns {
		tr := testTrajectory(t, m, 3)
		b, e := json.Marshal(tr)
		if e != nil {
			t.Fatal(e)
		}
		b = []byte(strings.ReplaceAll(string(b), `"test"`, fmt.Sprintf(`"repeat-%d"`, i)))
		if e = json.Unmarshal(b, &tr); e != nil {
			t.Fatal(e)
		}
		tr.Winner = -1
		if ret == 1 {
			tr.Winner = 0
		}
		if ret == -1 {
			tr.Winner = 1
		}
		tr.Steps[len(tr.Steps)-1].Reward = ret
		seed := int64(1100 + i)
		rng := rand.New(rand.NewSource(seed))
		var memory []float32
		for j := range tr.Steps {
			s := &tr.Steps[j]
			g := &battlenet.Graph[float32]{}
			o, e := battlepolicy.Forward(context.Background(), m.Bind(g), s.Frame, g.New(1, m.Config.Width, memory), nil, rng)
			if e != nil {
				t.Fatal(e)
			}
			s.Choices = o.Choices
			s.ConditionalLogProbs = o.ConditionalLogProbs
			s.LogProb = o.LogProb.Data[0]
			s.Value = o.Value.Data[0]
			memory = append([]float32(nil), o.Memory.Data...)
		}
		if e = tr.Validate(); e != nil {
			t.Fatal(e)
		}
		out = append(out, OpeningSample{Episode: tr, ActionSeed: seed})
	}
	return out
}

func TestOpeningBaselineLeavesOwnOutcomeOutAndHandlesDraws(t *testing.T) {
	m := testModel(t)
	for _, tc := range []struct {
		returns, want []float64
		varying       int
	}{
		{[]float64{1, 1, 1, -1}, []float64{2.0 / 3, 2.0 / 3, 2.0 / 3, -2}, 1},
		{[]float64{1, 1, 1, 1}, []float64{0, 0, 0, 0}, 0},
		{[]float64{0, 0, 0, 0}, []float64{0, 0, 0, 0}, 0},
		{[]float64{1, 0, -1, 0}, []float64{4.0 / 3, 0, -4.0 / 3, 0}, 1},
	} {
		samples := openingFixture(t, m, tc.returns)
		before, _ := json.Marshal(samples)
		_, a, s, e := openingAdvantages(context.Background(), m, samples, 4)
		if e != nil {
			t.Fatal(e)
		}
		if s.groups != 1 || s.varying != tc.varying || !digest(s.digest) {
			t.Fatal(s)
		}
		for i := range a {
			if math.Abs(a[i]-tc.want[i]) > 1e-12 {
				t.Fatal(a, tc.want)
			}
		}
		after, _ := json.Marshal(samples)
		if string(before) != string(after) {
			t.Fatal("estimation modified evidence")
		}
		// Input order only changes ordered provenance, not the matched baseline.
		samples[0], samples[3] = samples[3], samples[0]
		_, reordered, _, e := openingAdvantages(context.Background(), m, samples, 4)
		if e != nil {
			t.Fatal(e)
		}
		a[0], a[3] = a[3], a[0]
		if !reflect.DeepEqual(a, reordered) {
			t.Fatal("order-dependent baseline")
		}
	}
}

func TestOpeningBaselineRejectsBadGroupsWithoutMutatingWeights(t *testing.T) {
	for _, kind := range []string{"missing", "duplicates", "same-seed", "scenario-seed", "opponent", "sample-seed", "probability", "final-gap", "truncated", "canceled", "discount"} {
		t.Run(kind, func(t *testing.T) {
			m := testModel(t)
			samples := openingFixture(t, m, []float64{1, -1, 1, -1})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			config := DefaultPPOConfig()
			config.Epochs = 1
			switch kind {
			case "missing":
				samples = samples[:3]
			case "duplicates":
				samples[3] = samples[0]
			case "same-seed":
				samples[1].ActionSeed = samples[0].ActionSeed
			case "scenario-seed":
				samples[0].Episode.Setup.Seed++
				samples[0].Episode.Scenario, _ = Digest(samples[0].Episode.Setup)
			case "opponent":
				samples[0].Episode.Opponent = strings.Repeat("1", 64)
			case "sample-seed":
				samples[0].ActionSeed = 1234567
			case "probability":
				samples[0].Episode.Steps[0].LogProb -= .1
				samples[0].Episode.Steps[0].ConditionalLogProbs[0] -= .1
			case "final-gap":
				samples[0].Episode.FinalHistory.Gap = true
			case "truncated":
				tr := &samples[0].Episode
				successor := tr.Steps[2]
				tr.Terminated, tr.Truncated, tr.Winner = false, true, -1
				tr.Next, tr.Bootstrap = &successor.Frame, float64(successor.Value)
				tr.FinalObservations, tr.FinalHistory = successor.Observations, successor.History
				tr.Steps = tr.Steps[:2]
				if e := tr.Validate(); e != nil {
					t.Fatal("expected a valid truncated trajectory", e)
				}
			case "canceled":
				cancel()
			case "discount":
				config.Gamma = .99
			}
			adam := &battlenet.Adam[float32]{}
			before, _ := json.Marshal(m)
			if _, e := TrainRepeatedOpenings(ctx, m, adam, samples, 4, config); e == nil {
				t.Fatal("bad group accepted")
			}
			after, _ := json.Marshal(m)
			if string(before) != string(after) || adam.Step != 0 || adam.Moments != nil {
				t.Fatal("failed update mutated state")
			}
		})
	}
}

func TestOpeningEstimatorSharesRecurrentPPOAndRejectsOldRollouts(t *testing.T) {
	m := testModel(t)
	samples := openingFixture(t, m, []float64{1, -1, 1, -1})
	var clone battlenet.Model[float32]
	b, _ := json.Marshal(m)
	if e := json.Unmarshal(b, &clone); e != nil {
		t.Fatal(e)
	}
	config := DefaultPPOConfig()
	config.Epochs = 2
	config.SequenceLength = 2
	adam, restored := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	r, e := TrainRepeatedOpenings(context.Background(), m, adam, samples, 4, config)
	if e != nil {
		t.Fatal(e)
	}
	episodes, a, _, e := openingAdvantages(context.Background(), &clone, samples, 4)
	if e != nil {
		t.Fatal(e)
	}
	control, e := trainWithOpeningAdvantages(context.Background(), &clone, restored, episodes, config, a)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(r.Training, control) || !reflect.DeepEqual(*m, clone) || !reflect.DeepEqual(adam, restored) || len(r.Training.Epochs) != 2 {
		t.Fatal("different update/replay")
	}
	for _, epoch := range r.Training.Epochs {
		if epoch.PostUpdateKL == nil || *epoch.PostUpdateKL > config.TargetKL {
			t.Fatal("KL guard bypassed")
		}
	}
	if _, e = TrainRepeatedOpenings(context.Background(), m, adam, samples, 4, config); e == nil {
		t.Fatal("old rollout reused after policy change")
	}
	// Real saved weights/moments, fresh samples from the NEW policy, then the
	// same update in continuous and restored processes' numerical states.
	state := LearningState{Schema: 1, Model: m, Optimizer: adam}
	encoded, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	var reload LearningState
	if e = json.Unmarshal(encoded, &reload); e != nil {
		t.Fatal(e)
	}
	if e = reload.Validate(); e != nil {
		t.Fatal(e)
	}
	fresh := openingFixture(t, m, []float64{-1, 1, 0, 1})
	one, e := TrainRepeatedOpenings(context.Background(), m, adam, fresh, 4, config)
	if e != nil {
		t.Fatal(e)
	}
	two, e := TrainRepeatedOpenings(context.Background(), reload.Model, reload.Optimizer, fresh, 4, config)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(one, two) || !reflect.DeepEqual(m, reload.Model) || !reflect.DeepEqual(adam, reload.Optimizer) {
		t.Fatal("restored opening update differs")
	}
}

func TestOpeningFrameDropsOnlyRoutingAndPreservesEvidence(t *testing.T) {
	m := testModel(t)
	tr := testTrajectory(t, m, 1)
	f := tr.Steps[0].Frame
	original, _ := json.Marshal(f)
	id, e := openingFrameDigest(f)
	if e != nil {
		t.Fatal(e)
	}
	if now, _ := json.Marshal(f); string(now) != string(original) {
		t.Fatal("mutated caller frame")
	}
	f.Match = "new"
	f.Slots[0].Observation = "new"
	f.Slots[0].Candidates[0].ID = "new"
	other, e := openingFrameDigest(f)
	if e != nil || other != id {
		t.Fatal("routing affected equivalence", e)
	}
	f.Entities[0][0] += .1
	other, e = openingFrameDigest(f)
	if e != nil || other == id {
		t.Fatal("ignored numeric input", e)
	}
}

func TestOpeningIdenticalReturnsAreNotPositiveActionLabels(t *testing.T) {
	m := testModel(t)
	samples := openingFixture(t, m, []float64{1, 1, 1, 1})
	before, _ := ModelDigest(m)
	config := DefaultPPOConfig()
	config.Epochs, config.ValueWeight, config.EntropyWeight = 1, 0, 0
	r, e := TrainRepeatedOpenings(context.Background(), m, &battlenet.Adam[float32]{}, samples, 4, config)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := ModelDigest(m)
	if before != after || r.VaryingGroups != 0 || r.Training.Epochs[0].Objectives.PolicyLoss != 0 {
		t.Fatal("all wins incorrectly became positive action labels", r)
	}
}
