package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type MixedExportReport struct {
	Initialization *PolicyInitialization      `json:"initialization,omitempty"`
	Schema         string                     `json:"schema"`
	Checkpoint     MixedCheckpoint            `json:"checkpoint"`
	Experiment     MixedExperiment            `json:"experiment"`
	Training       battlepolicy.MixedTraining `json:"training"`
}

// ExportMixedCandidate replays committed behavior and verifies the complete
// receipt chain before declaring modes. Pending games are never exported as
// updates. A rejected-only run cannot claim support inherited from its parent.
func ExportMixedCandidate(ctx context.Context, root, checkpoint, output string) (string, error) {
	loaded, err := LoadMixedCheckpointID(ctx, root, checkpoint)
	if err != nil {
		return "", err
	}
	x, c := loaded.Experiment, loaded.Checkpoint
	id, err := Digest(c)
	if err != nil {
		return "", err
	}
	summary := battlepolicy.MixedTraining{Schema: "commander-mixed-training-v1", Checkpoint: id, Objective: x.Mixture.Schema}
	for i, p := range x.Parts {
		summary.Modes = append(summary.Modes, battlepolicy.MixedModeTraining{Mode: p.Experiment.Mode, Experiment: p.Digest, Weight: x.Mixture.Modes[i].Weight, FamiliesPerBatch: p.FamiliesPerBatch})
	}
	var shards []string
	if loaded.Parent != nil {
		shards = append(shards, loaded.Parent.TrainingShards...)
	}
	for _, receipt := range loaded.Receipts {
		// All inspected batches remain provenance, including a rejected update.
		shards = sortedUnion(shards, receipt.Shards)
		if len(receipt.Report.Epochs) == 0 {
			continue
		}
		summary.Batches++
		summary.Updates += len(receipt.Report.Epochs)
		for i, count := range receipt.Report.Modes {
			summary.Modes[i].Episodes += count.Episodes
			summary.Modes[i].TeamTurns += count.TeamTurns
			summary.Modes[i].Actions += count.Actions
		}
	}
	if summary.Batches == 0 {
		return "", fmt.Errorf("mixed export requires an accepted shared update for every declared mode")
	}
	selection, heldout, recordedSelection := x.mixedProvenance()
	weights, err := ModelDigest(loaded.Learning.Model)
	if err != nil {
		return "", err
	}
	report := MixedExportReport{Schema: "commander-mixed-export-v1", Checkpoint: c, Experiment: x, Training: summary}
	if c.Initialization != "" {
		report.Initialization, err = verifyPolicyInitialization(ctx, root, c.Initialization, c.Config, loaded.Parent, c.Config.Network, c.Config.Seed, c.Config.InitialPolicyScale)
		if err != nil {
			return "", err
		}
		report.Schema = "commander-mixed-export-v2"
	}
	reportID, err := Digest(report)
	if err != nil {
		return "", err
	}
	features := battlepolicy.NetworkFeatures(c.Config.Network)
	a := battlepolicy.Artifact{
		Schema: 6, Mixed: &summary, Architecture: battlepolicy.NetworkArchitecture(c.Config.Network),
		Features: features, Actions: battlepolicy.ActionsForFeatures(features), Status: "candidate",
		Environment: x.Parts[0].Experiment.Environment, WeightsDigest: weights, TrainingReport: reportID,
		TrainingShards: shards, TrainingGroups: loaded.TrainingGroups, Network: loaded.Learning.Model,
		Experiment: c.Config.Experiment, Parent: x.Parts[0].Experiment.InitialModel,
		SelectionGroups: selection, HeldoutGroups: heldout, RecordedSelectionArtifacts: recordedSelection,
		Recorded: x.Parts[0].Experiment.InitialRecorded,
	}
	for _, p := range x.Parts {
		a.Modes = append(a.Modes, p.Experiment.Mode)
	}
	if err := x.ValidateCandidate(a); err != nil {
		return "", err
	}
	artifactID, err := Digest(a)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := writeFeedbackObject(filepath.Join(root, "reports", reportID+".json"), report, mixedExperimentBytes+(8<<20)); err != nil {
		return "", err
	}
	if output == "" {
		output = filepath.Join(root, "models", artifactID+".json")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return output, writeObject(output, a)
}

func (x MixedExperiment) mixedProvenance() (selection, heldout, recordedSelection []string) {
	for _, p := range x.Parts {
		selection = sortedUnion(selection, p.Experiment.SelectionGroups)
		heldout = sortedUnion(heldout, p.Experiment.heldoutGroups())
		recordedSelection = sortedUnion(recordedSelection, p.Experiment.RecordedSelectionArtifacts)
	}
	return
}

// ValidateCandidate checks the complete envelope, not a single child's identity.
// Checkpoint replay at export/verification establishes the update counts; this
// method establishes structural and split provenance for inference/evaluation.
func (x MixedExperiment) ValidateCandidate(a battlepolicy.Artifact) error {
	if err := x.Validate(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	id, _ := Digest(x)
	selection, heldout, recordedSelection := x.mixedProvenance()
	first := x.Parts[0].Experiment
	if a.Schema != 6 || a.Experiment != id || a.Environment != first.Environment || a.Parent != first.InitialModel || !slices.Equal(a.SelectionGroups, selection) || !slices.Equal(a.HeldoutGroups, heldout) || !slices.Equal(a.RecordedSelectionArtifacts, recordedSelection) || !reflect.DeepEqual(a.Recorded, first.InitialRecorded) || len(a.Modes) != len(x.Parts) {
		return fmt.Errorf("mixed candidate differs from experiment envelope or ancestry")
	}
	allowed := map[string]bool{}
	for i, p := range x.Parts {
		m := a.Mixed.Modes[i]
		if m.Mode != p.Experiment.Mode || m.Experiment != p.Digest || m.Weight != x.Mixture.Modes[i].Weight || m.FamiliesPerBatch != p.FamiliesPerBatch {
			return fmt.Errorf("mixed candidate mode/objective declaration differs from experiment")
		}
		for group := range p.Experiment.allowedTrainingGroups() {
			allowed[group] = true
		}
	}
	actual := map[string]bool{}
	for _, group := range a.TrainingGroups {
		if !allowed[group] {
			return fmt.Errorf("mixed candidate used undeclared training groups")
		}
		actual[group] = true
	}
	for _, group := range first.InitialTrainingGroups {
		if !actual[group] {
			return fmt.Errorf("mixed candidate dropped inherited training provenance")
		}
	}
	return nil
}
