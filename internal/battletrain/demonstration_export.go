package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// ExportDemonstrationCandidate validates frozen observations, epoch provenance
// and complete optimizer progress, then exports only inference weights and
// source digests. It does not manufacture native scenario or held-out groups.
func ExportDemonstrationCandidate(ctx context.Context, root, checkpoint, output string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c, state, _, err := LoadDemonstrationCheckpointID(ctx, root, checkpoint)
	if err != nil {
		return "", err
	}
	if len(c.Reports) == 0 {
		return "", fmt.Errorf("demonstration export requires a completed imitation epoch")
	}
	id, err := Digest(c)
	if err != nil {
		return "", err
	}
	weights, err := ModelDigest(state.Model)
	if err != nil {
		return "", err
	}
	a := battlepolicy.Artifact{
		Schema: 3, Architecture: battlepolicy.NetworkArchitecture(state.Model.Config), Features: c.Features, Actions: battlepolicy.ActionsForFeatures(c.Features), Status: "candidate",
		Environment: battleenv.Metadata{Rules: c.Rules, Platform: c.Platform}, Modes: []int{c.Mode}, WeightsDigest: weights, TrainingReport: id, Network: state.Model,
		Recorded: &battlepolicy.RecordedTraining{Schema: "commander-recorded-training-v1", Checkpoint: id, Dataset: c.Dataset.Digest, Sources: append([]string(nil), c.Dataset.Sources...), GroupKind: "recorded-roster-v1", RosterGroups: append([]string(nil), c.Dataset.Groups...), Epochs: len(c.Reports)},
	}
	if err := a.Validate(); err != nil {
		return "", err
	}
	artifactID, err := Digest(a)
	if err != nil {
		return "", err
	}
	if output == "" {
		output = filepath.Join(root, "models", artifactID+".json")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return "", err
	}
	return output, writeObject(output, a)
}
