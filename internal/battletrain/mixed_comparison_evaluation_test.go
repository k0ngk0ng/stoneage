package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestMixedComparisonScheduleAndReportBoundaries(t *testing.T) {
	left := mixedExperimentFixture(t)
	right := cloneMixedExperiment(t, left)
	right.Mixture.Modes[0].Weight++
	x, err := NewMixedValidationComparison(left, right)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mixedCandidateFixture(t, left), mixedCandidateFixture(t, right)
	part := left.Parts[0].Experiment
	opponents := []Opponent{{Name: "peer", Model: &b}}
	s, err := prepareEvaluationWithComparison(context.Background(), a.Environment, a, opponents, EvaluationConfig{}, &part, &left, &x, "validation")
	if err != nil {
		t.Fatal(err)
	}
	r := s.Report
	r.Evidence = strings.Repeat("b", 64)
	for i, scenario := range s.Suite {
		r.Games = append(r.Games, EvaluationGame{Opponent: "peer", OpponentPolicy: s.Versions[0], Group: ScenarioGroup(scenario), Scenario: scenario, CandidateSide: i % 2, Turns: 3, Winner: -1, Truncated: true, Shard: strings.Repeat("c", 64)})
	}
	r.Comparisons = Summarize(r.Games, r.Config.Seed)
	if err := ValidateEvaluation(r); err != nil {
		t.Fatal(err)
	}
	if r.Schema != "commander-mixed-comparison-evaluation-v1" || r.ValidationComparisonDigest == "" {
		t.Fatal("unbound comparison report")
	}
	for _, kind := range []string{"missing", "digest", "old-schema", "test", "right-identity"} {
		t.Run(kind, func(t *testing.T) {
			var bad EvaluationReport
			if err := json.Unmarshal(mustJSON(t, r), &bad); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				bad.ValidationComparison = nil
			case "digest":
				bad.ValidationComparisonDigest = strings.Repeat("a", 64)
			case "old-schema":
				bad.Schema = "commander-mixed-evaluation-v1"
			case "test":
				bad.Split = "test"
			case "right-identity":
				bad.ValidationComparison.RightDigest = strings.Repeat("e", 64)
			}
			if ValidateEvaluation(bad) == nil {
				t.Fatal("malformed declaration accepted")
			}
		})
	}
	if _, err := assessPromotionScoped(r, DefaultPromotionGate(), 1, false, 2); err == nil {
		t.Fatal("validation comparison qualified a champion")
	}
	for _, ops := range [][]Opponent{{{Name: "rule", Rule: "basic"}}, {{Name: "peer", Model: &b}, {Name: "extra", Rule: "basic"}}, {{Name: "wrong", Model: &a}}} {
		if _, err := prepareEvaluationWithComparison(context.Background(), a.Environment, a, ops, EvaluationConfig{}, &part, &left, &x, "validation"); err == nil {
			t.Fatal("comparison accepts an unbound opponent")
		}
	}
	if _, err := prepareEvaluationWithComparison(context.Background(), a.Environment, a, opponents, EvaluationConfig{}, &part, &left, &x, "test"); err == nil {
		t.Fatal("comparison released final test")
	}
	paired, err := compareVerifiedEvaluations(context.Background(), r, r)
	if err != nil || paired.ValidationComparison != r.ValidationComparisonDigest {
		t.Fatal("paired report lost declaration", err)
	}
}

