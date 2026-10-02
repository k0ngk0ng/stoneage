package battletrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func demonstrationRunFixture(t *testing.T) (string, DemonstrationRunConfig) {
	t.Helper()
	model := testModel(t)
	d := demonstrationFixture(t, teacherTrajectory(t, model, 3))
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if _, err := SaveDemonstrations(context.Background(), path, []Demonstration{d}); err != nil {
		t.Fatal(err)
	}
	update := DefaultWarmupConfig().Update
	update.BatchEpisodes = 1
	return path, DemonstrationRunConfig{Seed: 71, Network: model.Config, Update: update}
}

func TestDemonstrationRunResumeUsesFrozenDataAndAdam(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	root := t.TempDir()
	full, resumed := filepath.Join(root, "full"), filepath.Join(root, "resumed")
	ctx := context.Background()
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: full, Dataset: dataset, Config: &config, Epochs: 3}); err != nil {
		t.Fatal(err)
	}
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: resumed, Dataset: dataset, Config: &config, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataset); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataset + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: resumed, Resume: true, Epochs: 2}); err != nil {
		t.Fatal(err)
	}
	a, sa, _, err := LoadDemonstrationCheckpoint(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	b, sb, _, err := LoadDemonstrationCheckpoint(ctx, resumed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) || len(a.Reports) != 3 || sa.Optimizer.Step != 3 {
		t.Fatal("resume changed evidence/weights/moments")
	}
	if _, _, err := LoadCheckpoint(resumed); err == nil {
		t.Fatal("demonstration checkpoint accepted as PPO")
	}
	if _, err := ExportCandidate(resumed, filepath.Join(root, "invalid-model.json")); err == nil {
		t.Fatal("demonstration exported under synthetic provenance")
	}
}

func TestDemonstrationRunCancellationAndCommitBoundary(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	for _, event := range []string{"demonstration_training_ready", "demonstration_epoch_committed"} {
		t.Run(event, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "run")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 3, Progress: func(p DemonstrationProgress) error {
				if p.Event == event {
					cancel()
				}
				return nil
			}})
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			c, state, _, err := LoadDemonstrationCheckpoint(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if event == "demonstration_epoch_committed" {
				want = 1
			}
			if len(c.Reports) != want || state.Optimizer.Step != want {
				t.Fatal("cancellation lost committed boundary", c, state.Optimizer.Step)
			}
			if err := RunDemonstrations(context.Background(), DemonstrationRunOptions{Directory: root, Resume: true, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDemonstrationRunRejectsOverridesAndConcurrentWriter(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	root := filepath.Join(t.TempDir(), "run")
	err := RunDemonstrations(context.Background(), DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 1, Progress: func(p DemonstrationProgress) error {
		if p.Event == "demonstration_training_ready" {
			if err := RunDemonstrations(context.Background(), DemonstrationRunOptions{Directory: root, Resume: true, Epochs: 1}); err == nil {
				t.Fatal("second writer acquired training directory")
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, DemonstrationPointer))
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []DemonstrationRunOptions{
		{Directory: root, Dataset: dataset, Config: &config, Epochs: 1},
		{Directory: root, Resume: true, Config: &config, Epochs: 1},
		{Directory: root, Resume: true, Dataset: dataset, Epochs: 1},
		{Directory: root, Resume: true, Epochs: 0},
	} {
		if err := RunDemonstrations(context.Background(), options); err == nil {
			t.Fatal("invalid resume accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, DemonstrationPointer))
	if string(before) != string(after) {
		t.Fatal("rejected invocation changed committed state")
	}
}

func TestDemonstrationResumeRejectsDamagedEvidence(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	for _, kind := range []string{"dataset", "report", "learning", "checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "run")
			if err := RunDemonstrations(context.Background(), DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
			c, _, _, err := LoadDemonstrationCheckpoint(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := Digest(c)
			paths := map[string]string{
				"dataset":    filepath.Join(root, "demonstrations", c.Dataset.Digest+".jsonl"),
				"report":     filepath.Join(root, "demonstration-reports", c.Reports[0]+".json"),
				"learning":   filepath.Join(root, "learning", c.Learning+".json"),
				"checkpoint": filepath.Join(root, "demonstration-checkpoints", id+".json"),
			}
			before, _ := os.ReadFile(filepath.Join(root, DemonstrationPointer))
			if err := os.WriteFile(paths[kind], []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := RunDemonstrations(context.Background(), DemonstrationRunOptions{Directory: root, Resume: true, Epochs: 1}); err == nil {
				t.Fatal("corrupt evidence accepted")
			}
			after, _ := os.ReadFile(filepath.Join(root, DemonstrationPointer))
			if string(before) != string(after) {
				t.Fatal("failed verification advanced progress")
			}
		})
	}
}
