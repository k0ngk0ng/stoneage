package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type PPOConfig struct {
	// Empty preserves the original pre-update-only check when resuming old runs.
	UpdateGuard    string  `json:"update_guard,omitempty"`
	Epochs         int     `json:"epochs"`
	SequenceLength int     `json:"sequence_length"`
	LearningRate   float64 `json:"learning_rate"`
	Clip           float64 `json:"clip"`
	Gamma          float64 `json:"gamma"`
	Lambda         float64 `json:"lambda"`
	ValueWeight    float64 `json:"value_weight"`
	EntropyWeight  float64 `json:"entropy_weight"`
	GradientClip   float64 `json:"gradient_clip"`
	TargetKL       float64 `json:"target_kl"`
}

func DefaultPPOConfig() PPOConfig {
	return PPOConfig{UpdateGuard: "backtrack-v1", Epochs: 3, SequenceLength: 16, LearningRate: 3e-4, Clip: .2, Gamma: 1, Lambda: .95, ValueWeight: .5, EntropyWeight: .01, GradientClip: .5, TargetKL: .03}
}
func (c PPOConfig) validate() error {
	if c.UpdateGuard != "" && c.UpdateGuard != "backtrack-v1" {
		return fmt.Errorf("unsupported PPO update guard %q", c.UpdateGuard)
	}
	for _, v := range []float64{c.LearningRate, c.Clip, c.Gamma, c.Lambda, c.ValueWeight, c.EntropyWeight, c.GradientClip, c.TargetKL} {
		if !finite(v) {
			return fmt.Errorf("nonfinite PPO setting")
		}
	}
	if c.Epochs < 1 || c.Epochs > 20 || c.SequenceLength < 1 || c.SequenceLength > 128 || c.LearningRate <= 0 || c.Clip <= 0 || c.Clip >= 1 || c.Gamma < 0 || c.Gamma > 1 || c.Lambda < 0 || c.Lambda > 1 || c.ValueWeight < 0 || c.EntropyWeight < 0 || c.GradientClip <= 0 || c.TargetKL <= 0 {
		return fmt.Errorf("invalid PPO settings")
	}
	return nil
}

// These are pre-update, per-team-turn means on the current recurrent replay.
// ConditionalEntropy sums slot entropies along recorded plan prefixes; it is
// not an exact enumeration of joint-plan entropy, nor a win probability.
type ObjectiveTerms struct {
	PolicyLoss         float64 `json:"policy_loss"`
	ValueLoss          float64 `json:"value_loss"`
	EntropyLoss        float64 `json:"entropy_loss"`
	ConditionalEntropy float64 `json:"conditional_entropy"`
}

type EpochReport struct {
	Modes        []ModeEpochReport `json:"modes,omitempty"`
	Loss         float64           `json:"loss"`
	ApproxKL     float64           `json:"approx_kl"`
	ClipFraction float64           `json:"clip_fraction"`
	GradientNorm float64           `json:"gradient_norm"`
	PostUpdateKL *float64          `json:"post_update_kl,omitempty"`
	LearningRate float64           `json:"learning_rate,omitempty"`
	Backtracks   int               `json:"backtracks,omitempty"`
	Objectives   *ObjectiveTerms   `json:"objectives,omitempty"`
}
type Report struct {
	ModeMixture       *ModeMixture       `json:"mode_mixture,omitempty"`
	Modes             []ModeBatchReport  `json:"modes,omitempty"`
	ModeGuardFailures []ModeGuardFailure `json:"mode_guard_failures,omitempty"`
	BehaviorPolicy    string             `json:"behavior_policy"`
	CandidatePolicy   string             `json:"candidate_policy"`
	Episodes          int                `json:"episodes"`
	TeamTurns         int                `json:"team_turns"`
	Epochs            []EpochReport      `json:"epochs"`
	StoppedForKL      bool               `json:"stopped_for_kl"`
	RejectedUpdates   int                `json:"rejected_updates,omitempty"`
}

// Train performs full-batch recurrent PPO, committing a candidate and Adam
// state atomically on success. Collected weights are immutable while learning;
// episodes from a different behavior digest are rejected, not reused off-policy.
// Each epoch recomputes hidden states from the START of each match using the
// current weights. Segment boundaries detach gradients, not numerical memory.
// This gives exact burn-in and bounded graphs without shuffling isolated turns.
func Train(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config PPOConfig) (Report, error) {
	return trainWithOpeningAdvantages(ctx, model, optimizer, episodes, config, nil)
}

