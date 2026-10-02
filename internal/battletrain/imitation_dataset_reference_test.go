package battletrain

import (
	"context"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"reflect"
	"sort"
	"strings"
)

// Frozen pre-compaction preparation oracle. It deliberately retains complete
// trajectories and the former validation/extraction order. Differential tests
// compare complete reports, model weights and Adam after multiple epochs and
// serialization. Keep this reference independent of the resident-data path.
func legacyFullTrajectoryImitate(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], episodes []Trajectory, config ImitationConfig) (ImitationReport, error) {
	var report ImitationReport
	if e := config.validate(); e != nil {
		return report, e
	}
	state := LearningState{Schema: 1, Model: model, Optimizer: optimizer}
	if e := state.Validate(); e != nil {
		return report, e
	}
	if len(episodes) == 0 {
		return report, fmt.Errorf("imitation requires teacher trajectories")
	}
	if e := ctx.Err(); e != nil {
		return report, e
	}
	version, e := ModelDigest(model)
	if e != nil {
		return report, e
	}
	report.BeforePolicy = version
	report.Episodes = len(episodes)
	seen, teachers := map[string]bool{}, map[string]bool{}
	for _, t := range episodes {
		if e = ctx.Err(); e != nil {
			return report, e
		}
		if e = t.Validate(); e != nil {
			return report, e
		}
		rule := strings.TrimPrefix(t.PolicyKind, "rule:")
		teacher := Policy{Rule: rule}
		id, err := teacher.Version()
		if err != nil || !strings.HasPrefix(t.PolicyKind, "rule:") || id != t.Policy {
			return report, fmt.Errorf("imitation requires a known frozen rule identity")
		}
		key := fmt.Sprintf("%s:%d", t.Match, t.Side)
		first := episodes[0]
		if seen[key] || t.Rules != first.Rules || t.Platform != first.Platform || t.Environment != first.Environment || t.Mode != first.Mode {
			return report, fmt.Errorf("duplicate or incompatible teacher trajectory")
		}
		seen[key], teachers[rule] = true, true
		if t.Bootstrap != 0 {
			return report, fmt.Errorf("rule trajectory has a learned bootstrap value")
		}
		for _, s := range t.Steps {
			choices, err := battlepolicy.RuleChoices(s.Frame, rule)
			if err != nil || !reflect.DeepEqual(choices, s.Choices) || s.LogProb != 0 || s.Value != 0 {
				return report, fmt.Errorf("teacher choice/probability differs from frozen rule")
			}
			for _, p := range s.ConditionalLogProbs {
				if p != 0 {
					return report, fmt.Errorf("rule behavior is not a point mass")
				}
			}
			report.Actions += len(s.Choices)
		}
		report.TeamTurns += len(t.Steps)
	}
	for rule := range teachers {
		report.Teachers = append(report.Teachers, rule)
	}
	sort.Strings(report.Teachers)
	sequences := make([][]imitationStep, 0, len(episodes))
	for _, t := range episodes {
		sequence := make([]imitationStep, 0, len(t.Steps))
		for _, s := range t.Steps {
			sequence = append(sequence, imitationStep{s.Frame, s.Choices})
		}
		sequences = append(sequences, sequence)
	}
	return imitateValidated(ctx, model, optimizer, sequences, config, report)
}
