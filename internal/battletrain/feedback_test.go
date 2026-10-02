package battletrain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func copyFeedbackSource(t *testing.T, source Trajectory) Trajectory {
	t.Helper()
	b, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var out Trajectory
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func feedbackFixture(t *testing.T) FeedbackExample {
	t.Helper()
	behavior := testModel(t)
	source := testTrajectory(t, behavior, 4)
	for _, teacher := range []string{"basic", "defensive", "guard-break"} {
		before, _ := Digest(source)
		labels, err := LabelRuleFeedback(context.Background(), source, teacher)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := Digest(source)
		if before != after {
			t.Fatal("label generation mutated actual trajectory")
		}
		for i, step := range source.Steps {
			if !reflect.DeepEqual(step.Choices, labels.Choices[i]) {
				return FeedbackExample{source, labels, Policy{Model: behavior}}
			}
		}
	}
	t.Fatal("fixture needs distinct real and suggested actions")
	return FeedbackExample{}
}

func TestFeedbackPreservesFactsAndLearnsSuggestions(t *testing.T) {
	example := feedbackFixture(t)
	before, _ := Digest(example.Source)
	beforeBehavior, _ := ModelDigest(example.Behavior.Model)
	model, opt := testModel(t), &battlenet.Adam[float32]{}
	c := DefaultWarmupConfig().Update
	c.SequenceLength = 2
	loss := func(m *battlenet.Model[float32]) float64 {
		x := copyFeedbackSource(t, example.Source)
		for i := range x.Steps {
			x.Steps[i].Choices = example.Targets.Choices[i]
		}
		// Loss-only numerical helper; the altered copy is never stored or
		// represented as executed evidence or passed to a trajectory trainer.
		return teacherLoss(t, m, x)
	}
	initial := loss(model)
	var report FeedbackReport
	for epoch := 0; epoch < 4; epoch++ {
		var err error
		report, err = ImitateFeedback(context.Background(), model, opt, []FeedbackExample{example}, c)
		if err != nil {
			t.Fatal(err)
		}
	}
	if loss(model) >= initial || report.Disagreements == 0 || report.Imitation.TeamTurns != 4 || report.Imitation.OptimizerUpdates != 1 || len(report.Sources) != 1 || report.Sources[0] != before {
		t.Fatal("feedback did not learn the separate targets", report)
	}
	after, _ := Digest(example.Source)
	afterBehavior, _ := ModelDigest(example.Behavior.Model)
	if before != after || beforeBehavior != afterBehavior {
		t.Fatal("feedback rewrote actual data or behavior weights")
	}
	// An outcome change may change evidence identity but never the teacher's
	// current-state labels or imitation numerical update.
	flipped := example
	flipped.Source = copyFeedbackSource(t, example.Source)
	flipped.Source.Winner = 1
	flipped.Source.Steps[3].Reward = -1
	var err error
	flipped.Targets, err = LabelRuleFeedback(context.Background(), flipped.Source, example.Targets.Teacher)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flipped.Targets.Choices, example.Targets.Choices) || flipped.Targets.Trajectory == example.Targets.Trajectory {
		t.Fatal("outcome leaked into targets or identity omitted outcome")
	}
	a, b := testModel(t), testModel(t)
	ao, bo := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	if _, err = ImitateFeedback(context.Background(), a, ao, []FeedbackExample{example}, c); err != nil {
		t.Fatal(err)
	}
	if _, err = ImitateFeedback(context.Background(), b, bo, []FeedbackExample{flipped}, c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ao, bo) {
		t.Fatal("outcome changed supervised update")
	}
}

