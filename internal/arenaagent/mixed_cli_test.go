package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestMixedCLIHelpAndConflicts(t *testing.T) {
	for _, command := range []string{"experiment-mix", "train", "evaluate"} {
		var out bytes.Buffer
		if err := Main(context.Background(), []string{command, "--help"}, "test", &out); err != nil {
			t.Fatal(err)
		}
		want := "mixed-experiment"
		if command == "experiment-mix" {
			want = "families-per-batch"
		}
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing mixed help", command)
		}
	}
	for _, flag := range []string{"experiment", "mode", "warmup-matches", "batch-matches", "demonstrations", "database", "opening-rollouts", "batch-episodes"} {
		var out bytes.Buffer
		err := Main(context.Background(), []string{"train", "--mixed-experiment", "unread", "--environment", "unread", "--data-dir", filepath.Join(t.TempDir(), "unused"), "--" + flag, "1"}, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "--"+flag) {
			t.Fatal("mixed conflict reached IO", flag, err)
		}
	}
	for _, args := range [][]string{
		{"evaluate", "--mixed-experiment", "unread"},
		{"evaluate", "--mixed-experiment", "unread", "--mode", "1", "--matches", "8"},
		{"evaluate", "--mixed-experiment", "unread", "--experiment", "unread"},
		{"train", "--mixed-experiment", "unread", "--resume"},
		{"experiment-mix", "--experiment", "unread", "--output", "unused"},
	} {
		var out bytes.Buffer
		if err := Main(context.Background(), args, "test", &out); err == nil {
			t.Fatal("invalid mixed arguments accepted", args)
		}
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "mixed-checkpoints"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"train", "--resume", "--environment", "unread", "--data-dir", root, "--seed", "3"}, "test", &out)
	if err == nil || !strings.Contains(err.Error(), "--seed") {
		t.Fatal("mixed resume accepted changed recipe", err)
	}
	if err := os.WriteFile(filepath.Join(root, "latest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hasMixedCheckpoint(root); err == nil {
		t.Fatal("mixed/single ambiguous directory accepted")
	}
}

type mixedStopWriter struct {
	bytes.Buffer
	stop error
}

func (w *mixedStopWriter) Write(p []byte) (int, error) {
	n, _ := w.Buffer.Write(p)
	var event struct {
		Event string
		Games int
	}
	if json.Unmarshal(p, &event) == nil && event.Event == "mixed_collected" && event.Games == 9 {
		return n, w.stop
	}
	return n, nil
}

// Opt-in, bounded installed-command logic against the real native engine.
// Reuses an earlier 32-game source, runs 32 new training + 64 evaluation games.
func TestNativeMixedCLICollectionResumeEvaluation(t *testing.T) {
	env, source, root := os.Getenv("STONEAGE_MIXED_CLI_ENV"), os.Getenv("STONEAGE_MIXED_CLI_SOURCE"), os.Getenv("STONEAGE_MIXED_CLI_ROOT")
	if env == "" || source == "" || root == "" {
		t.Skip("explicit persistent native mixed CLI fixture required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("preserve prior fixture; choose new root", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	parentPath, err := battletrain.ExportMixedCandidate(ctx, source, "", filepath.Join(root, "parent.json"))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := battlepolicy.LoadArtifact(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "mixed.json")
	args := []string{"experiment-mix", "--from-model", parentPath, "--output", manifest}
	for _, mode := range []int{1, 5} {
		c := battletrain.DefaultEvaluationConfig()
		c.Mode = mode
		c.Seed = int64(6500 + mode)
		c.MaxTurns = 3
		c.PetPoints = 0
		x, err := battletrain.NewExperimentFromSources(ctx, parent.Environment, c, [3]int{3, 2, 2}, nil, &parent)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, strings.Repeat("m", mode)+".json")
		if _, err := battletrain.SaveExperiment(path, x); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--experiment", path)
	}
	var out bytes.Buffer
	if err := Main(ctx, args, "test", &out); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "training")
	stop := errors.New("intentional native CLI partial batch stop")
	w := &mixedStopWriter{stop: stop}
	err = Main(ctx, []string{"train", "--environment", env, "--data-dir", data, "--mixed-experiment", manifest, "--from-model", parentPath, "--seed", "6901", "--workers", "1", "--epochs", "1", "--sequence-length", "2", "--opponent-mix", "100,0,0", "--rule-opponent", "basic"}, "test", w)
	if !errors.Is(err, stop) || strings.Contains(w.String(), "candidate_saved") {
		t.Fatal("partial CLI failure lost or exported candidate", err)
	}
	partial, err := battletrain.LoadMixedCheckpoint(ctx, data)
	if err != nil || partial.Checkpoint.NextGame != 9 {
		t.Fatal("CLI partial progress lost", err)
	}
	model := filepath.Join(root, "candidate.json")
	if err := Main(ctx, []string{"train", "--environment", env, "--data-dir", data, "--resume", "--batches", "2", "--workers", "3", "--output", model}, "test", &out); err != nil {
		t.Fatal(err)
	}
	loaded, err := battletrain.LoadMixedCheckpoint(ctx, data)
	if err != nil || loaded.Checkpoint.NextGame != 32 || len(loaded.Checkpoint.Reports) != 2 {
		t.Fatal("CLI resume progress differs", err)
	}
	if err := Main(ctx, []string{"export-model", "--data-dir", data, "--output", model}, "test", &out); err != nil {
		t.Fatal("CLI re-export failed", err)
	}
	for _, mode := range []string{"1", "5"} {
		report := filepath.Join(root, "evaluation-"+mode+".json")
		if err := Main(ctx, []string{"evaluate", "--environment", env, "--mixed-experiment", manifest, "--mode", mode, "--model", model, "--opponent", "basic", "--opponent-model", model, "--output", report}, "test", &out); err != nil {
			t.Fatal(err)
		}
		if err := Main(ctx, []string{"verify-evaluation", "--report", report}, "test", &out); err != nil {
			t.Fatal(err)
		}
		r, err := battletrain.VerifyEvaluation(ctx, report)
		if err != nil || len(r.Games) != 32 || r.MixedExperimentDigest != loaded.Checkpoint.Config.Experiment {
			t.Fatal("CLI mixed evidence incomplete", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "command-output.jsonl"), out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "interrupted-output.jsonl"), w.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("32 newly trained games, 64 mode-specific evaluation games, replay verification, CLI resume and export passed")
}
