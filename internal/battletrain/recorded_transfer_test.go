package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestRecordedParentAndNativeChildKeepSeparateSourceDomains(t *testing.T) {
	parent := recordedEvaluationFixture(t)
	meta := parent.Environment
	meta.Scenario = "controlled-battle-v8"
	c := DefaultEvaluationConfig()
	c.Mode = 2 // A new target mode does not relabel the recorded parent's coverage.
	x, err := NewExperimentFromSources(context.Background(), meta, c, [3]int{2, 1, 1}, nil, &parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.validateInitialModel(&parent); err != nil {
		t.Fatal(err)
	}
	if x.InitialRecorded == nil || len(x.InitialTrainingGroups) != 0 {
		t.Fatal("recorded roster became native scenario group")
	}
	if supports(parent, c.Mode) {
		t.Fatal("cross-mode initialization expanded parent inference coverage")
	}
	child := experimentCandidate(t, x)
	child.Schema, child.Recorded, child.Parent = 4, parent.Recorded, x.InitialModel
	if err := child.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := x.validateCandidateProvenance(child); err != nil {
		t.Fatal(err)
	}
	s, err := prepareEvaluation(context.Background(), meta, child, []Opponent{{Name: "basic", Rule: "basic"}}, c, &x, "validation")
	id, _ := Digest(child)
	if err != nil || !reflect.DeepEqual(s.Report.UnverifiedSourceArtifacts, []string{id}) {
		t.Fatal("child lost source uncertainty", err)
	}
	// Numerical report fixture: even without external evidence files, the
	// experiment itself proves recorded-source uncertainty must be declared.
	report := s.Report
	for i, scenario := range s.Suite {
		report.Games = append(report.Games, EvaluationGame{Opponent: "basic", OpponentPolicy: s.Versions[0], Group: ScenarioGroup(scenario), Scenario: scenario, CandidateSide: i % 2, Turns: 1, Winner: 0, Terminated: true})
	}
	report.Comparisons = Summarize(report.Games, report.Config.Seed)
	if err := ValidateEvaluation(report); err != nil {
		t.Fatal(err)
	}
	report.UnverifiedSourceArtifacts = nil
	if err := ValidateEvaluation(report); err == nil {
		t.Fatal("experiment uncertainty omitted from self-contained report")
	}
	for _, schema := range []int{2, 4} {
		bad := child
		bad.Schema, bad.Recorded = schema, nil
		if x.validateCandidateProvenance(bad) == nil {
			t.Fatal("child source laundering accepted", schema)
		}
		if schema == 4 && bad.Validate() == nil {
			t.Fatal("schema 4 without recorded source accepted")
		}
	}
	bad := x
	bad.InitialRecorded = nil
	if bad.Validate() == nil || bad.validateInitialModel(&parent) == nil {
		t.Fatal("experiment lost source domain")
	}
	// Another native generation must retain both synthetic exclusions and the
	// original recorded source, without changing the parent artifact.
	next, err := NewExperimentFromSources(context.Background(), meta, c, [3]int{2, 1, 1}, nil, &child)
	if err != nil || !reflect.DeepEqual(next.InitialRecorded, parent.Recorded) || !reflect.DeepEqual(next.InitialTrainingGroups, child.TrainingGroups) {
		t.Fatal("next generation lost ancestry", err)
	}
	if _, _, err := buildPolicy(Opponent{Name: "recorded", Model: &child}, meta, c.Mode); err != nil {
		t.Fatal("compatible recorded child cannot participate in build search", err)
	}
}

func TestNativeRecordedParentPPOResumeAndExport(t *testing.T) {
	raw, modelPath := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND"), os.Getenv("STONEAGE_RECORDED_MODEL")
	if raw == "" || modelPath == "" {
		t.Skip("explicit native engine and recorded model required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	parent, err := battlepolicy.LoadArtifact(modelPath)
	if err != nil || parent.Recorded == nil {
		t.Fatal("recorded parent required", err)
	}
	before, _ := json.Marshal(parent)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	engine.Close()
	c := DefaultEvaluationConfig()
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = parent.Modes[0], 20, 20, 10, 4
	x, err := NewExperimentFromSources(ctx, meta, c, [3]int{2, 1, 1}, nil, &parent)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultRunConfig()
	config.Network, config.Warmup = parent.Network.Config, nil
	config.Mode, config.Points, config.PetPoints, config.Level, config.MaxTurns = c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns
	config.BatchMatches, config.PPO.Epochs, config.PPO.SequenceLength = 2, 1, 2
	config.InitialModel, config.Experiment = x.InitialModel, ""
	config.Experiment, _ = Digest(x)
	interrupted, continuous := t.TempDir(), t.TempDir()
	stop := errors.New("stop after committed native collection")
	err = Run(ctx, RunOptions{Directory: interrupted, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "ready" {
			_, state, err := LoadCheckpoint(interrupted)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := ModelDigest(state.Model)
			if id != parent.WeightsDigest || state.Optimizer.Step != 0 {
				t.Fatal("did not start with exact parent weights and fresh PPO optimizer")
			}
		}
		if p.Event == "collected" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal("did not reach native collection", err)
	}
	if err := Run(ctx, RunOptions{Directory: interrupted, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, RunOptions{Directory: continuous, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	checkpoint, a, err := LoadCheckpoint(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := LoadCheckpoint(continuous)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("recorded-to-native resume diverged", err)
	}
	path, err := ExportCandidate(interrupted, "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := battlepolicy.LoadArtifact(path)
	if err != nil || child.Schema != 4 || child.Parent != x.InitialModel || !reflect.DeepEqual(child.Recorded, parent.Recorded) || child.Environment != meta {
		t.Fatal("export lost recorded/native source distinction", err)
	}
	if err := x.validateCandidateProvenance(child); err != nil {
		t.Fatal(err)
	}
	s, err := prepareEvaluation(ctx, meta, child, []Opponent{{Name: "basic", Rule: "basic"}}, c, &x, "validation")
	if err != nil || len(s.Report.UnverifiedSourceArtifacts) != 1 {
		t.Fatal("native child cannot be evaluated truthfully", err)
	}
	after, _ := json.Marshal(parent)
	if string(before) != string(after) {
		t.Fatal("parent was relabeled")
	}
	id, _ := Digest(a)
	t.Logf("recorded parent to native mode=%d batches=%d resumed_learning=%s exported_schema=%d source preserved; no strength claim", c.Mode, checkpoint.CompletedBatches, id, child.Schema)
}
