package battletrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestFeedbackExportBindsSourcesAndHistoricalEpoch(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	ctx, root := context.Background(), filepath.Join(t.TempDir(), "run")
	var first FeedbackCheckpoint
	var firstState LearningState
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 2, Progress: func(p FeedbackProgress) error {
		if p.Event == "feedback_epoch_committed" && p.Epochs == 1 {
			var err error
			first, firstState, _, err = LoadFeedbackCheckpoint(ctx, root)
			return err
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	firstID, _ := Digest(first)
	before, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
	path, err := ExportFeedbackCandidate(ctx, root, firstID, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
	if string(before) != string(after) || !reflect.DeepEqual(a.Network, firstState.Model) || a.Status != "candidate" || !reflect.DeepEqual(a.Modes, initial.Modes) {
		t.Fatal("historical export changed live progress or inference weights/modes")
	}
	var report FeedbackExportReport
	if err := readObject(filepath.Join(root, "reports", a.TrainingReport+".json"), &report, 4<<20); err != nil {
		t.Fatal(err)
	}
	reportID, _ := Digest(report)
	if reportID != a.TrainingReport || report.Schema != "commander-feedback-export-v1" || report.Checkpoint != firstID || !reflect.DeepEqual(report.Training, first) || report.Training.Initial != first.Initial || len(report.Training.Datasets) != 2 {
		t.Fatal("export lost feedback initialization, sources or epoch provenance")
	}
	x, err := LoadExperiment(filepath.Join(root, "feedback-data", "experiments", a.Experiment+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := x.validateCandidateProvenance(a); err != nil {
		t.Fatal(err)
	}
	wantShards := initial.TrainingShards
	for _, id := range first.Datasets {
		var d FeedbackDataset
		if err := readObject(filepath.Join(root, "feedback-data", "datasets", id+".json"), &d, 16<<20); err != nil {
			t.Fatal(err)
		}
		wantShards = sortedUnion(wantShards, []string{d.Source})
	}
	if !reflect.DeepEqual(a.TrainingShards, wantShards) || !reflect.DeepEqual(a.TrainingGroups, initial.TrainingGroups) || !reflect.DeepEqual(a.HeldoutGroups, x.heldoutGroups()) {
		t.Fatal("export source or held-out sets differ")
	}
	// Existing inference accepts the unchanged native artifact contract;
	// provenance-aware consumers can follow its distinct training receipt.
	if _, err := (Policy{Model: a.Network, Greedy: true}).Version(); err != nil {
		t.Fatal(err)
	}
	latest, err := ExportFeedbackCandidate(ctx, root, "", "")
	if err != nil || latest == path {
		t.Fatal("latest epoch did not export separately", err)
	}
	if repeated, err := ExportFeedbackCandidate(ctx, root, firstID, path); err != nil || repeated != path {
		t.Fatal("idempotent export failed", err)
	}
	if _, err := ExportFeedbackCandidate(ctx, root, "", path); err == nil {
		t.Fatal("different epoch overwrote historical candidate")
	}
}

func TestFeedbackExportRefusesUntrainedAndDamagedSources(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	for _, kind := range []string{"untrained", "source", "labels", "initial", "epoch", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, root := context.Background(), filepath.Join(t.TempDir(), "run")
			stop := errors.New("stop at ready")
			err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 1, Progress: func(p FeedbackProgress) error {
				if kind == "untrained" && p.Event == "feedback_training_ready" {
					return stop
				}
				return nil
			}})
			if err != nil && !errors.Is(err, stop) {
				t.Fatal(err)
			}
			c, _, _, err := LoadFeedbackCheckpoint(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			var d FeedbackDataset
			if err := readObject(filepath.Join(root, "feedback-data", "datasets", c.Datasets[0]+".json"), &d, 16<<20); err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{
				"source":  filepath.Join(root, "feedback-data", "sources", d.Source+".jsonl.gz"),
				"labels":  filepath.Join(root, "feedback-data", "labels", d.Examples[0].Targets+".json"),
				"initial": filepath.Join(root, "initial-models", c.Initial+".json"),
			}
			if kind == "epoch" {
				paths[kind] = filepath.Join(root, "feedback-reports", c.Reports[0]+".json")
			}
			if path := paths[kind]; path != "" {
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			output := filepath.Join(root, "should-not-exist", "model.json")
			if _, err := ExportFeedbackCandidate(ctx, root, "", output); err == nil {
				t.Fatal("invalid export accepted")
			}
			if _, err := os.Stat(filepath.Dir(output)); !os.IsNotExist(err) {
				t.Fatal("invalid export wrote output")
			}
		})
	}
}

func TestFeedbackExportPreservesDeclaredParentSourceDomains(t *testing.T) {
	for _, kind := range []string{"native", "recorded", "recorded-selection"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			_, source, artifacts := feedbackStorageFixture(t)
			parent := artifacts[source.Source.Policy]
			if kind == "recorded" {
				parent = recordedEvaluationFixture(t)
			}
			if kind == "recorded-selection" {
				parent.Schema = 5
				parent.RecordedSelectionArtifacts = []string{strings.Repeat("a", 64)}
			}
			meta := parent.Environment
			meta.Scenario = "controlled-battle-v8"
			ec := DefaultEvaluationConfig()
			ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns, ec.Seed = 1, 120, 0, 35, 4, 9281
			x, err := NewExperimentFromSources(ctx, meta, ec, [3]int{1, 1, 1}, nil, &parent)
			if err != nil {
				t.Fatal(err)
			}
			tr := testTrajectory(t, parent.Network, 4)
			x.Families[0] = ExperimentFamily{Group: tr.Group, Split: "train", Scenario: tr.Setup}
			if err := x.Validate(); err != nil {
				t.Fatal(err)
			}
			labels, err := LabelRuleFeedback(ctx, tr, "control")
			if err != nil {
				t.Fatal(err)
			}
			e := FeedbackExample{Source: tr, Targets: labels, Behavior: Policy{Model: parent.Network}}
			data := t.TempDir()
			id, err := SaveFeedbackDataset(ctx, data, x, []FeedbackExample{e}, map[string]battlepolicy.Artifact{tr.Policy: parent})
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "run")
			update := DefaultWarmupConfig().Update
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: []FeedbackDatasetInput{{data, id}}, Initial: &parent, Update: &update, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
			path, err := ExportFeedbackCandidate(ctx, root, "", "")
			if err != nil {
				t.Fatal(err)
			}
			a, err := battlepolicy.LoadArtifact(path)
			if err != nil {
				t.Fatal(err)
			}
			parentID, _ := Digest(parent)
			if a.Parent != parentID || !reflect.DeepEqual(a.Recorded, parent.Recorded) || !reflect.DeepEqual(a.RecordedSelectionArtifacts, parent.RecordedSelectionArtifacts) || !reflect.DeepEqual(a.SelectionGroups, x.SelectionGroups) || !reflect.DeepEqual(a.TrainingGroups, sortedUnion(parent.TrainingGroups, []string{tr.Group})) {
				t.Fatal("feedback export lost parent influence or mixed source domains")
			}
			if err := x.validateCandidateProvenance(a); err != nil {
				t.Fatal(err)
			}
			schedule, err := prepareEvaluation(ctx, meta, a, []Opponent{{Name: "basic", Rule: "basic"}}, ec, &x, "validation")
			if err != nil {
				t.Fatal(err)
			}
			if (kind != "native") != (len(schedule.Report.UnverifiedSourceArtifacts) == 1) {
				t.Fatal("evaluation lost recorded-source uncertainty")
			}
		})
	}
}

