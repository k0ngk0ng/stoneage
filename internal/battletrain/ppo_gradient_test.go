package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Check the gradient that actually reaches Adam, rather than a separate
// reconstructed autodiff graph. Finite differences hold behavior advantages,
// targets and segment-start memory fixed. Recomputing the latter under the
// perturbation would test full BPTT instead of the configured truncated BPTT.
func checkPPOGradient(t *testing.T, model *battlenet.Model[float32], episodes []Trajectory, sequence int) {
	t.Helper()
	ctx := context.Background()
	config := DefaultPPOConfig()
	config.Epochs, config.SequenceLength = 1, sequence
	config.GradientClip, config.LearningRate = 10000, 1e-6
	var candidate battlenet.Model[float32]
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &candidate); err != nil {
		t.Fatal(err)
	}
	adam := &battlenet.Adam[float32]{}
	report, err := Train(ctx, &candidate, adam, episodes, config)
	if err != nil || len(report.Epochs) != 1 || adam.Step != 1 {
		t.Fatal("one real PPO update required", report, err)
	}
	if report.Epochs[0].GradientNorm >= config.GradientClip {
		t.Fatal("gradient was clipped")
	}

	advantages, targets := make([][]float64, len(episodes)), make([][]float64, len(episodes))
	memories := make([][][]float32, len(episodes))
	sum, squares, turns := 0., 0., 0
	for i, episode := range episodes {
		n := len(episode.Steps)
		advantages[i], targets[i], memories[i] = make([]float64, n), make([]float64, n), make([][]float32, n+1)
		for j, step := range episode.Steps {
			// Independent forward sum of TD residuals, not Advantages().
			for k := j; k < n; k++ {
				next := episode.Bootstrap
				if k+1 < n {
					next = float64(episode.Steps[k+1].Value)
				}
				residual := episode.Steps[k].Reward + config.Gamma*next - float64(episode.Steps[k].Value)
				advantages[i][j] += math.Pow(config.Gamma*config.Lambda, float64(k-j)) * residual
			}
			targets[i][j] = advantages[i][j] + float64(step.Value)
			sum += advantages[i][j]
			squares += advantages[i][j] * advantages[i][j]
			g := &battlenet.Graph[float32]{}
			o, err := battlepolicy.Forward(ctx, model.Bind(g), step.Frame, g.New(1, model.Config.Width, memories[i][j]), step.Choices, nil)
			if err != nil {
				t.Fatal(err)
			}
			memories[i][j+1] = append([]float32(nil), o.Memory.Data...)
		}
		turns += n
	}
	mean := sum / float64(turns)
	std := math.Sqrt(math.Max(0, squares/float64(turns)-mean*mean))
	for i := range advantages {
		for j := range advantages[i] {
			if std > 1e-8 {
				advantages[i][j] = (advantages[i][j] - mean) / (std + 1e-8)
			}
		}
	}
	// Evaluate the numerical reference in float64 using the same generic
	// kernels, avoiding subtraction of rounded float32 joint log probabilities.
	var reference battlenet.Model[float64]
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	objective := func() float64 {
		loss := 0.
		for i, episode := range episodes {
			var memory []float64
			for j, step := range episode.Steps {
				if j%sequence == 0 {
					memory = make([]float64, model.Config.Width)
					for k, value := range memories[i][j] {
						memory[k] = float64(value)
					}
				}
				logp, value, entropy, next := ppoReferenceForward(t, &reference, step, memory)
				memory = next
				ratio := math.Exp(logp - float64(step.LogProb))
				if math.Abs(ratio-1) >= config.Clip {
					t.Fatal("finite difference crossed PPO clip boundary")
				}
				d := value - targets[i][j]
				loss += -ratio*advantages[i][j] + .5*config.ValueWeight*d*d - config.EntropyWeight*entropy
			}
		}
		return loss / float64(turns)
	}
	baseline := objective()
	if math.Abs(baseline-report.Epochs[0].Loss) > 1e-5 {
		t.Fatal("independent objective differs", baseline, report.Epochs[0].Loss)
	}
	names := []string{"entity.0.w", "block.0.q.w", "history.gates.w", "candidate.0.w", "plan.gates.w", "score.0.w", "value.0.w"}
	if model.Config.PlanFeatures != "" {
		names = append(names, "score.plan.w")
	}
	for _, name := range names {
		if name == "plan.gates.w" && len(episodes[0].Steps[0].Choices) == 1 {
			continue // A single acting slot has no following conditional choice.
		}
		parameter := reference.Parameters[name]
		before := append([]float64(nil), parameter.Values...)
		gradient := adam.Moments[name].First
		norm := 0.
		for _, value := range gradient {
			norm += math.Pow(float64(value)/.1, 2)
		}
		norm = math.Sqrt(norm)
		if norm < 1e-7 {
			t.Fatal("unexercised gradient block", name, norm)
		}
		// Unit vector along this block's observed gradient gives a detectable
		// directional derivative while perturbing every parameter in the block.
		epsilons := []float64{1e-5, 5e-6}
		if model.Config.PlanFeatures != "" {
			// Trained projection fixtures can be close to a ReLU boundary.
			// Use finer float64 perturbations, retaining the same tolerance
			// and two-step agreement rather than broadening acceptance.
			epsilons = []float64{1e-6, 5e-7}
		}
		for _, epsilon := range epsilons {
			values := [2]float64{}
			for side, sign := range []float64{1, -1} {
				for j := range parameter.Values {
					parameter.Values[j] = before[j] + sign*epsilon*float64(gradient[j])/(.1*norm)
				}
				values[side] = objective()
			}
			copy(parameter.Values, before)
			finiteDifference := (values[0] - values[1]) / (2 * epsilon)
			if math.Abs(finiteDifference-norm) > 1e-5+.001*norm {
				t.Fatalf("sequence=%d %s epsilon=%g Adam gradient=%g finite difference=%g", sequence, name, epsilon, norm, finiteDifference)
			}
			t.Logf("sequence=%d block=%s epsilon=%g Adam=%g finite_difference=%g", sequence, name, epsilon, norm, finiteDifference)
		}
	}
}