// Non-nil openingAdvantages are produced only by openingAdvantages after
// validating the complete sampled groups. They replace policy advantages;
// recurrent replay, GAE value targets and all optimizer safeguards are shared.
func trainWithOpeningAdvantages(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config PPOConfig, openingAdvantages []float64) (Report, error) {
	return trainWithModes(ctx, model, optimizer, episodes, config, openingAdvantages, nil)
}

func trainWithModes(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config PPOConfig, openingAdvantages []float64, mixture *ModeMixture) (Report, error) {
	var report Report
	if e := config.validate(); e != nil {
		return report, e
	}
	if optimizer == nil || len(episodes) == 0 {
		return report, fmt.Errorf("optimizer and on-policy trajectories required")
	}
	if openingAdvantages != nil && len(openingAdvantages) != len(episodes) {
		return report, fmt.Errorf("incomplete opening advantages")
	}
	version, e := ModelDigest(model)
	if e != nil {
		return report, e
	}
	report.BehaviorPolicy = version
	report.Episodes = len(episodes)
	var candidate battlenet.Model[float32]
	var adam battlenet.Adam[float32]
	b, e := json.Marshal(model)
	if e != nil {
		return report, e
	}
	if e = json.Unmarshal(b, &candidate); e != nil {
		return report, e
	}
	b, e = json.Marshal(optimizer)
	if e != nil {
		return report, e
	}
	if e = json.Unmarshal(b, &adam); e != nil {
		return report, e
	}
	advantages := make([][]float64, len(episodes))
	returns := make([][]float64, len(episodes))
	sum, square := 0., 0.
	seen := map[string]bool{}
	rules, scenarioMode := "", 0
	for i, t := range episodes {
		if e = t.Validate(); e != nil {
			return report, e
		}
		if t.Policy != version || t.PolicyKind != "network-sampled" {
			return report, fmt.Errorf("off-policy trajectory: collect with the frozen current model")
		}
		key := fmt.Sprintf("%s:%d", t.Match, t.Side)
		if seen[key] {
			return report, fmt.Errorf("duplicate trajectory")
		}
		seen[key] = true
		if i == 0 {
			rules = t.Rules
			scenarioMode = t.Mode
		} else if t.Rules != rules || mixture == nil && t.Mode != scenarioMode || t.Platform != episodes[0].Platform || t.Environment != episodes[0].Environment {
			return report, fmt.Errorf("mixed rule digests or modes in initial PPO batch")
		}
		r, v := make([]float64, len(t.Steps)), make([]float64, len(t.Steps))
		for j, s := range t.Steps {
			r[j], v[j] = s.Reward, float64(s.Value)
		}
		advantages[i], returns[i], e = battlenet.Advantages(r, v, t.Bootstrap, t.Terminated, config.Gamma, config.Lambda)
		if e != nil {
			return report, e
		}
		if openingAdvantages != nil {
			if !finite(openingAdvantages[i]) {
				return report, fmt.Errorf("nonfinite opening advantage")
			}
			for j := range advantages[i] {
				advantages[i][j] = openingAdvantages[i]
			}
		}
		for _, a := range advantages[i] {
			sum += a
			square += a * a
		}
		report.TeamTurns += len(t.Steps)
	}
	var mixed *mixedPPOBatch
	if mixture != nil {
		mixed, e = prepareMixedPPO(*mixture, episodes, advantages, &report)
		if e != nil {
			return report, e
		}
	} else {
		mean := sum / float64(report.TeamTurns)
		std := math.Sqrt(max(0, square/float64(report.TeamTurns)-mean*mean))
		if std > 1e-8 {
			for i := range advantages {
				for j := range advantages[i] {
					advantages[i][j] = (advantages[i][j] - mean) / (std + 1e-8)
				}
			}
		}
	}
	for epoch := 0; epoch < config.Epochs; epoch++ {
		if e = ctx.Err(); e != nil {
			return report, e
		}
		gradients := map[string][]float32{}
		stats := EpochReport{Objectives: &ObjectiveTerms{}}
		var modeStats [6]EpochReport
		for i, t := range episodes {
			measured := &stats
			if mixed != nil {
				measured = &modeStats[t.Mode]
				if measured.Objectives == nil {
					measured.Objectives = &ObjectiveTerms{}
				}
			}
			// Current-policy burn-in, one inference pass per episode per epoch.
			memories := make([][]float32, len(t.Steps)+1)
			for j, s := range t.Steps {
				g := &battlenet.Graph[float32]{}
				o, e := battlepolicy.Forward(ctx, candidate.Bind(g), s.Frame, g.New(1, candidate.Config.Width, memories[j]), s.Choices, nil)
				if e != nil {
					return report, e
				}
				memories[j+1] = append([]float32(nil), o.Memory.Data...)
				if epoch == 0 && (math.Abs(float64(o.LogProb.Data[0]-s.LogProb)) > 1e-4 || math.Abs(float64(o.Value.Data[0]-s.Value)) > 1e-5) {
					return report, fmt.Errorf("recorded behavior probability/value does not match frozen policy at %s step %d", t.Match, j)
				}
				if epoch == 0 {
					for k, p := range o.ConditionalLogProbs {
						if math.Abs(float64(p-s.ConditionalLogProbs[k])) > 1e-5 {
							return report, fmt.Errorf("recorded conditional probability mismatch")
						}
					}
				}
			}
			if epoch == 0 && t.Truncated {
				g := &battlenet.Graph[float32]{}
				o, e := battlepolicy.Forward(ctx, candidate.Bind(g), *t.Next, g.New(1, candidate.Config.Width, memories[len(t.Steps)]), nil, nil)
				if e != nil {
					return report, e
				}
				if math.Abs(float64(o.Value.Data[0])-t.Bootstrap) > 1e-5 {
					return report, fmt.Errorf("recorded bootstrap value mismatch")
				}
			}
			for start := 0; start < len(t.Steps); start += config.SequenceLength {
				g := &battlenet.Graph[float32]{Train: true}
				bound := candidate.Bind(g)
				memory := g.New(1, candidate.Config.Width, memories[start])
				loss := g.New(1, 1, nil)
				for j := start; j < min(start+config.SequenceLength, len(t.Steps)); j++ {
					s := t.Steps[j]
					o, e := battlepolicy.Forward(ctx, bound, s.Frame, memory, s.Choices, nil)
					if e != nil {
						return report, e
					}
					memory = o.Memory
					delta := float64(o.LogProb.Data[0] - s.LogProb)
					ratio := math.Exp(delta)
					measured.ApproxKL += ratio - 1 - delta
					if math.Abs(ratio-1) > config.Clip {
						measured.ClipFraction++
					}
					policyLoss, e := battlenet.PPOLoss(g, o.LogProb, float64(s.LogProb), advantages[i][j], config.Clip)
					if e != nil {
						return report, e
					}
					valueError := g.Add(o.Value, g.New(1, 1, []float32{float32(-returns[i][j])}))
					valueLoss := g.Scale(g.Mul(valueError, valueError), float32(config.ValueWeight*.5))
					entropyLoss := g.Scale(o.Entropy, float32(-config.EntropyWeight))
					measured.Objectives.PolicyLoss += float64(policyLoss.Data[0])
					measured.Objectives.ValueLoss += float64(valueLoss.Data[0])
					measured.Objectives.EntropyLoss += float64(entropyLoss.Data[0])
					measured.Objectives.ConditionalEntropy += float64(o.Entropy.Data[0])
					loss = g.Add(loss, g.Add(policyLoss, g.Add(valueLoss, entropyLoss)))
				}
				measured.Loss += float64(loss.Data[0])
				if mixed != nil {
					loss = g.Scale(loss, mixed.gradientWeight(t.Mode))
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
		if mixed != nil {
			if e = mixed.finishEpoch(&stats, modeStats); e != nil {
				return report, e
			}
		} else {
			stats.ApproxKL /= float64(report.TeamTurns)
			stats.ClipFraction /= float64(report.TeamTurns)
			stats.Loss /= float64(report.TeamTurns)
			stats.Objectives.PolicyLoss /= float64(report.TeamTurns)
			stats.Objectives.ValueLoss /= float64(report.TeamTurns)
			stats.Objectives.EntropyLoss /= float64(report.TeamTurns)
			stats.Objectives.ConditionalEntropy /= float64(report.TeamTurns)
		}
		if !finite(stats.ApproxKL) || !finite(stats.Loss) {
			return report, fmt.Errorf("nonfinite epoch metrics")
		}
		if epochExceedsKL(stats, config.TargetKL) {
			report.StoppedForKL = true
			break
		}
		if config.UpdateGuard == "backtrack-v1" {
			accepted, rejected, err := guardedUpdateForModes(ctx, &candidate, &adam, gradients, episodes, report.TeamTurns, config, &stats, mixed, epoch, &report)
			if err != nil {
				return report, err
			}
			report.RejectedUpdates += rejected
			if !accepted {
				report.StoppedForKL = true
				break
			}
		} else {
			stats.GradientNorm, e = adam.Update(&candidate, gradients, report.TeamTurns, config.LearningRate, config.GradientClip)
			if e != nil {
				return report, e
			}
		}
		report.Epochs = append(report.Epochs, stats)
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	report.CandidatePolicy, e = ModelDigest(&candidate)
	if e != nil {
		return report, e
	}
	*model, *optimizer = candidate, adam
	return report, nil
}

// replayKL measures the joint policy change against the frozen collecting
// policy. Rebuild recurrent memory from the start with the tentative weights;
// neither saved old-policy memory nor independent-turn replay is valid here.
func replayKL(ctx context.Context, model *battlenet.Model[float32], episodes []Trajectory) (float64, error) {
	total, turns := 0., 0
	for _, t := range episodes {
		var memory []float32
		for _, s := range t.Steps {
			if e := ctx.Err(); e != nil {
				return 0, e
			}
			g := &battlenet.Graph[float32]{}
			o, e := battlepolicy.Forward(ctx, model.Bind(g), s.Frame, g.New(1, model.Config.Width, memory), s.Choices, nil)
			if e != nil {
				return 0, e
			}
			memory = o.Memory.Data
			delta := float64(o.LogProb.Data[0] - s.LogProb)
			// Expm1 avoids cancellation for the small changes near the bound.
			total += math.Expm1(delta) - delta
			turns++
		}
	}
	if turns == 0 {
		return 0, fmt.Errorf("KL replay requires team turns")
	}
	return total / float64(turns), nil
}

func guardedUpdate(ctx context.Context, model *battlenet.Model[float32], adam *battlenet.Adam[float32], gradients map[string][]float32, episodes []Trajectory, turns int, config PPOConfig, stats *EpochReport) (bool, int, error) {
	return guardedUpdateForModes(ctx, model, adam, gradients, episodes, turns, config, stats, nil, 0, nil)
}

func guardedUpdateForModes(ctx context.Context, model *battlenet.Model[float32], adam *battlenet.Adam[float32], gradients map[string][]float32, episodes []Trajectory, turns int, config PPOConfig, stats *EpochReport, mixed *mixedPPOBatch, epoch int, report *Report) (bool, int, error) {
	// Adam allocates new weights and moment slices. Every attempt starts from
	// exactly these pre-update maps; rejected attempts never advance Adam.
	before, optimizer := *model, *adam
	if mixed != nil {
		turns = 1 // Gradients already contain the declared per-mode means.
	}
	rate := config.LearningRate
	for attempt := 0; attempt <= 8; attempt++ {
		if e := ctx.Err(); e != nil {
			return false, attempt, e
		}
		trial, trialAdam := before, optimizer
		norm, e := trialAdam.Update(&trial, gradients, turns, rate, config.GradientClip)
		if e != nil {
			return false, attempt, e
		}
		var kl float64
		var modeKL []float64
		var exceeded []int
		if mixed == nil {
			kl, e = replayKL(ctx, &trial, episodes)
		} else {
			kl, modeKL, exceeded, e = mixed.replayKL(ctx, &trial, config.TargetKL)
		}
		if e != nil {
			return false, attempt, e
		}
		if finite(kl) && kl <= config.TargetKL && len(exceeded) == 0 {
			*model, *adam = trial, trialAdam
			stats.GradientNorm, stats.PostUpdateKL, stats.LearningRate, stats.Backtracks = norm, &kl, rate, attempt
			for i := range modeKL {
				stats.Modes[i].PostUpdateKL = &modeKL[i]
			}
			return true, attempt, nil
		}
		if mixed != nil {
			report.ModeGuardFailures = append(report.ModeGuardFailures, ModeGuardFailure{Epoch: epoch, LearningRate: rate, Modes: exceeded})
		}
		rate *= .5
	}
	return false, 9, nil
}
