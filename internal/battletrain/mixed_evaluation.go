package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func (x MixedExperiment) evaluationPart(mode int) (Experiment, error) {
	if err := x.Validate(); err != nil {
		return Experiment{}, err
	}
	for _, p := range x.Parts {
		if p.Experiment.Mode == mode {
			return p.Experiment, nil
		}
	}
	return Experiment{}, fmt.Errorf("mixed experiment does not declare mode %d", mode)
}

func (x MixedExperiment) validateEvaluationPart(part *Experiment) error {
	if part == nil {
		return fmt.Errorf("mixed evaluation requires its mode-specific experiment")
	}
	expected, err := x.evaluationPart(part.Mode)
	if err != nil {
		return err
	}
	a, _ := Digest(expected)
	b, err := Digest(*part)
	if err != nil || a != b {
		return fmt.Errorf("evaluation child differs from mixed experiment")
	}
	return nil
}

func (x MixedExperiment) validateEvaluationCandidate(a battlepolicy.Artifact, part Experiment, split string) error {
	if split != "validation" && split != "test" {
		return fmt.Errorf("mixed evaluation requires validation or test split")
	}
	if err := x.validateEvaluationPart(&part); err != nil {
		return err
	}
	if split == "validation" && part.InitialModel != "" {
		id, _ := Digest(a)
		if id == part.InitialModel {
			return x.ValidateParent(&a)
		}
	}
	return x.ValidateCandidate(a)
}

// EvaluateMixedRecorded reports one declared mode without changing the model's
// composite experiment identity. The full envelope travels with its evidence.
func EvaluateMixedRecorded(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, x MixedExperiment, mode int, split, output string, progress func(EvaluationGame) error, options ...EvaluationRecordingOptions) (EvaluationReport, error) {
	part, err := x.evaluationPart(mode)
	if err != nil {
		return EvaluationReport{}, err
	}
	return evaluateRecordedWithMixed(ctx, engine, candidate, opponents, experimentEvaluationConfig(part, split), &part, &x, split, output, progress, options...)
}

// FreezeMixedFinalSelection binds one candidate across every mode, then binds
// that mode's complete opponent set. Mode-specific baselines may differ, but a
// later mode cannot quietly select new shared weights after seeing test scores.
// Existing exact selections are reusable; conflicting files are never replaced.
func FreezeMixedFinalSelection(ctx context.Context, path string, x MixedExperiment, mode int, candidate battlepolicy.Artifact, opponents []Opponent) error {
	part, err := x.evaluationPart(mode)
	if err != nil {
		return err
	}
	schedule, err := prepareEvaluationWithMixed(ctx, part.Environment, candidate, opponents, experimentEvaluationConfig(part, "test"), &part, &x, "test")
	if err != nil {
		return err
	}
	selection := struct {
		Schema     string `json:"schema"`
		Experiment string `json:"experiment"`
		Candidate  string `json:"candidate_artifact"`
	}{"commander-mixed-final-selection-v1", schedule.Report.MixedExperimentDigest, schedule.Report.CandidateArtifact}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := writeObject(path, selection); err != nil {
		return err
	}
	var frozen []evaluationOpponent
	for _, opponent := range opponents {
		o := evaluationOpponent{Name: opponent.Name, Rule: opponent.Rule}
		if opponent.Model != nil {
			o.Artifact, err = Digest(*opponent.Model)
			if err != nil {
				return err
			}
		}
		frozen = append(frozen, o)
	}
	modeSelection := struct {
		Schema    string               `json:"schema"`
		Selection string               `json:"selection"`
		Mode      int                  `json:"mode"`
		Opponents []evaluationOpponent `json:"opponents"`
	}{Schema: "commander-mixed-final-mode-v1", Mode: mode, Opponents: frozen}
	modeSelection.Selection, err = Digest(selection)
	if err != nil {
		return err
	}
	return writeObject(fmt.Sprintf("%s.mode-%d.json", path, mode), modeSelection)
}