// Independent plan traversal: scalar float64 likelihood/entropy arithmetic,
// no PPOLoss, no autodiff and no training-loop helper. All joint choices and
// within-turn plan memory remain, including the effects of earlier teammates.
func ppoReferenceForward(t *testing.T, model *battlenet.Model[float64], step Transition, memory []float64) (float64, float64, float64, []float64) {
	t.Helper()
	g := &battlenet.Graph[float64]{}
	b := model.Bind(g)
	f := step.Frame
	entities, events := []float64{}, []float64{}
	for _, row := range f.Entities {
		for _, x := range row {
			entities = append(entities, float64(x))
		}
	}
	for _, row := range f.EventSequence {
		for _, x := range row {
			events = append(events, float64(x))
		}
	}
	for _, x := range f.Events {
		events = append(events, float64(x))
	}
	state, err := b.EncodeSequence(context.Background(), g.New(len(f.Entities), battlepolicy.EntityFeatures, entities), g.New(len(f.EventSequence)+1, battlepolicy.EventFeatures, events), g.New(1, model.Config.Width, memory))
	if err != nil {
		t.Fatal(err)
	}
	joint, entropy := 0., 0.
	prefix := g.New(1, model.Config.Width, nil)
	memberPrefixes := make([]*battlenet.Tensor[float64], f.Mode)
	for i, slot := range f.Slots {
		if model.Config.PlanScope == "member" {
			prefix = memberPrefixes[slot.Member]
			if prefix == nil {
				prefix = g.New(1, model.Config.Width, nil)
			}
		}
		features, targets, mask := []float64{}, []int{}, []bool{}
		for _, c := range slot.Candidates {
			for _, x := range c.Features {
				features = append(features, float64(x))
			}
			targets, mask = append(targets, c.Target), append(mask, c.Supported)
		}
		var counts *battlenet.Tensor[float64]
		if model.Config.PlanFeatures != "" {
			values, err := battlepolicy.PlanTargetCounts(f, i, step.Choices[:i], model.Config.PlanScope)
			if err != nil {
				t.Fatal(err)
			}
			numbers := make([]float64, len(values))
			for j, v := range values {
				numbers[j] = float64(v)
			}
			counts = g.New(len(slot.Candidates), battlenet.PlanTargetFeatures, numbers)
		}
		logits, embeddings := b.ScoreWithPlanCounts(state, slot.Entity, g.New(len(slot.Candidates), battlepolicy.CandidateFeatures, features), targets, prefix, counts)
		logp := g.LogSoftmax(logits, mask)
		choice := step.Choices[i]
		joint += logp.Data[choice]
		for j, p := range logp.Data {
			if mask[j] {
				entropy -= math.Exp(p) * p
			}
		}
		prefix = b.AdvancePlan(prefix, g.Slice(embeddings, choice, 1, 0, model.Config.Width))
		if model.Config.PlanScope == "member" {
			memberPrefixes[slot.Member] = prefix
		}
	}
	return joint, b.Value(state).Data[0], entropy, state.Memory.Data
}

func TestPPOUpdateGradientFiniteDifference(t *testing.T) {
	for _, sequence := range []int{1, 2, 5} {
		model := testModel(t)
		checkPPOGradient(t, model, []Trajectory{testTrajectory(t, model, 5)}, sequence)
	}
}

func TestNativeTeamPPOUpdateGradientFiniteDifference(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	model := testModel(t)
	config := DefaultRunConfig()
	config.Mode, config.MaxTurns = 2, 6
	config.HealingMagic, config.HealingItems, config.ReservePets = 20, 2, 2
	scenario, group := scenarioFor(config, 173)
	trajectories, err := Collect(ctx, engine, scenario, [2]*battlenet.Model[float32]{model, model}, [2]*rand.Rand{rand.New(rand.NewSource(19)), rand.New(rand.NewSource(23))}, group, engine.Metadata().Rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, episode := range trajectories {
		if !episode.Truncated || len(episode.Steps) != 6 || len(episode.Steps[0].Choices) != 4 {
			t.Fatal("six-turn native team fixture required")
		}
	}
	for _, sequence := range []int{1, 2, 6} {
		checkPPOGradient(t, model, trajectories[:], sequence)
	}
}
