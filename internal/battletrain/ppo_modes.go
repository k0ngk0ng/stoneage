package battletrain

import (
	"context"
	"fmt"
	"math"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// ModeMixture is an explicit opt-in to a different learning objective. The
// weights average mode-specific team-turn means, not pooled turns or actors.
// This numerical contract does not itself qualify a model for any mode.
type ModeMixture struct {
	Schema string       `json:"schema"`
	Modes  []ModeWeight `json:"modes"`
}

type ModeWeight struct {
	Mode   int `json:"mode"`
	Weight int `json:"weight"`
}

func (m ModeMixture) Validate() error {
	if m.Schema != "mode-weighted-ppo-v1" || len(m.Modes) < 2 || len(m.Modes) > 5 {
		return fmt.Errorf("mixed PPO requires mode-weighted-ppo-v1 and 2..5 declared modes")
	}
	last := 0
	for _, entry := range m.Modes {
		if entry.Mode <= last || entry.Mode > 5 || entry.Weight < 1 || entry.Weight > 1000000 {
			return fmt.Errorf("mixed PPO modes must be sorted, unique, in 1..5, with weights in 1..1000000")
		}
		last = entry.Mode
	}
	return nil
}

type ModeBatchReport struct {
	Mode      int `json:"mode"`
	Episodes  int `json:"episodes"`
	TeamTurns int `json:"team_turns"`
	Actions   int `json:"actions"`
}

type ModeEpochReport struct {
	Mode         int            `json:"mode"`
	Loss         float64        `json:"loss"`
	ApproxKL     float64        `json:"approx_kl"`
	ClipFraction float64        `json:"clip_fraction"`
	Objectives   ObjectiveTerms `json:"objectives"`
	PostUpdateKL *float64       `json:"post_update_kl,omitempty"`
}

type ModeGuardFailure struct {
	Epoch        int     `json:"epoch"` // Zero-based epoch and exact rejected trial rate.
	LearningRate float64 `json:"learning_rate"`
	Modes        []int   `json:"modes"` // Excessive or nonfinite per-mode empirical joint KL.
}

// TrainMixed uses one frozen behavior policy for every mode and commits one
// shared model/Adam pair atomically. Collection, experiment provenance, export
// and arena qualification are separate responsibilities. Historical trajectories
// do not become on-policy through this entry point.
func TrainMixed(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config PPOConfig, mixture ModeMixture) (Report, error) {
	if err := mixture.Validate(); err != nil {
		return Report{}, err
	}
	if config.UpdateGuard != "backtrack-v1" {
		return Report{}, fmt.Errorf("mixed PPO requires per-mode backtracking; set update_guard=backtrack-v1")
	}
	// Freeze the caller's declaration; report ownership is independent of it.
	mixture.Modes = append([]ModeWeight(nil), mixture.Modes...)
	return trainWithModes(ctx, model, optimizer, episodes, config, nil, &mixture)
}

type mixedPPOBatch struct {
	mixture ModeMixture
	counts  [6]ModeBatchReport
	weights [6]float64
	byMode  [6][]Trajectory
}

// Called after all trajectories have passed their full observation validation.
func prepareMixedPPO(mixture ModeMixture, episodes []Trajectory, advantages [][]float64, report *Report) (*mixedPPOBatch, error) {
	m := &mixedPPOBatch{mixture: mixture}
	totalWeight := 0
	for _, entry := range mixture.Modes {
		totalWeight += entry.Weight
		m.counts[entry.Mode].Mode = entry.Mode
	}
	for _, entry := range mixture.Modes {
		m.weights[entry.Mode] = float64(entry.Weight) / float64(totalWeight)
	}
	var sums, squares [6]float64
	for i, tr := range episodes {
		if m.weights[tr.Mode] == 0 {
			return nil, fmt.Errorf("PPO batch contains undeclared mode %d", tr.Mode)
		}
		count := &m.counts[tr.Mode]
		count.Episodes++
		count.TeamTurns += len(tr.Steps)
		m.byMode[tr.Mode] = append(m.byMode[tr.Mode], tr)
		for j, step := range tr.Steps {
			count.Actions += len(step.Choices)
			a := advantages[i][j]
			sums[tr.Mode] += a
			squares[tr.Mode] += a * a
		}
	}
	for _, entry := range mixture.Modes {
		if m.counts[entry.Mode].TeamTurns == 0 {
			return nil, fmt.Errorf("PPO batch is missing declared mode %d", entry.Mode)
		}
	}
	for i, tr := range episodes {
		n := float64(m.counts[tr.Mode].TeamTurns)
		mean := sums[tr.Mode] / n
		std := math.Sqrt(max(0, squares[tr.Mode]/n-mean*mean))
		if std > 1e-8 {
			for j := range advantages[i] {
				advantages[i][j] = (advantages[i][j] - mean) / (std + 1e-8)
			}
		}
	}
	report.ModeMixture = &m.mixture
	for _, entry := range mixture.Modes {
		report.Modes = append(report.Modes, m.counts[entry.Mode])
	}
	return m, nil
}

func (m *mixedPPOBatch) gradientWeight(mode int) float32 {
	return float32(m.weights[mode] / float64(m.counts[mode].TeamTurns))
}

func (m *mixedPPOBatch) finishEpoch(stats *EpochReport, modes [6]EpochReport) error {
	for _, entry := range m.mixture.Modes {
		mode, s := entry.Mode, modes[entry.Mode]
		n, w := float64(m.counts[mode].TeamTurns), m.weights[mode]
		r := ModeEpochReport{Mode: mode, Loss: s.Loss / n, ApproxKL: s.ApproxKL / n, ClipFraction: s.ClipFraction / n,
			Objectives: ObjectiveTerms{PolicyLoss: s.Objectives.PolicyLoss / n, ValueLoss: s.Objectives.ValueLoss / n,
				EntropyLoss: s.Objectives.EntropyLoss / n, ConditionalEntropy: s.Objectives.ConditionalEntropy / n}}
		for _, value := range []float64{r.Loss, r.ApproxKL, r.ClipFraction, r.Objectives.PolicyLoss, r.Objectives.ValueLoss, r.Objectives.EntropyLoss, r.Objectives.ConditionalEntropy} {
			if !finite(value) {
				return fmt.Errorf("nonfinite epoch metrics for mode %d", mode)
			}
		}
		stats.Modes = append(stats.Modes, r)
		stats.Loss += w * r.Loss
		stats.ApproxKL += w * r.ApproxKL
		stats.ClipFraction += w * r.ClipFraction
		stats.Objectives.PolicyLoss += w * r.Objectives.PolicyLoss
		stats.Objectives.ValueLoss += w * r.Objectives.ValueLoss
		stats.Objectives.EntropyLoss += w * r.Objectives.EntropyLoss
		stats.Objectives.ConditionalEntropy += w * r.Objectives.ConditionalEntropy
	}
	return nil
}

func epochExceedsKL(stats EpochReport, limit float64) bool {
	if stats.ApproxKL > limit {
		return true
	}
	for _, m := range stats.Modes {
		if m.ApproxKL > limit {
			return true
		}
	}
	return false
}

func (m *mixedPPOBatch) replayKL(ctx context.Context, model *battlenet.Model[float32], limit float64) (float64, []float64, []int, error) {
	total := 0.
	values := make([]float64, len(m.mixture.Modes))
	var exceeded []int
	for i, entry := range m.mixture.Modes {
		kl, err := replayKL(ctx, model, m.byMode[entry.Mode])
		if err != nil {
			return 0, nil, nil, err
		}
		values[i] = kl
		total += m.weights[entry.Mode] * kl
		if !finite(kl) || kl > limit {
			exceeded = append(exceeded, entry.Mode)
		}
	}
	return total, values, exceeded, nil
}