func TestFeedbackRejectsUnboundLabelsAndBehavior(t *testing.T) {
	for _, kind := range []string{"schema", "source", "teacher", "labels", "missing-turn", "behavior", "value", "probability", "duplicate", "alias"} {
		t.Run(kind, func(t *testing.T) {
			x := feedbackFixture(t)
			model, opt := testModel(t), &battlenet.Adam[float32]{}
			switch kind {
			case "schema":
				x.Targets.Schema = "unknown"
			case "source":
				x.Targets.Trajectory = "wrong"
			case "teacher":
				x.Targets.Policy = "wrong"
			case "labels":
				x.Targets.Choices[0][0] = -1
			case "missing-turn":
				x.Targets.Choices = x.Targets.Choices[:3]
			case "behavior":
				x.Behavior.Greedy = true
			case "value":
				x.Source.Steps[1].Value += .01
			case "probability":
				x.Source.Steps[1].LogProb -= .01
				x.Source.Steps[1].ConditionalLogProbs[0] -= .01
			case "alias":
				model = x.Behavior.Model
			}
			if kind == "value" || kind == "probability" {
				var err error
				x.Targets, err = LabelRuleFeedback(context.Background(), x.Source, x.Targets.Teacher)
				if err != nil {
					t.Fatal("fixture must pass structural validation", err)
				}
			}
			examples := []FeedbackExample{x}
			if kind == "duplicate" {
				examples = append(examples, x)
			}
			before, _ := Digest(LearningState{1, model, opt})
			if _, err := ImitateFeedback(context.Background(), model, opt, examples, DefaultWarmupConfig().Update); err == nil {
				t.Fatal("accepted invalid", kind)
			}
			after, _ := Digest(LearningState{1, model, opt})
			if before != after {
				t.Fatal("failed feedback mutated learning state")
			}
		})
	}
	x := feedbackFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LabelRuleFeedback(ctx, x.Source, x.Targets.Teacher); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ImitateFeedback(ctx, testModel(t), &battlenet.Adam[float32]{}, []FeedbackExample{x}, DefaultWarmupConfig().Update); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFeedbackResumeAndLateCancellation(t *testing.T) {
	base := feedbackFixture(t)
	var examples []FeedbackExample
	for i := 0; i < 6; i++ {
		raw, err := json.Marshal(base.Source)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.ReplaceAll(raw, []byte(`"test"`), []byte(fmt.Sprintf(`"feedback-%d"`, i)))
		var source Trajectory
		if err := json.Unmarshal(raw, &source); err != nil {
			t.Fatal(err)
		}
		labels, err := LabelRuleFeedback(context.Background(), source, base.Targets.Teacher)
		if err != nil {
			t.Fatal(err)
		}
		examples = append(examples, FeedbackExample{source, labels, base.Behavior})
	}
	c := DefaultWarmupConfig().Update
	c.BatchEpisodes, c.SequenceLength = 1, 2
	full, resumed := testModel(t), testModel(t)
	a, b := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	for epoch := 0; epoch < 2; epoch++ {
		if _, err := ImitateFeedback(context.Background(), full, a, examples, c); err != nil {
			t.Fatal(err)
		}
		if _, err := ImitateFeedback(context.Background(), resumed, b, examples, c); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(LearningState{1, resumed, b})
		if err != nil {
			t.Fatal(err)
		}
		var state LearningState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		resumed, b = state.Model, state.Optimizer
	}
	if !reflect.DeepEqual(full, resumed) || !reflect.DeepEqual(a, b) {
		t.Fatal("restored feedback learning state diverged")
	}
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, err := ImitateFeedback(probe, testModel(t), &battlenet.Adam[float32]{}, examples, c); err != nil {
		t.Fatal(err)
	}
	ctx := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls * 3 / 4}
	model, optimizer := testModel(t), &battlenet.Adam[float32]{}
	before, _ := Digest(LearningState{1, model, optimizer})
	report, err := ImitateFeedback(ctx, model, optimizer, examples, c)
	if !errors.Is(err, context.Canceled) || report.Imitation.OptimizerUpdates < 1 || report.Imitation.OptimizerUpdates >= len(examples) {
		t.Fatal("did not exercise cancellation after internal minibatches", report, err)
	}
	after, _ := Digest(LearningState{1, model, optimizer})
	if before != after {
		t.Fatal("partial feedback epoch escaped after cancellation")
	}
}