func TestNativeMixedComparisonEvidence(t *testing.T) {
	raw := os.Getenv("STONEAGE_COMPARISON_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit isolated native comparison environment required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("STONEAGE_COMPARISON_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	var parts []MixedExperimentPart
	for _, mode := range []int{1, 2, 3, 4, 5} {
		c := DefaultEvaluationConfig()
		c.Mode, c.Seed, c.MaxTurns, c.PetPoints = mode, int64(7031+mode), 3, 0
		e, err := NewExperiment(ctx, meta, c, [3]int{2, 1, 1})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: e, FamiliesPerBatch: 1})
	}
	mixture := ModeMixture{Schema: "mode-weighted-ppo-v1", Modes: []ModeWeight{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 1}}}
	left, err := NewMixedExperiment(mixture, parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	right := cloneMixedExperiment(t, left)
	right.Mixture.Modes[0].Weight++
	x, err := NewMixedValidationComparison(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMixedValidationComparison(filepath.Join(root, "comparison.json"), x); err != nil {
		t.Fatal(err)
	}
	var models []battlepolicy.Artifact
	for i, e := range []MixedExperiment{left, right} {
		dir := filepath.Join(root, fmt.Sprintf("training-%d", i))
		c := mixedRunConfigFixture(t, e)
		if err := RunMixed(ctx, MixedRunOptions{Directory: dir, Command: command, Experiment: &e, Config: &c, Batches: 1, Workers: 1}); err != nil {
			t.Fatal(err)
		}
		path, err := ExportMixedCandidate(ctx, dir, "", "")
		if err != nil {
			t.Fatal(err)
		}
		a, err := battlepolicy.LoadArtifact(path)
		if err != nil {
			t.Fatal(err)
		}
		models = append(models, a)
		if err := writeObject(filepath.Join(root, fmt.Sprintf("candidate-%d.json", i)), a); err != nil {
			t.Fatal(err)
		}
	}
	engine, err = battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mode := range []int{1, 2, 3, 4, 5} {
		path := filepath.Join(root, fmt.Sprintf("mode-%d.json", mode))
		var firstShard string
		{
			stop := errors.New("intentional comparison interruption")
			partial, err := EvaluateMixedComparisonRecorded(ctx, engine, models[0], models[1], x, mode, path, func(EvaluationGame) error { return stop })
			if !errors.Is(err, stop) {
				t.Fatal("interruption lost", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("partial report published")
			}
			if len(partial.Games) != 1 {
				t.Fatal("wrong interrupted prefix")
			}
			firstShard = partial.Games[0].Shard
			// The evidence specification must reject another valid model from
			// the same experiment even when its numerical weights are identical.
			changed := models[1]
			changed.TrainingReport = strings.Repeat("d", 64)
			if _, err := EvaluateMixedComparisonRecorded(ctx, engine, models[0], changed, x, mode, path, nil, EvaluationRecordingOptions{Resume: true}); err == nil {
				t.Fatal("interrupted comparison replaced frozen peer")
			}
		}
		executed := 0
		r, err := EvaluateMixedComparisonRecorded(ctx, engine, models[0], models[1], x, mode, path, func(EvaluationGame) error { executed++; return nil }, EvaluationRecordingOptions{Resume: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := VerifyEvaluation(ctx, path)
		if err != nil || !reflect.DeepEqual(r, got) {
			t.Fatal("native comparison evidence did not verify", err)
		}
		if executed != 7 || r.Games[0].Shard != firstShard {
			t.Fatal("mixed comparison replayed committed game", mode, executed)
		}
		if len(r.Games) != 8 {
			t.Fatal("wrong shared validation schedule")
		}
		for _, kind := range []string{"remove-comparison", "right-digest", "peer-artifact", "test"} {
			badPath := filepath.Join(root, fmt.Sprintf("tamper-%d-%s.json", mode, kind))
			copyMixedRunFixture(t, path+".data", badPath+".data")
			var spec evaluationSpec
			if err := readObject(filepath.Join(badPath+".data", "spec.json"), &spec, mixedValidationComparisonBytes+mixedExperimentBytes+(32<<20)); err != nil {
				t.Fatal(err)
			}
			bad := r
			switch kind {
			case "remove-comparison":
				spec.Comparison = nil
				spec.Schema = "commander-mixed-evaluation-evidence-v1"
				bad.ValidationComparison = nil
				bad.ValidationComparisonDigest = ""
				bad.Schema = "commander-mixed-evaluation-v1"
			case "right-digest":
				spec.Comparison.RightDigest = strings.Repeat("e", 64)
				bad.ValidationComparison = spec.Comparison
				bad.ValidationComparisonDigest, _ = Digest(*spec.Comparison)
			case "peer-artifact":
				spec.Opponents[0].Artifact = spec.Candidate
			case "test":
				spec.Split = "test"
				bad.Split = "test"
			}
			bad.Evidence, _ = Digest(spec)
			if err := os.WriteFile(filepath.Join(badPath+".data", "spec.json"), mustJSON(t, spec), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeObject(badPath, bad); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyEvaluation(ctx, badPath); err == nil {
				t.Fatal("rehashed malformed native evidence accepted", kind)
			}
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}
