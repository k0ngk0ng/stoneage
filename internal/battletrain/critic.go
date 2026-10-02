package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// CriticConfig is an explicit experimental value-only fit, never an implicit
// PPO default. TrainingGroups must come from the caller's frozen experiment.
// Only complete on-policy episodes are eligible; targets are undiscounted
// observed terminal returns, not true optimal values or action-quality labels.
type CriticConfig struct {
	Epochs         int      `json:"epochs"`
	BatchSize      int      `json:"batch_size"`
	LearningRate   float64  `json:"learning_rate"`
	GradientClip   float64  `json:"gradient_clip"`
	Seed           int64    `json:"seed"`
	MaxTurns       int      `json:"max_turns"`
	TrainingGroups []string `json:"training_groups"`
}

func (c CriticConfig) validate() error {
	if c.Epochs < 1 || c.Epochs > 100 || c.BatchSize < 1 || c.BatchSize > 4096 || !finite(c.LearningRate) || c.LearningRate <= 0 || !finite(c.GradientClip) || c.GradientClip <= 0 || c.MaxTurns < 1 || c.MaxTurns > 250000 || len(c.TrainingGroups) == 0 || len(c.TrainingGroups) > 4096 {
		return fmt.Errorf("invalid critic calibration settings")
	}
	previous := ""
	for _, g := range c.TrainingGroups {
		if !digest(g) || g <= previous {
			return fmt.Errorf("critic training groups must be sorted, unique experiment groups")
		}
		previous = g
	}
	return nil
}

type CriticMetrics struct {
	Turns      int     `json:"turns"`
	MeanValue  float64 `json:"mean_value"`
	MeanReturn float64 `json:"mean_return"`
	Bias       float64 `json:"bias"`
	MSE        float64 `json:"mse"`
}
type CriticReport struct {
	Schema           string        `json:"schema"`
	Config           CriticConfig  `json:"config"`
	BeforePolicy     string        `json:"before_policy"`
	AfterPolicy      string        `json:"after_policy"`
	FrozenParameters string        `json:"unchanged_non_value_parameters"`
	Sources          []string      `json:"source_trajectories"`
	Groups           []string      `json:"training_groups"`
	Episodes         int           `json:"episodes"`
	Orders           int           `json:"verified_orders"`
	OptimizerSteps   int           `json:"optimizer_steps"`
	Before           CriticMetrics `json:"before"`
	After            CriticMetrics `json:"after"`
}
type criticRow struct {
	global, memory, conditional []float32
	joint, target, value        float32
	choices                     []int
}

func criticParameters(m *battlenet.Model[float32], head bool) map[string]battlenet.Parameter[float32] {
	p := map[string]battlenet.Parameter[float32]{}
	for n, v := range m.Parameters {
		if strings.HasPrefix(n, "value.") == head {
			p[n] = v
		}
	}
	return p
}
func criticSummary(rows []criticRow) CriticMetrics {
	s := CriticMetrics{Turns: len(rows)}
	for _, r := range rows {
		v, y := float64(r.value), float64(r.target)
		s.MeanValue += v
		s.MeanReturn += y
		s.MSE += (v - y) * (v - y)
	}
	if len(rows) > 0 {
		n := float64(len(rows))
		s.MeanValue /= n
		s.MeanReturn /= n
		s.MSE /= n
		s.Bias = s.MeanValue - s.MeanReturn
	}
	return s
}
func criticBatch(m *battlenet.Model[float32], adam *battlenet.Adam[float32], rows []criticRow, indices []int, c CriticConfig) error {
	n, w := len(indices), m.Config.Width
	global, memory, targets := make([]float32, 0, n*w), make([]float32, 0, n*w), make([]float32, 0, n)
	for _, i := range indices {
		r := rows[i]
		global = append(global, r.global...)
		memory = append(memory, r.memory...)
		targets = append(targets, r.target)
	}
	g := &battlenet.Graph[float32]{Train: true}
	b := m.Bind(g)
	value := b.Value(battlenet.Encoding[float32]{Global: g.New(n, w, global), Memory: g.New(n, w, memory)})
	delta := g.Add(value, g.Scale(g.New(n, 1, targets), -1))
	loss := g.Scale(g.Sum(g.Mul(delta, delta)), .5)
	g.Backward(loss)
	gradients := b.Gradients()
	if len(gradients) != 4 {
		return fmt.Errorf("critic update touched non-value parameters")
	}
	parameters, _, e := adam.UpdateParameters(criticParameters(m, true), gradients, n, c.LearningRate, c.GradientClip)
	if e != nil {
		return e
	}
	for name, p := range parameters {
		m.Parameters[name] = p
	}
	return nil
}

