package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func feedbackRunFixture(t *testing.T) (string, []FeedbackDatasetInput, battlepolicy.Artifact, ImitationConfig) {
	t.Helper()
	x, e, artifacts := feedbackStorageFixture(t)
	root := t.TempDir()
	var inputs []FeedbackDatasetInput
	for i := 0; i < 2; i++ {
		if i == 1 {
			// Distinct numerical fixture match; actual multi-match storage is
			// separately verified against the native worker.
			b, err := json.Marshal(e.Source)
			if err != nil {
				t.Fatal(err)
			}
			b = []byte(strings.ReplaceAll(string(b), `"test"`, `"feedback-second"`))
			if err := json.Unmarshal(b, &e.Source); err != nil {
				t.Fatal(err)
			}
			e.Targets, err = LabelRuleFeedback(context.Background(), e.Source, "control")
			if err != nil {
				t.Fatal(err)
			}
		}
		id, err := SaveFeedbackDataset(context.Background(), root, x, []FeedbackExample{e}, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, FeedbackDatasetInput{root, id})
	}
	cfg := DefaultWarmupConfig().Update
	cfg.BatchEpisodes, cfg.SequenceLength = 1, 2
	return root, inputs, artifacts[e.Source.Policy], cfg
}

func TestFeedbackRunResumeFreezesAggregateAndAdam(t *testing.T) {
	for _, batch := range []int{0, 1} {
		t.Run(map[int]string{0: "full-batch", 1: "minibatch"}[batch], func(t *testing.T) {
			datasetRoot, inputs, initial, update := feedbackRunFixture(t)
			update.BatchEpisodes = batch
			before, _ := Digest(initial)
			ctx, root := context.Background(), t.TempDir()
			full, resumed := filepath.Join(root, "full"), filepath.Join(root, "resumed")
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: full, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 3}); err != nil {
				t.Fatal(err)
			}
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: resumed, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(datasetRoot, datasetRoot+"-unavailable"); err != nil {
				t.Fatal(err)
			}
			defer os.Rename(datasetRoot+"-unavailable", datasetRoot)
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: resumed, Resume: true, Epochs: 2}); err != nil {
				t.Fatal(err)
			}
			a, sa, ea, err := LoadFeedbackCheckpoint(ctx, full)
			if err != nil {
				t.Fatal(err)
			}
			b, sb, eb, err := LoadFeedbackCheckpoint(ctx, resumed)
			if err != nil {
				t.Fatal(err)
			}
			wantSteps := 3
			if batch == 1 {
				wantSteps = 6
			}
			if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) || !reflect.DeepEqual(ea, eb) || len(a.Reports) != 3 || len(a.Datasets) != 2 || sa.Optimizer.Step != wantSteps {
				t.Fatal("feedback resume changed frozen inputs, reports, model or Adam")
			}
			after, _ := Digest(initial)
			if before != after {
				t.Fatal("feedback changed caller's initial artifact")
			}
			if _, _, err := LoadCheckpoint(resumed); err == nil {
				t.Fatal("feedback checkpoint entered PPO")
			}
			if _, err := ExportCandidate(resumed, filepath.Join(root, "invalid-export.json")); err == nil {
				t.Fatal("feedback exported without its own provenance")
			}
		})
	}
}

