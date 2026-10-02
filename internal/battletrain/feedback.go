package battletrain

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const RuleFeedbackSchema = "commander-rule-feedback-v1"

// RuleFeedback annotates actual trajectory frames with unexecuted teacher
// suggestions. Never replace the source's actions, behavior identity, values,
// probabilities, rewards or subsequent observations with these labels.
type RuleFeedback struct {
	Schema     string  `json:"schema"`
	Trajectory string  `json:"trajectory_digest"`
	Teacher    string  `json:"teacher"`
	Policy     string  `json:"teacher_policy"`
	Choices    [][]int `json:"suggested_choices"`
}

// LabelRuleFeedback only supplies public frames to the teacher, never initial
// hidden allocations, later observations, terminal outcomes or behavior weights.
func LabelRuleFeedback(ctx context.Context, source Trajectory, teacher string) (RuleFeedback, error) {
	var result RuleFeedback
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := source.Validate(); err != nil {
		return result, err
	}
	policy, err := (Policy{Rule: teacher}).Version()
	if err != nil {
		return result, err
	}
	id, err := Digest(source)
	if err != nil {
		return result, err
	}
	result = RuleFeedback{Schema: RuleFeedbackSchema, Trajectory: id, Teacher: teacher, Policy: policy}
	for _, step := range source.Steps {
		if err := ctx.Err(); err != nil {
			return RuleFeedback{}, err
		}
		choices, err := battlepolicy.RuleChoices(step.Frame, teacher)
		if err != nil {
			return RuleFeedback{}, err
		}
		result.Choices = append(result.Choices, choices)
	}
	return result, nil
}

func (f RuleFeedback) Validate(ctx context.Context, source Trajectory) error {
	expected, err := LabelRuleFeedback(ctx, source, f.Teacher)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(f, expected) {
		return fmt.Errorf("teacher feedback differs from its source or public rule")
	}
	return nil
}

// FeedbackExample is an in-memory training input. Persist source trajectories,
// labels and frozen behavior-model references separately; a suggestion is not
// a submitted demonstration or an on-policy PPO transition.
type FeedbackExample struct {
	Source   Trajectory
	Targets  RuleFeedback
	Behavior Policy
}

type FeedbackReport struct {
	Imitation        ImitationReport `json:"imitation"`
	Sources          []string        `json:"source_trajectories"`
	Targets          []string        `json:"feedback_digests"`
	BehaviorPolicies []string        `json:"behavior_policies"`
	Disagreements    int             `json:"suggested_action_differences"`
}

// verifyFeedbackBehavior reconstructs actual recurrent behavior, including
// greedy point-mass or sampled probabilities and any final bootstrap. Labels
// never enter this replay. This is numerical provenance, not server attestation.
func verifyFeedbackBehavior(ctx context.Context, source Trajectory, p Policy, id string) error {
	var err error
	if id != source.Policy || p.Kind() != source.PolicyKind {
		return fmt.Errorf("feedback source behavior identity differs")
	}
	var memory []float32
	for turn, step := range source.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		var actual policyDecision
		if p.Rule != "" || p.Greedy {
			actual, err = p.decide(ctx, step.Frame, memory, nil)
		} else {
			g := &battlenet.Graph[float32]{}
			var result battlepolicy.Output
			result, err = battlepolicy.Forward(ctx, p.Model.Bind(g), step.Frame, g.New(1, p.Model.Config.Width, memory), step.Choices, nil)
			if err == nil {
				actual = policyDecision{choices: result.Choices, conditional: result.ConditionalLogProbs, logProb: result.LogProb.Data[0], value: result.Value.Data[0], memory: append([]float32(nil), result.Memory.Data...)}
			}
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual.choices, step.Choices) || len(actual.conditional) != len(step.ConditionalLogProbs) || math.Abs(float64(actual.logProb-step.LogProb)) > 1e-4 || math.Abs(float64(actual.value-step.Value)) > 1e-5 {
			return fmt.Errorf("feedback source differs from actual behavior at turn %d", turn)
		}
		for i, v := range actual.conditional {
			if math.Abs(float64(v-step.ConditionalLogProbs[i])) > 1e-4 {
				return fmt.Errorf("feedback source conditional probability differs at turn %d", turn)
			}
		}
		memory = actual.memory
	}
	if source.Truncated {
		value, err := p.value(ctx, *source.Next, memory)
		if err != nil {
			return err
		}
		if math.Abs(value-source.Bootstrap) > 1e-5 {
			return fmt.Errorf("feedback source bootstrap differs")
		}
	}
	return nil
}

