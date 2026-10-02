package battletrain

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const SqrtActionFrequency = "sqrt-action-frequency-v1"

// Counts cover the entire frozen supervised dataset, including forced waits.
// Neither outcomes nor validation observations determine these weights.
type ImitationActionWeights struct {
	Schema  string             `json:"schema"`
	Counts  map[string]int     `json:"counts"`
	Weights map[string]float64 `json:"weights"`
}

var imitationKinds = []string{"attack", "guard", "wait", "guard_break", "poison", "stone", "confuse", "sleep", "guardian", "single_heal", "group_heal", "healing_item", "switch", "recall"}

func validImitationClass(key string) bool {
	actor, kind, ok := strings.Cut(key, ":")
	if !ok || actor != "player" && actor != "pet" {
		return false
	}
	for _, allowed := range imitationKinds {
		if kind == allowed {
			return true
		}
	}
	return false
}

// Feature contracts v6..v8 assign these semantic flags. Status/guardian attacks
// also carry the ordinary attack flag; count them by the more specific class.
func imitationActionClass(f battlepolicy.Frame, slot, choice int) (string, error) {
	if !battlepolicy.SupportedFeatures(f.Schema) || slot < 0 || slot >= len(f.Slots) {
		return "", fmt.Errorf("invalid weighted imitation frame/slot")
	}
	s := f.Slots[slot]
	if s.Actor != "player" && s.Actor != "pet" || choice < 0 || choice >= len(s.Candidates) || !s.Candidates[choice].Supported {
		return "", fmt.Errorf("invalid weighted imitation action")
	}
	x := s.Candidates[choice].Features
	var kinds []string
	add := func(on bool, kind string) {
		if on {
			kinds = append(kinds, kind)
		}
	}
	add(x[30] == 1, "switch")
	add(x[31] == 1, "recall")
	if x[22] == 1 {
		switch {
		case x[29] == 1 && x[25] != 1:
			kinds = append(kinds, "healing_item")
		case x[29] != 1 && x[25] == 1:
			kinds = append(kinds, "group_heal")
		case x[29] != 1 && x[25] != 1:
			kinds = append(kinds, "single_heal")
		default:
			return "", fmt.Errorf("ambiguous healing action class")
		}
	}
	add(x[32] == 1 && f.Schema == battlepolicy.FeatureVersion, "guardian")
	add(x[16] == 1, "poison")
	add(x[17] == 1, "stone")
	add(x[18] == 1, "confuse")
	add(x[19] == 1, "sleep")
	add(x[3] == 1, "guard_break")
	add(x[1] == 1, "guard")
	add(x[2] == 1, "wait")
	if len(kinds) == 0 && x[0] == 1 {
		kinds = append(kinds, "attack")
	}
	if len(kinds) != 1 {
		return "", fmt.Errorf("unknown or ambiguous weighted imitation action class")
	}
	if x[32] != 0 && f.Schema != battlepolicy.FeatureVersion {
		return "", fmt.Errorf("guardian flag outside declared feature contract")
	}
	if x[0] == 1 {
		switch kinds[0] {
		case "attack", "guard_break", "poison", "stone", "confuse", "sleep", "guardian":
		default:
			return "", fmt.Errorf("attack flag conflicts with imitation action class")
		}
	}
	return s.Actor + ":" + kinds[0], nil
}

func addImitationCounts(counts map[string]int, f battlepolicy.Frame, choices []int) error {
	if len(choices) != len(f.Slots) {
		return fmt.Errorf("incomplete weighted imitation plan")
	}
	for slot, choice := range choices {
		key, err := imitationActionClass(f, slot, choice)
		if err != nil {
			return err
		}
		counts[key]++
	}
	return nil
}

