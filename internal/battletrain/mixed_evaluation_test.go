package battletrain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func mixedCandidateFixture(t *testing.T, x MixedExperiment) battlepolicy.Artifact {
	t.Helper()
	a := experimentCandidate(t, x.Parts[0].Experiment)
	a.Schema, a.Modes = 6, nil
	a.Parent, a.Recorded = x.Parts[0].Experiment.InitialModel, x.Parts[0].Experiment.InitialRecorded
	a.TrainingGroups = sortedUnion(a.TrainingGroups, x.Parts[0].Experiment.InitialTrainingGroups)
	a.Experiment, _ = Digest(x)
	a.Mixed = &battlepolicy.MixedTraining{Schema: "commander-mixed-training-v1", Checkpoint: strings.Repeat("a", 64), Objective: x.Mixture.Schema, Batches: 1, Updates: 1}
	a.SelectionGroups, a.HeldoutGroups, a.RecordedSelectionArtifacts = x.mixedProvenance()
	for i, p := range x.Parts {
		a.Modes = append(a.Modes, p.Experiment.Mode)
		a.TrainingGroups = sortedUnion(a.TrainingGroups, []string{p.Experiment.groups("train")[0].Group})
		a.Mixed.Modes = append(a.Mixed.Modes, battlepolicy.MixedModeTraining{Mode: p.Experiment.Mode, Experiment: p.Digest, Weight: x.Mixture.Modes[i].Weight, FamiliesPerBatch: p.FamiliesPerBatch, Episodes: 8, TeamTurns: 8, Actions: 8})
	}
	if err := x.ValidateCandidate(a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMixedEvaluationProvenanceAndFinalSelection(t *testing.T) {
	ctx := context.Background()
	x := mixedExperimentFixture(t)
	a := mixedCandidateFixture(t, x)
	opponents := []Opponent{{Name: "basic", Rule: "basic"}, {Name: "same-envelope", Model: &a}}
	var reports []EvaluationReport
	for _, p := range x.Parts {
		s, err := prepareEvaluationWithMixed(ctx, a.Environment, a, opponents, EvaluationConfig{}, &p.Experiment, &x, "validation")
		if err != nil {
			t.Fatal(err)
		}
		if s.Report.MixedExperimentDigest != a.Experiment || s.Report.ExperimentDigest != p.Digest || s.Report.Config.Mode != p.Experiment.Mode || !reflect.DeepEqual(s.Report.MixedExperiment, &x) {
			t.Fatal("mixed schedule lost full envelope")
		}
		r := s.Report
		r.Evidence = strings.Repeat("b", 64)
		for oi, o := range opponents {
			for i, scenario := range s.Suite {
				r.Games = append(r.Games, EvaluationGame{Opponent: o.Name, OpponentPolicy: s.Versions[oi], Group: ScenarioGroup(scenario), Scenario: scenario, CandidateSide: i % 2, Turns: 3, Winner: -1, Truncated: true, Shard: strings.Repeat("c", 64)})
			}
		}
		r.Comparisons = Summarize(r.Games, r.Config.Seed)
		if err := ValidateEvaluation(r); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"lost-envelope", "lost-identity", "wrong-child", "old-schema"} {
			t.Run(kind+string(rune('0'+p.Experiment.Mode)), func(t *testing.T) {
				var bad EvaluationReport
				if err := json.Unmarshal(mustJSON(t, r), &bad); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "lost-envelope":
					bad.MixedExperiment = nil
				case "lost-identity":
					bad.MixedExperimentDigest = ""
				case "wrong-child":
					bad.Experiment = &x.Parts[1].Experiment
					if p.Experiment.Mode == 5 {
						bad.Experiment = &x.Parts[0].Experiment
					}
				case "old-schema":
					bad.Schema = evaluationSchema(r.Config.Pairing)
				}
				if ValidateEvaluation(bad) == nil {
					t.Fatal("unbound mixed report accepted")
				}
			})
		}
		paired, err := compareVerifiedEvaluations(ctx, r, r)
		if err != nil || paired.MixedExperiment != a.Experiment {
			t.Fatal("paired comparison lost mixed identity", err)
		}
		reports = append(reports, r)
	}
	if _, err := compareVerifiedEvaluations(ctx, reports[0], reports[1]); err == nil {
		t.Fatal("different modes pooled into one paired comparison")
	}
	part := x.Parts[0].Experiment
	if _, err := prepareEvaluation(ctx, a.Environment, a, opponents, EvaluationConfig{}, &part, "validation"); err == nil {
		t.Fatal("composite artifact accepted without envelope")
	}
	if _, err := x.evaluationPart(3); err == nil {
		t.Fatal("undeclared mode accepted")
	}
	foreign := a
	foreign.Experiment = strings.Repeat("e", 64)
	if _, err := prepareEvaluationWithMixed(ctx, a.Environment, a, []Opponent{{Name: "foreign", Model: &foreign}}, EvaluationConfig{}, &part, &x, "validation"); err == nil {
		t.Fatal("foreign opponent's held-out overlap ignored")
	}
	path := filepath.Join(t.TempDir(), "selection.json")
	for _, mode := range []int{1, 5, 1} {
		if err := FreezeMixedFinalSelection(ctx, path, x, mode, a, opponents); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := FreezeMixedFinalSelection(ctx, path, x, 1, a, opponents[:1]); err == nil {
		t.Fatal("final-test opponent selection changed")
	}
	changed := a
	changed.TrainingReport = strings.Repeat("f", 64)
	if err := FreezeMixedFinalSelection(ctx, path, x, 5, changed, opponents); err == nil {
		t.Fatal("final-test candidate changed across modes")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("final candidate overwritten", err)
	}
}

func TestMixedEvaluationRetainedEvidenceRejectsEnvelopeRewrite(t *testing.T) {
	source := os.Getenv("STONEAGE_MIXED_EVALUATION_FIXTURE")
	if source == "" {
		t.Skip("explicit retained mixed CLI evaluation required")
	}
	ctx := context.Background()
	original, err := VerifyEvaluation(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"remove-envelope", "changed-weight", "changed-child"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			copyMixedRunFixture(t, source+".data", path+".data")
			var report EvaluationReport
			if err := json.Unmarshal(mustJSON(t, original), &report); err != nil {
				t.Fatal(err)
			}
			var spec evaluationSpec
			if err := readObject(filepath.Join(path+".data", "spec.json"), &spec, mixedExperimentBytes+(32<<20)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "remove-envelope":
				spec.Mixed = nil
				spec.Schema = "commander-evaluation-evidence-v1"
				report.MixedExperiment, report.MixedExperimentDigest = nil, ""
				report.Schema = evaluationSchema(report.Config.Pairing)
			case "changed-weight":
				spec.Mixed.Mixture.Modes[0].Weight++
				report.MixedExperiment = spec.Mixed
				report.MixedExperimentDigest, _ = Digest(*spec.Mixed)
			case "changed-child":
				spec.Experiment = &spec.Mixed.Parts[1].Experiment
			}
			// Recompute hashes so this tests semantics, not a stale checksum.
			report.Evidence, _ = Digest(spec)
			if err := os.WriteFile(filepath.Join(path+".data", "spec.json"), mustJSON(t, spec), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, mustJSON(t, report), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyEvaluation(ctx, path); err == nil {
				t.Fatal("rewritten mixed evaluation accepted")
			}
		})
	}
}
