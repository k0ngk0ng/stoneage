package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type ImitationConfig struct {
	ActionWeighting string  `json:"action_weighting,omitempty"`
	BatchEpisodes   int     `json:"batch_episodes,omitempty"` // 0 preserves the original full-batch update/order.
	SequenceLength  int     `json:"sequence_length"`
	LearningRate    float64 `json:"learning_rate"`
	GradientClip    float64 `json:"gradient_clip"`
}

func (c ImitationConfig) validate() error {
	if c.ActionWeighting != "" && c.ActionWeighting != SqrtActionFrequency {
		return fmt.Errorf("unsupported imitation action weighting %q", c.ActionWeighting)
	}
	if c.BatchEpisodes < 0 || c.BatchEpisodes > 2000 || c.SequenceLength < 1 || c.SequenceLength > 128 || !finite(c.LearningRate) || c.LearningRate <= 0 || !finite(c.GradientClip) || c.GradientClip <= 0 {
		return fmt.Errorf("invalid imitation update settings")
	}
	return nil
}

type ImitationReport struct {
	ActionWeighting      *ImitationActionWeights `json:"action_weighting,omitempty"`
	WeightedCrossEntropy *float64                `json:"weighted_cross_entropy_before_update,omitempty"`
	OptimizerUpdates     int                     `json:"optimizer_updates,omitempty"`
	BeforePolicy         string                  `json:"before_policy"`
	AfterPolicy          string                  `json:"after_policy"`
	Teachers             []string                `json:"teachers"`
	Episodes             int                     `json:"episodes"`
	TeamTurns            int                     `json:"team_turns"`
	Actions              int                     `json:"actions"`
	CrossEntropy         float64                 `json:"cross_entropy_before_update"`
	GradientNorm         float64                 `json:"gradient_norm"`
}

// Imitate performs one recurrent behavior-cloning epoch. Teacher trajectories
// are checked against their frozen observation-only rule, including losing and
// truncated games. Outcomes are NOT action-quality labels or value targets.
// This is intentionally separate from the on-policy PPO path.
func Imitate(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config ImitationConfig) (ImitationReport, error) {
	report, e := imitationReport(ctx, model, optimizer, config)
	if e != nil {
		return report, e
	}
	if len(episodes) == 0 {
		return report, fmt.Errorf("imitation requires teacher trajectories")
	}
	var data teacherDataset
	for _, t := range episodes {
		if e := data.add(ctx, t); e != nil {
			return report, e
		}
	}
	return data.updateValidated(ctx, model, optimizer, config, report)
}

func imitationReport(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], config ImitationConfig) (ImitationReport, error) {
	var report ImitationReport
	if e := config.validate(); e != nil {
		return report, e
	}
	state := LearningState{Schema: 1, Model: model, Optimizer: optimizer}
	if e := state.Validate(); e != nil {
		return report, e
	}
	if e := ctx.Err(); e != nil {
		return report, e
	}
	version, e := ModelDigest(model)
	if e != nil {
		return report, e
	}
	report.BeforePolicy = version
	return report, nil
}

type imitationStep struct {
	Frame   battlepolicy.Frame
	Choices []int
}

// Private, validated numerical inputs. Raw observations, effects and terminal
// snapshots remain in immutable shards, not resident throughout every epoch.
// Frames and choices are read-only after add; this is not a persisted format or
// an entry point for unvalidated numerical labels.
type teacherDataset struct {
	sequences                    [][]imitationStep
	groups                       []string
	seen, teachers               map[string]bool
	rules, platform, environment string
	mode, actions, turns         int
}

func (d *teacherDataset) add(ctx context.Context, t Trajectory) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := t.Validate(); e != nil {
		return e
	}
	rule := strings.TrimPrefix(t.PolicyKind, "rule:")
	id, e := (Policy{Rule: rule}).Version()
	if e != nil || !strings.HasPrefix(t.PolicyKind, "rule:") || id != t.Policy {
		return fmt.Errorf("imitation requires a known frozen rule identity")
	}
	key := fmt.Sprintf("%s:%d", t.Match, t.Side)
	if d.seen[key] || len(d.sequences) > 0 && (t.Rules != d.rules || t.Platform != d.platform || t.Environment != d.environment || t.Mode != d.mode) {
		return fmt.Errorf("duplicate or incompatible teacher trajectory")
	}
	if t.Bootstrap != 0 {
		return fmt.Errorf("rule trajectory has a learned bootstrap value")
	}
	sequence := make([]imitationStep, 0, len(t.Steps))
	actions := 0
	for _, s := range t.Steps {
		if e := ctx.Err(); e != nil {
			return e
		}
		choices, e := battlepolicy.RuleChoices(s.Frame, rule)
		if e != nil || !reflect.DeepEqual(choices, s.Choices) || s.LogProb != 0 || s.Value != 0 {
			return fmt.Errorf("teacher choice/probability differs from frozen rule")
		}
		for _, p := range s.ConditionalLogProbs {
			if p != 0 {
				return fmt.Errorf("rule behavior is not a point mass")
			}
		}
		sequence = append(sequence, imitationStep{s.Frame, s.Choices})
		actions += len(s.Choices)
	}
	if len(d.sequences) == 0 {
		d.rules, d.platform, d.environment, d.mode = t.Rules, t.Platform, t.Environment, t.Mode
		d.seen, d.teachers = map[string]bool{}, map[string]bool{}
	}
	d.seen[key], d.teachers[rule] = true, true
	d.sequences = append(d.sequences, sequence)
	d.groups = append(d.groups, t.Group)
	d.actions += actions
	d.turns += len(t.Steps)
	return nil
}