func TestFeedbackRunCommitBoundariesAndStorageStop(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	for _, event := range []string{"feedback_training_ready", "feedback_epoch_committed", "storage_limit_reached"} {
		t.Run(event, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "run")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := FeedbackRunOptions{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 3}
			seen := false
			if event == "storage_limit_reached" {
				options.StopAtDataBytes = 1
			}
			options.Progress = func(p FeedbackProgress) error {
				if p.Event == event {
					seen = true
					if event == "storage_limit_reached" {
						if p.DataBytes < 1 || p.StopAtBytes != 1 || p.StorageTriggerStage != "feedback_training_ready" {
							t.Fatal("missing storage threshold evidence")
						}
					} else {
						cancel()
					}
				}
				return nil
			}
			err := RunFeedback(ctx, options)
			var limit *StorageLimitError
			if !seen || event == "storage_limit_reached" && !errors.As(err, &limit) || event != "storage_limit_reached" && !errors.Is(err, context.Canceled) {
				t.Fatal("wrong termination", err)
			}
			c, state, _, err := LoadFeedbackCheckpoint(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if event == "feedback_epoch_committed" {
				want = 1
			}
			if len(c.Reports) != want || state.Optimizer.Step != 2*want {
				t.Fatal("lost committed epoch")
			}
			if err := RunFeedback(context.Background(), FeedbackRunOptions{Directory: root, Resume: true, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeedbackRunRejectsOverridesConcurrentWriterAndDuplicateData(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	ctx, root := context.Background(), filepath.Join(t.TempDir(), "run")
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 1, Progress: func(p FeedbackProgress) error {
		if p.Event == "feedback_training_ready" {
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Resume: true, Epochs: 1}); err == nil {
				t.Fatal("concurrent writer accepted")
			}
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
	for _, opts := range []FeedbackRunOptions{
		{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 1},
		{Directory: root, Resume: true, Datasets: inputs, Epochs: 1},
		{Directory: root, Resume: true, Initial: &initial, Epochs: 1},
		{Directory: root, Resume: true, Update: &update, Epochs: 1},
		{Directory: root, Resume: true, Epochs: 0},
		{Directory: root, Resume: true, Epochs: 10001},
		{Directory: root, Resume: true, Epochs: 1, StopAtDataBytes: -1},
	} {
		if err := RunFeedback(ctx, opts); err == nil {
			t.Fatal("invalid invocation accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
	if string(before) != string(after) {
		t.Fatal("rejected invocation changed checkpoint")
	}
	x, e, artifacts := feedbackStorageFixture(t)
	dataRoot := t.TempDir()
	first, err := SaveFeedbackDataset(ctx, dataRoot, x, []FeedbackExample{e}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	e.Targets, err = LabelRuleFeedback(ctx, e.Source, "sustain")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SaveFeedbackDataset(ctx, dataRoot, x, []FeedbackExample{e}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{first, first}, {first, second}} {
		uncreated := filepath.Join(t.TempDir(), "uncreated")
		var duplicate []FeedbackDatasetInput
		for _, id := range ids {
			duplicate = append(duplicate, FeedbackDatasetInput{dataRoot, id})
		}
		if err := RunFeedback(ctx, FeedbackRunOptions{Directory: uncreated, Datasets: duplicate, Initial: &initial, Update: &update, Epochs: 1}); err == nil {
			t.Fatal("duplicate dataset or relabeled perspective accepted")
		}
		if _, err := os.Stat(uncreated); !os.IsNotExist(err) {
			t.Fatal("invalid aggregate wrote a run directory")
		}
	}
}

func TestFeedbackRunRejectsDamagedCheckpointChain(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	for _, kind := range []string{"initial", "dataset", "report", "early-learning", "learning", "checkpoint", "rebound-report", "rebound-progress", "rebound-update", "rebound-order"} {
		t.Run(kind, func(t *testing.T) {
			ctx, root := context.Background(), filepath.Join(t.TempDir(), "run")
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 2}); err != nil {
				t.Fatal(err)
			}
			c, _, _, err := LoadFeedbackCheckpoint(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := Digest(c)
			var first FeedbackEpoch
			if err := readObject(filepath.Join(root, "feedback-reports", c.Reports[0]+".json"), &first, 16<<20); err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{
				"initial":        filepath.Join(root, "initial-models", c.Initial+".json"),
				"dataset":        filepath.Join(root, "feedback-data", "datasets", c.Datasets[0]+".json"),
				"report":         filepath.Join(root, "feedback-reports", c.Reports[0]+".json"),
				"early-learning": filepath.Join(root, "learning", first.AfterLearning+".json"),
				"learning":       filepath.Join(root, "learning", c.Learning+".json"),
				"checkpoint":     filepath.Join(root, "feedback-checkpoints", id+".json"),
			}
			if kind == "rebound-report" {
				first.Report.Disagreements++
				c.Reports[0], _ = Digest(first)
				if err := writeObject(filepath.Join(root, "feedback-reports", c.Reports[0]+".json"), first); err != nil {
					t.Fatal(err)
				}
				if err := saveFeedbackCheckpoint(root, c); err != nil {
					t.Fatal(err)
				}
			} else if kind == "rebound-progress" {
				c.Learning = c.Start
				if err := saveFeedbackCheckpoint(root, c); err != nil {
					t.Fatal(err)
				}
			} else if kind == "rebound-update" || kind == "rebound-order" {
				if kind == "rebound-update" {
					c.Update.LearningRate *= 2
				} else {
					c.Datasets[0], c.Datasets[1] = c.Datasets[1], c.Datasets[0]
				}
				if err := saveFeedbackCheckpoint(root, c); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(paths[kind], []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: root, Resume: true, Epochs: 1}); err == nil {
				t.Fatal("damaged evidence accepted")
			}
			after, _ := os.ReadFile(filepath.Join(root, FeedbackPointer))
			if string(before) != string(after) {
				t.Fatal("failed validation advanced checkpoint")
			}
		})
	}
}

func TestFeedbackRunStorageStopAfterEpochAndExactResume(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	ctx, parent := context.Background(), t.TempDir()
	full, stopped := filepath.Join(parent, "full"), filepath.Join(parent, "stopped")
	var readyBytes int64
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: full, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 3, Progress: func(p FeedbackProgress) error {
		if p.Event == "feedback_training_ready" {
			var err error
			readyBytes, err = trainingDataBytes(ctx, full)
			return err
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	var stoppedAt int
	err := RunFeedback(ctx, FeedbackRunOptions{Directory: stopped, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 3, StopAtDataBytes: readyBytes + 1, Progress: func(p FeedbackProgress) error {
		if p.Event == "storage_limit_reached" {
			if p.StorageTriggerStage != "feedback_epoch_committed" {
				t.Fatal("missing committed storage stop stage")
			}
			stoppedAt = p.Epochs
		}
		return nil
	}})
	var limit *StorageLimitError
	if !errors.As(err, &limit) || stoppedAt != 1 {
		t.Fatal("did not stop at first committed epoch", err, stoppedAt)
	}
	if err := RunFeedback(ctx, FeedbackRunOptions{Directory: stopped, Resume: true, Epochs: 2}); err != nil {
		t.Fatal(err)
	}
	a, sa, _, err := LoadFeedbackCheckpoint(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	b, sb, _, err := LoadFeedbackCheckpoint(ctx, stopped)
	if err != nil || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) {
		t.Fatal("storage stop changed resumed training", err)
	}
}

func TestFeedbackRunCancellationWithinEpochRetainsPreviousCommit(t *testing.T) {
	_, inputs, initial, update := feedbackRunFixture(t)
	parent := t.TempDir()
	full, interrupted := filepath.Join(parent, "full"), filepath.Join(parent, "interrupted")
	probe := &imitationCancelCounter{Context: context.Background()}
	var ready, firstEpoch int
	if err := RunFeedback(probe, FeedbackRunOptions{Directory: full, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 2, Progress: func(p FeedbackProgress) error {
		if p.Event == "feedback_training_ready" {
			ready = probe.Calls
		}
		if p.Event == "feedback_epoch_committed" && p.Epochs == 1 {
			firstEpoch = probe.Calls
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if firstEpoch <= ready {
		t.Fatal("missing epoch cancellation probe")
	}
	cancelled := &imitationCancelCounter{Context: context.Background()}
	committed := false
	err := RunFeedback(cancelled, FeedbackRunOptions{Directory: interrupted, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 2, Progress: func(p FeedbackProgress) error {
		if p.Event == "feedback_training_ready" {
			cancelled.Limit = cancelled.Calls + (firstEpoch-ready)*3/4
		}
		if p.Event == "feedback_epoch_committed" {
			committed = true
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || committed {
		t.Fatal("cancellation escaped epoch boundary", err)
	}
	c, state, _, err := LoadFeedbackCheckpoint(context.Background(), interrupted)
	if err != nil || len(c.Reports) != 0 || state.Optimizer.Step != 0 || c.Learning != c.Start {
		t.Fatal("partial epoch advanced saved state", err)
	}
	if err := RunFeedback(context.Background(), FeedbackRunOptions{Directory: interrupted, Resume: true, Epochs: 2}); err != nil {
		t.Fatal(err)
	}
	a, sa, _, err := LoadFeedbackCheckpoint(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	b, sb, _, err := LoadFeedbackCheckpoint(context.Background(), interrupted)
	if err != nil || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) {
		t.Fatal("cancelled epoch resume differs", err)
	}
}