func TestFeedbackExportRetainsBehaviorTrainingInfluence(t *testing.T) {
	ctx := context.Background()
	old, e, _ := feedbackStorageFixture(t)
	ec := DefaultEvaluationConfig()
	ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = 1, 120, 0, 35, 4
	x, err := NewExperiment(ctx, old.Environment, ec, [3]int{2, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	x.Families[0] = ExperimentFamily{Group: e.Source.Group, Split: "train", Scenario: e.Source.Setup}
	if err := x.Validate(); err != nil {
		t.Fatal(err)
	}
	initial := experimentCandidate(t, x)
	behavior := initial
	behavior.TrainingGroups = sortedUnion(initial.TrainingGroups, []string{x.groups("train")[1].Group})
	behavior.TrainingShards = sortedUnion(initial.TrainingShards, []string{strings.Repeat("d", 64)})
	data := t.TempDir()
	id, err := SaveFeedbackDataset(ctx, data, x, []FeedbackExample{e}, map[string]battlepolicy.Artifact{e.Source.Policy: behavior})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "run")
	update := DefaultWarmupConfig().Update
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: []FeedbackDatasetInput{{data, id}}, Initial: &initial, Update: &update, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	path, err := ExportFeedbackCandidate(ctx, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.TrainingGroups, behavior.TrainingGroups) {
		t.Fatal("export omitted behavior's indirect training influence")
	}
	for _, shard := range behavior.TrainingShards {
		found := false
		for _, actual := range a.TrainingShards {
			found = found || shard == actual
		}
		if !found {
			t.Fatal("lost behavior source shard")
		}
	}
	var receipt FeedbackExportReport
	if err := readObject(filepath.Join(root, "reports", a.TrainingReport+".json"), &receipt, 4<<20); err != nil {
		t.Fatal(err)
	}
	behaviorID, _ := Digest(behavior)
	if !reflect.DeepEqual(receipt.BehaviorArtifacts, []string{behaviorID}) {
		t.Fatal("missing frozen behavior receipt")
	}
}