func (d *teacherDataset) update(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], config ImitationConfig) (ImitationReport, error) {
	report, e := imitationReport(ctx, model, optimizer, config)
	if e != nil {
		return report, e
	}
	return d.updateValidated(ctx, model, optimizer, config, report)
}

func (d *teacherDataset) updateValidated(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], config ImitationConfig, report ImitationReport) (ImitationReport, error) {
	if len(d.sequences) == 0 {
		return report, fmt.Errorf("imitation requires teacher trajectories")
	}
	report.Episodes, report.TeamTurns, report.Actions = len(d.sequences), d.turns, d.actions
	for rule := range d.teachers {
		report.Teachers = append(report.Teachers, rule)
	}
	sort.Strings(report.Teachers)
	return imitateValidated(ctx, model, optimizer, d.sequences, config, report)
}

// Both sources validate their own provenance first; the numerical update is
// shared. Recorded demonstrations never acquire fabricated PPO probabilities.
func imitateValidated(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes [][]imitationStep, config ImitationConfig, report ImitationReport) (ImitationReport, error) {
	if report.Actions < 1 {
		return report, fmt.Errorf("imitation requires demonstrated actions")
	}
	weights, e := imitationActionWeights(ctx, episodes, config, report.Actions)
	if e != nil {
		return report, e
	}
	report.ActionWeighting = weights
	if weights != nil {
		report.WeightedCrossEntropy = new(float64)
	}
	// Clone both weights and optimizer, so cancellation and any bad gradient
	// leave the caller's state untouched, even after substantial computation.
	state := LearningState{Schema: 1, Model: model, Optimizer: optimizer}
	data, e := json.Marshal(state)
	if e != nil {
		return report, e
	}
	var candidate LearningState
	if e = json.Unmarshal(data, &candidate); e != nil {
		return report, e
	}
	order := make([]int, len(episodes))
	for i := range order {
		order[i] = i
	}
	batchSize := len(episodes)
	if config.BatchEpisodes > 0 {
		batchSize = config.BatchEpisodes
		// Saved optimizer progress determines the shuffle. Match/event IDs may
		// differ between native worker processes and must not change training.
		rng := rand.New(rand.NewSource(gameSeed(int64(candidate.Optimizer.Step), 0, 81)))
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	}
	for first := 0; first < len(order); first += batchSize {
		gradients := map[string][]float32{}
		batchActions := 0
		for _, index := range order[first:min(first+batchSize, len(order))] {
			t := episodes[index]
			// Exact current-weight memory from the start of the match; detach only
			// at BPTT segment boundaries, never shuffle isolated recurrent turns.
			var memory []float32
			for start := 0; start < len(t); start += config.SequenceLength {
				g := &battlenet.Graph[float32]{Train: true}
				bound := candidate.Model.Bind(g)
				h := g.New(1, candidate.Model.Config.Width, memory)
				loss := g.New(1, 1, nil)
				var unweighted float32
				for j := start; j < min(start+config.SequenceLength, len(t)); j++ {
					s := t[j]
					batchActions += len(s.Choices)
					o, err := battlepolicy.Forward(ctx, bound, s.Frame, h, s.Choices, nil)
					if err != nil {
						return report, err
					}
					h = o.Memory
					if weights == nil {
						loss = g.Add(loss, g.Scale(o.LogProb, -1))
					} else {
						unweighted -= o.LogProb.Data[0]
						for k, choice := range s.Choices {
							key, err := imitationActionClass(s.Frame, k, choice)
							if err != nil {
								return report, err
							}
							loss = g.Add(loss, g.Scale(o.ConditionalLogProbTerms[k], -float32(weights.Weights[key])))
						}
					}
				}
				memory = append([]float32(nil), h.Data...)
				if weights == nil {
					report.CrossEntropy += float64(loss.Data[0])
				} else {
					report.CrossEntropy += float64(unweighted)
					*report.WeightedCrossEntropy += float64(loss.Data[0])
				}
				g.Backward(loss)
				for name, grad := range bound.Gradients() {
					if gradients[name] == nil {
						gradients[name] = make([]float32, len(grad))
					}
					for k, v := range grad {
						gradients[name][k] += v
					}
				}
			}
		}
		norm, err := candidate.Optimizer.Update(candidate.Model, gradients, batchActions, config.LearningRate, config.GradientClip)
		if err != nil {
			return report, err
		}
		// Aggregate pre-update metrics by action count; in minibatch mode these
		// are measured before EACH minibatch update, not on one frozen epoch.
		if config.BatchEpisodes > 0 {
			report.GradientNorm += norm * float64(batchActions)
			report.OptimizerUpdates++
		} else {
			report.GradientNorm = norm
		}
	}
	report.CrossEntropy /= float64(report.Actions)
	if report.WeightedCrossEntropy != nil {
		*report.WeightedCrossEntropy /= float64(report.Actions)
		if !finite(*report.WeightedCrossEntropy) {
			return report, fmt.Errorf("nonfinite weighted imitation loss")
		}
	}
	if !finite(report.CrossEntropy) {
		return report, fmt.Errorf("nonfinite imitation loss")
	}
	if config.BatchEpisodes > 0 {
		report.GradientNorm /= float64(report.Actions)
	}
	report.AfterPolicy, e = ModelDigest(candidate.Model)
	if e != nil {
		return report, e
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	*model, *optimizer = *candidate.Model, *candidate.Optimizer
	return report, nil
}