func TestNativeFeedbackActualActionsRemainSeparate(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
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
	behavior := testModel(t)
	c := DefaultRunConfig()
	c.Mode, c.MaxTurns, c.HealingMagic, c.HealingItems, c.ReservePets = 2, 4, 20, 2, 2
	ec := DefaultEvaluationConfig()
	ec.Mode, ec.MaxTurns, ec.HealingMagic, ec.HealingItems, ec.ReservePets = c.Mode, c.MaxTurns, c.HealingMagic, c.HealingItems, c.ReservePets
	x, err := NewExperiment(ctx, engine.Metadata(), ec, [3]int{1, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	var examples []FeedbackExample
	for game, greedy := range []bool{false, true} {
		scenario, group := x.trainingScenario(c.Seed, uint64(game))
		policies := [2]Policy{{Model: behavior, Greedy: greedy}, {Rule: "sustain"}}
		tr, err := CollectPolicies(ctx, engine, scenario, policies, [2]*rand.Rand{rand.New(rand.NewSource(83)), nil}, group, engine.Metadata().Rules)
		if err != nil {
			t.Fatal(err)
		}
		for side := range tr {
			labels, err := LabelRuleFeedback(ctx, tr[side], "control")
			if err != nil {
				t.Fatal(err)
			}
			examples = append(examples, FeedbackExample{tr[side], labels, policies[side]})
		}
	}
	artifacts := map[string]battlepolicy.Artifact{}
	for _, example := range examples {
		if example.Behavior.Model != nil {
			artifacts[example.Source.Policy] = experimentCandidate(t, x)
		}
	}
	root := t.TempDir()
	dataset, err := SaveFeedbackDataset(ctx, root, x, examples, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	_, _, reloaded, err := LoadFeedbackDataset(ctx, root, dataset)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Digest(examples)
	model, opt := testModel(t), &battlenet.Adam[float32]{}
	cfg := DefaultWarmupConfig().Update
	cfg.BatchEpisodes, cfg.SequenceLength = 2, 2
	report, err := ImitateFeedback(ctx, model, opt, examples, cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := Digest(examples)
	if before != after || report.Disagreements == 0 || report.Imitation.OptimizerUpdates != 2 || len(report.BehaviorPolicies) != 3 {
		t.Fatal("real behavior facts changed or feedback unused", report)
	}
	reloadedModel, reloadedOpt := testModel(t), &battlenet.Adam[float32]{}
	reloadedReport, err := ImitateFeedback(ctx, reloadedModel, reloadedOpt, reloaded, cfg)
	if err != nil || !reflect.DeepEqual(report, reloadedReport) || !reflect.DeepEqual(model, reloadedModel) || !reflect.DeepEqual(opt, reloadedOpt) {
		t.Fatal("persisted native feedback changed model or optimizer update", err)
	}
	initial := experimentCandidate(t, x)
	for i, name := range []string{"full", "resumed"} {
		if err := RunFeedback(ctx, FeedbackRunOptions{Directory: filepath.Join(root, name), Datasets: []FeedbackDatasetInput{{root, dataset}}, Initial: &initial, Update: &cfg, Epochs: 2 - i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: filepath.Join(root, "resumed"), Resume: true, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	fullCheckpoint, fullState, _, err := LoadFeedbackCheckpoint(ctx, filepath.Join(root, "full"))
	if err != nil {
		t.Fatal(err)
	}
	resumedCheckpoint, resumedState, _, err := LoadFeedbackCheckpoint(ctx, filepath.Join(root, "resumed"))
	if err != nil || !reflect.DeepEqual(fullCheckpoint, resumedCheckpoint) || !reflect.DeepEqual(fullState, resumedState) {
		t.Fatal("native feedback run resume differs from continuous training", err)
	}
	exportedPath, err := ExportFeedbackCandidate(ctx, filepath.Join(root, "resumed"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := battlepolicy.LoadArtifact(exportedPath)
	if err != nil {
		t.Fatal(err)
	}
	evaluationPath := filepath.Join(root, "feedback-evaluation.json")
	evaluation, err := EvaluateRecorded(ctx, engine, exported, []Opponent{{Name: "basic", Rule: "basic"}}, ec, &x, "validation", evaluationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyEvaluation(ctx, evaluationPath)
	if err != nil || len(verified.Games) != 8 || !reflect.DeepEqual(evaluation, verified) {
		t.Fatal("feedback candidate could not complete raw-verified native evaluation", err)
	}
	// Greedy samples retain their point-mass identity and remain ineligible
	// for on-policy PPO even after being annotated for supervised feedback.
	if _, err := Train(ctx, behavior, &battlenet.Adam[float32]{}, []Trajectory{examples[2].Source}, DefaultPPOConfig()); err == nil {
		t.Fatal("greedy feedback source entered PPO")
	}
	// A separate high-health, one-turn match guarantees a real nonterminal
	// cutoff rather than letting a skipped branch pretend to test bootstrap.
	c.MaxTurns = 1
	scenario, _ := scenarioFor(c, 17)
	for i := range scenario.Builds {
		scenario.Builds[i] = battleenv.Build{117, 1, 1, 1}
	}
	for i := range scenario.PetBuilds {
		scenario.PetBuilds[i] = battleenv.Build{117, 1, 1, 1}
	}
	cutPolicies := [2]Policy{{Model: behavior, Greedy: true}, {Rule: "sustain"}}
	cut, err := CollectPolicies(ctx, engine, scenario, cutPolicies, [2]*rand.Rand{}, ScenarioGroup(scenario), engine.Metadata().Rules)
	if err != nil {
		t.Fatal(err)
	}
	cutTargets, err := LabelRuleFeedback(ctx, cut[0], "control")
	if err != nil {
		t.Fatal(err)
	}
	cutExample := FeedbackExample{cut[0], cutTargets, cutPolicies[0]}
	if _, err := ImitateFeedback(ctx, testModel(t), &battlenet.Adam[float32]{}, []FeedbackExample{cutExample}, cfg); err != nil {
		t.Fatal("valid cutoff feedback failed", err)
	}
	if !cutExample.Source.Truncated || cutExample.Behavior.Model == nil {
		t.Fatal("fixture did not exercise a learned truncation bootstrap")
	}
	bad := cutExample
	bad.Source = copyFeedbackSource(t, bad.Source)
	bad.Source.Bootstrap += .01
	bad.Targets, err = LabelRuleFeedback(ctx, bad.Source, "control")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImitateFeedback(ctx, testModel(t), &battlenet.Adam[float32]{}, []FeedbackExample{bad}, cfg); err == nil {
		t.Fatal("incorrect behavior bootstrap accepted")
	}
	t.Logf("two native training matches plus a forced-cutoff match; sampled/greedy/rule behavior replay, persistence, exact model+Adam parity and runner resume; exported candidate passed eight raw-verified validation games; %d differing suggestions in training pair; short-cutoff functional fixtures, not strength", report.Disagreements)
}