// ImitateFeedback learns teacher suggestions on the source policy's actual
// state distribution. Source validation precedes the shared atomic imitation
// update; no fictitious teacher rollout or PPO behavior probability is created.
func ImitateFeedback(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], examples []FeedbackExample, config ImitationConfig) (FeedbackReport, error) {
	var report FeedbackReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := config.validate(); err != nil {
		return report, err
	}
	if err := (LearningState{Schema: 1, Model: model, Optimizer: optimizer}).Validate(); err != nil {
		return report, err
	}
	if len(examples) == 0 {
		return report, fmt.Errorf("feedback training needs complete examples")
	}
	version, err := ModelDigest(model)
	if err != nil {
		return report, err
	}
	imitation := ImitationReport{BeforePolicy: version, Episodes: len(examples)}
	seen, teachers, behaviors := map[string]bool{}, map[string]bool{}, map[string]bool{}
	versions := map[Policy]string{}
	var sequences [][]imitationStep
	first := examples[0].Source
	for _, example := range examples {
		source := example.Source
		if example.Behavior.Model == model {
			return report, fmt.Errorf("feedback behavior must be a frozen model distinct from the learning target")
		}
		if err := example.Targets.Validate(ctx, source); err != nil {
			return report, err
		}
		behaviorID, ok := versions[example.Behavior]
		if !ok {
			behaviorID, err = example.Behavior.Version()
			if err != nil {
				return report, err
			}
			versions[example.Behavior] = behaviorID
		}
		if err := verifyFeedbackBehavior(ctx, source, example.Behavior, behaviorID); err != nil {
			return report, err
		}
		key := fmt.Sprintf("%s:%d", source.Match, source.Side)
		if seen[key] || source.Rules != first.Rules || source.Platform != first.Platform || source.Environment != first.Environment || source.Mode != first.Mode || source.Steps[0].Frame.Schema != battlepolicy.NetworkFeatures(model.Config) {
			return report, fmt.Errorf("duplicate or incompatible feedback trajectory")
		}
		seen[key], teachers[example.Targets.Teacher], behaviors[source.Policy] = true, true, true
		id, err := Digest(example.Targets)
		if err != nil {
			return report, err
		}
		report.Sources = append(report.Sources, example.Targets.Trajectory)
		report.Targets = append(report.Targets, id)
		sequence := make([]imitationStep, len(source.Steps))
		for turn, step := range source.Steps {
			choices := example.Targets.Choices[turn]
			sequence[turn] = imitationStep{step.Frame, choices}
			imitation.Actions += len(choices)
			for i, choice := range choices {
				if choice != step.Choices[i] {
					report.Disagreements++
				}
			}
		}
		imitation.TeamTurns += len(sequence)
		sequences = append(sequences, sequence)
	}
	for name := range teachers {
		imitation.Teachers = append(imitation.Teachers, name)
	}
	for id := range behaviors {
		report.BehaviorPolicies = append(report.BehaviorPolicies, id)
	}
	sort.Strings(imitation.Teachers)
	sort.Strings(report.BehaviorPolicies)
	report.Imitation, err = imitateValidated(ctx, model, optimizer, sequences, config, imitation)
	return report, err
}