// CalibrateValueHead atomically fits just the four value-head tensors with a
// fresh optimizer. It never mutates trajectories or the caller's PPO optimizer.
// Every source is verified against the original full model. On success its
// full model identity changes even though actor/history outputs are identical:
// collect NEW trajectories before PPO; never relabel the old ones.
// No weights are committed on validation failure, cancellation or failed replay.
func CalibrateValueHead(ctx context.Context, model *battlenet.Model[float32], episodes []Trajectory, c CriticConfig) (CriticReport, error) {
	report := CriticReport{Schema: "critic-calibration-v1", Config: c}
	if e := c.validate(); e != nil {
		return report, e
	}
	if model == nil || len(episodes) == 0 || len(episodes) > 10000 {
		return report, fmt.Errorf("critic calibration requires model and 1..10000 episodes")
	}
	if e := ctx.Err(); e != nil {
		return report, e
	}
	if e := model.Validate(); e != nil {
		return report, e
	}
	version, e := ModelDigest(model)
	if e != nil {
		return report, e
	}
	report.BeforePolicy = version
	frozen, e := Digest(criticParameters(model, false))
	if e != nil {
		return report, e
	}
	report.FrozenParameters = frozen
	data, e := json.Marshal(model)
	if e != nil {
		return report, e
	}
	var candidate battlenet.Model[float32]
	if e = json.Unmarshal(data, &candidate); e != nil {
		return report, e
	}
	groups, seen := map[string]bool{}, map[string]bool{}
	rows := []criticRow{}
	for i, t := range episodes {
		if e = ctx.Err(); e != nil {
			return report, e
		}
		if len(t.Steps) > c.MaxTurns-len(rows) {
			return report, fmt.Errorf("critic calibration turn limit exceeded")
		}
		if e = t.Validate(); e != nil {
			return report, e
		}
		if t.Policy != version || t.PolicyKind != "network-sampled" || !t.Terminated || t.Truncated {
			return report, fmt.Errorf("critic calibration requires complete on-policy sampled episodes")
		}
		j := sort.SearchStrings(c.TrainingGroups, t.Group)
		if j == len(c.TrainingGroups) || c.TrainingGroups[j] != t.Group {
			return report, fmt.Errorf("critic trajectory outside declared training groups")
		}
		key := fmt.Sprintf("%s:%d", t.Match, t.Side)
		if seen[key] || i > 0 && (t.Rules != episodes[0].Rules || t.Platform != episodes[0].Platform || t.Environment != episodes[0].Environment) {
			return report, fmt.Errorf("duplicate or incompatible critic episode")
		}
		seen[key], groups[t.Group] = true, true
		source, err := Digest(t)
		if err != nil {
			return report, err
		}
		report.Sources = append(report.Sources, source)
		target := float32(t.Steps[len(t.Steps)-1].Reward)
		var previous []float32
		for _, s := range t.Steps {
			if e = ctx.Err(); e != nil {
				return report, e
			}
			g := &battlenet.Graph[float32]{}
			out, err := battlepolicy.Forward(ctx, candidate.Bind(g), s.Frame, g.New(1, candidate.Config.Width, previous), s.Choices, nil)
			if err != nil {
				return report, err
			}
			if math.Abs(float64(out.Value.Data[0]-s.Value)) > 1e-5 || math.Abs(float64(out.LogProb.Data[0]-s.LogProb)) > 1e-5 {
				return report, fmt.Errorf("critic recorded value/probability mismatch")
			}
			for j, p := range out.ConditionalLogProbs {
				if math.Abs(float64(p-s.ConditionalLogProbs[j])) > 1e-5 {
					return report, fmt.Errorf("critic recorded conditional probability mismatch")
				}
			}
			for _, v := range out.Observation.Global.Data {
				if !finite(float64(v)) {
					return report, fmt.Errorf("nonfinite critic observation encoding")
				}
			}
			rows = append(rows, criticRow{global: append([]float32(nil), out.Observation.Global.Data...), memory: append([]float32(nil), out.Memory.Data...), conditional: append([]float32(nil), out.ConditionalLogProbs...), joint: out.LogProb.Data[0], target: target, value: out.Value.Data[0], choices: append([]int(nil), out.Choices...)})
			previous = append([]float32(nil), out.Memory.Data...)
			report.Orders += len(out.Choices)
		}
	}
	report.Episodes = len(episodes)
	report.Before = criticSummary(rows)
	for g := range groups {
		report.Groups = append(report.Groups, g)
	}
	sort.Strings(report.Groups)
	indices := make([]int, len(rows))
	for i := range indices {
		indices[i] = i
	}
	rng := rand.New(rand.NewSource(c.Seed))
	adam := &battlenet.Adam[float32]{}
	for epoch := 0; epoch < c.Epochs; epoch++ {
		rng.Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
		for start := 0; start < len(indices); start += c.BatchSize {
			if e = ctx.Err(); e != nil {
				return report, e
			}
			if e = criticBatch(&candidate, adam, rows, indices[start:min(start+c.BatchSize, len(indices))], c); e != nil {
				return report, e
			}
		}
	}
	if e = candidate.Validate(); e != nil {
		return report, e
	}
	afterFrozen, e := Digest(criticParameters(&candidate, false))
	if e != nil {
		return report, e
	}
	if frozen != afterFrozen {
		return report, fmt.Errorf("critic update altered actor/history parameters")
	}
	pos := 0
	for _, t := range episodes {
		var previous []float32
		for _, s := range t.Steps {
			if e = ctx.Err(); e != nil {
				return report, e
			}
			g := &battlenet.Graph[float32]{}
			out, err := battlepolicy.Forward(ctx, candidate.Bind(g), s.Frame, g.New(1, candidate.Config.Width, previous), s.Choices, nil)
			if err != nil {
				return report, err
			}
			r := &rows[pos]
			if !reflect.DeepEqual(r.choices, out.Choices) || r.joint != out.LogProb.Data[0] || !reflect.DeepEqual(r.conditional, out.ConditionalLogProbs) || !reflect.DeepEqual(r.memory, out.Memory.Data) || !reflect.DeepEqual(r.global, out.Observation.Global.Data) {
				return report, fmt.Errorf("critic update altered actor/history outputs")
			}
			r.value = out.Value.Data[0]
			previous = append([]float32(nil), out.Memory.Data...)
			pos++
		}
	}
	report.OptimizerSteps = adam.Step
	report.After = criticSummary(rows)
	report.AfterPolicy, e = ModelDigest(&candidate)
	if e != nil {
		return report, e
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	model.Parameters = candidate.Parameters
	return report, nil
}