func weightsFromCounts(counts map[string]int, actions int) (*ImitationActionWeights, error) {
	if actions < 1 || len(counts) == 0 || len(counts) > 2*len(imitationKinds) {
		return nil, fmt.Errorf("invalid imitation class counts")
	}
	keys, total := make([]string, 0, len(counts)), 0
	for key, count := range counts {
		if !validImitationClass(key) || count < 1 || count > actions-total {
			return nil, fmt.Errorf("invalid imitation class frequency")
		}
		total += count
		keys = append(keys, key)
	}
	if total != actions {
		return nil, fmt.Errorf("imitation class counts differ from actions")
	}
	sort.Strings(keys) // Stable normalization, independent of map iteration/order.
	r := &ImitationActionWeights{Schema: SqrtActionFrequency, Counts: map[string]int{}, Weights: map[string]float64{}}
	z := 0.
	for _, key := range keys {
		r.Counts[key] = counts[key]
		r.Weights[key] = math.Sqrt(float64(actions) / float64(counts[key]))
		z += float64(counts[key]) * r.Weights[key] / float64(actions)
	}
	for _, key := range keys {
		r.Weights[key] /= z
	}
	return r, nil
}

func imitationActionWeights(ctx context.Context, episodes [][]imitationStep, config ImitationConfig, actions int) (*ImitationActionWeights, error) {
	if config.ActionWeighting == "" {
		return nil, nil
	}
	if config.ActionWeighting != SqrtActionFrequency {
		return nil, fmt.Errorf("unsupported imitation weighting")
	}
	counts := map[string]int{}
	for _, episode := range episodes {
		for _, step := range episode {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := addImitationCounts(counts, step.Frame, step.Choices); err != nil {
				return nil, err
			}
		}
	}
	return weightsFromCounts(counts, actions)
}

// Nil expected checks structure/config. A nonnil expected map additionally
// binds report weights to independently reread demonstrated actions.
func validateImitationWeights(config ImitationConfig, report ImitationReport, expected map[string]int) error {
	if config.ActionWeighting == "" {
		if report.ActionWeighting != nil || report.WeightedCrossEntropy != nil {
			return fmt.Errorf("unexpected weighted imitation report")
		}
		return nil
	}
	if config.ActionWeighting != SqrtActionFrequency || report.ActionWeighting == nil || report.WeightedCrossEntropy == nil || !finite(*report.WeightedCrossEntropy) || *report.WeightedCrossEntropy < 0 {
		return fmt.Errorf("incomplete weighted imitation report")
	}
	counts := report.ActionWeighting.Counts
	if expected != nil {
		counts = expected
	}
	want, err := weightsFromCounts(counts, report.Actions)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, report.ActionWeighting) {
		return fmt.Errorf("imitation weights differ from frozen action counts")
	}
	return nil
}

func validateStoredWarmupWeights(ctx context.Context, root string, c Checkpoint, x *Experiment) error {
	counts := map[string]int{}
	for game, id := range c.Warmup.Shards {
		if err := ctx.Err(); err != nil {
			return err
		}
		pair, _, err := LoadShard(filepath.Join(root, "shards"), id)
		if err != nil {
			return err
		}
		scenario, group, policies := warmupScenario(c.Config, uint64(game), x)
		scenarioID, _ := Digest(scenario)
		if len(pair) != 2 {
			return fmt.Errorf("weighted warmup requires both recorded perspectives")
		}
		for side, tr := range pair {
			policy, _ := policies[side].Version()
			if tr.Rules != c.Environment.Rules || tr.Platform != c.Environment.Platform || tr.Environment != c.Environment.Scenario || tr.Side != side || tr.Scenario != scenarioID || tr.Group != group || tr.Policy != policy || tr.PolicyKind != policies[side].Kind() {
				return fmt.Errorf("weighted warmup differs from frozen collection schedule")
			}
			for _, step := range tr.Steps {
				if err := ctx.Err(); err != nil {
					return err
				}
				choices, err := battlepolicy.RuleChoices(step.Frame, policies[side].Rule)
				if err != nil || !reflect.DeepEqual(choices, step.Choices) {
					return fmt.Errorf("weighted warmup teacher choices differ")
				}
				if err := addImitationCounts(counts, step.Frame, step.Choices); err != nil {
					return err
				}
			}
		}
	}
	return validateImitationWeights(c.Config.Warmup.Update, *c.Warmup.LastReport, counts)
}
