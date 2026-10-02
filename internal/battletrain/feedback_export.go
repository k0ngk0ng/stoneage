package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// FeedbackExportReport is the object bound by Artifact.TrainingReport. Native
// artifact schemas already name an arbitrary immutable training report; this
// distinct report schema preserves feedback sources without relabeling source
// actions as demonstrations or pretending that PPO produced the candidate.
type FeedbackExportReport struct {
	Schema            string             `json:"schema"`
	Checkpoint        string             `json:"checkpoint_digest"`
	Training          FeedbackCheckpoint `json:"training"`
	BehaviorArtifacts []string           `json:"behavior_artifacts"`
}

// ExportFeedbackCandidate exports one committed epoch, with native source
// groups/shards and the initial artifact's provenance retained. Parent means
// the experiment's original parent as in native training; the exact feedback
// initialization and label datasets are additionally bound in TrainingReport.
// No evaluation, promotion or extra supported mode is implied by exporting.
func ExportFeedbackCandidate(ctx context.Context, root, checkpoint, output string) (string, error) {
	c, state, examples, err := LoadFeedbackCheckpointID(ctx, root, checkpoint)
	if err != nil {
		return "", err
	}
	if len(c.Reports) == 0 {
		return "", fmt.Errorf("feedback export requires a completed epoch")
	}
	x, err := LoadExperiment(filepath.Join(root, "feedback-data", "experiments", c.Experiment+".json"))
	if err != nil {
		return "", err
	}
	experimentID, _ := Digest(x)
	if experimentID != c.Experiment {
		return "", fmt.Errorf("feedback experiment changed while exporting")
	}
	var initial battlepolicy.Artifact
	if err := readObject(filepath.Join(root, "initial-models", c.Initial+".json"), &initial, 128<<20); err != nil {
		return "", err
	}
	id, _ := Digest(initial)
	if id != c.Initial {
		return "", fmt.Errorf("feedback initialization changed while exporting")
	}
	shards := append([]string(nil), initial.TrainingShards...)
	groups := append([]string(nil), initial.TrainingGroups...)
	seenBehaviors := map[string]bool{}
	var behaviorArtifacts []string
	for _, dataset := range c.Datasets {
		var d FeedbackDataset
		if err := readObject(filepath.Join(root, "feedback-data", "datasets", dataset+".json"), &d, 16<<20); err != nil {
			return "", err
		}
		id, _ := Digest(d)
		if id != dataset {
			return "", fmt.Errorf("feedback dataset changed while exporting")
		}
		shards = sortedUnion(shards, []string{d.Source})
		for _, entry := range d.Examples {
			ref := entry.Behavior.Artifact
			if ref == "" || seenBehaviors[ref] {
				continue
			}
			var behavior battlepolicy.Artifact
			if err := readObject(filepath.Join(root, "feedback-data", "behaviors", ref+".json"), &behavior, 128<<20); err != nil {
				return "", err
			}
			id, _ := Digest(behavior)
			if id != ref {
				return "", fmt.Errorf("feedback behavior changed while exporting")
			}
			// A behavior policy shapes which states the learner sees. Retain
			// its training influence, even if those groups were not collected
			// again in this feedback batch or used by the initial learner.
			groups = sortedUnion(groups, behavior.TrainingGroups)
			shards = sortedUnion(shards, behavior.TrainingShards)
			seenBehaviors[ref] = true
			behaviorArtifacts = append(behaviorArtifacts, ref)
		}
	}
	for _, example := range examples {
		groups = append(groups, example.Source.Group)
	}
	checkpointID, _ := Digest(c)
	report := FeedbackExportReport{Schema: "commander-feedback-export-v1", Checkpoint: checkpointID, Training: c, BehaviorArtifacts: sortedUnion(behaviorArtifacts)}
	reportID, err := Digest(report)
	if err != nil {
		return "", err
	}
	weights, err := ModelDigest(state.Model)
	if err != nil {
		return "", err
	}
	a := battlepolicy.Artifact{
		Schema: 2, Architecture: battlepolicy.NetworkArchitecture(state.Model.Config),
		Features: battlepolicy.NetworkFeatures(state.Model.Config), Actions: battlepolicy.ActionsForFeatures(battlepolicy.NetworkFeatures(state.Model.Config)),
		Status: "candidate", Environment: x.Environment, Modes: []int{x.Mode},
		WeightsDigest: weights, TrainingReport: reportID, TrainingShards: sortedUnion(shards), TrainingGroups: sortedUnion(groups),
		Experiment: c.Experiment, Parent: x.InitialModel, SelectionGroups: x.SelectionGroups, HeldoutGroups: x.heldoutGroups(),
		Recorded: x.InitialRecorded, RecordedSelectionArtifacts: x.RecordedSelectionArtifacts, Network: state.Model,
	}
	if a.Recorded != nil {
		a.Schema = 4
	}
	if len(a.RecordedSelectionArtifacts) > 0 {
		a.Schema = 5
	}
	if err := a.Validate(); err != nil {
		return "", err
	}
	if err := x.validateCandidateProvenance(a); err != nil {
		return "", err
	}
	artifactID, err := Digest(a)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := writeFeedbackObject(filepath.Join(root, "reports", reportID+".json"), report, 4<<20); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if output == "" {
		output = filepath.Join(root, "models", artifactID+".safetensors")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return output, battlepolicy.SaveArtifact(output, a)
}
