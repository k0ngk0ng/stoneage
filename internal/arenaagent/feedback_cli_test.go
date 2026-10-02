package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestFeedbackCLIHelpAndRejectedOverrides(t *testing.T) {
	for _, command := range []string{"collect-feedback", "train-feedback"} {
		var out bytes.Buffer
		if err := Main(context.Background(), []string{command, "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "resume") {
			t.Fatal("missing feedback help", command, err)
		}
		for _, flags := range [][]string{{"--model", "unopened.json"}, {"--seed", "3"}, {"--collection", "unopened"}, {"--teacher", "basic"}, {"--experiment", "unopened.json"}, {"--learning-rate", ".1"}} {
			root := filepath.Join(t.TempDir(), "uncreated")
			args := append([]string{command, "--data-dir", root, "--resume"}, flags...)
			if err := Main(context.Background(), args, "test", &out); err == nil {
				t.Fatal("resume override accepted", args)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("rejected command wrote files")
			}
		}
	}
	for _, other := range []string{"latest.json", "checkpoints", battletrain.DemonstrationPointer, "demonstration-checkpoints", battletrain.FeedbackCollectionPointer} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "feedback-checkpoints"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, other), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := Main(context.Background(), []string{"export-model", "--data-dir", root}, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatal("mixed trainer directory not rejected", other, err)
		}
	}
}

func TestNativeFeedbackCLICollectTrainResumeAndExport(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	_ = engine.Close()
	root := t.TempDir()
	ec := battletrain.DefaultEvaluationConfig()
	ec.Mode, ec.MaxTurns = 2, 2
	x, err := battletrain.NewExperiment(ctx, meta, ec, [3]int{2, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	experiment := filepath.Join(root, "experiment.json")
	xid, err := battletrain.SaveExperiment(experiment, x)
	if err != nil {
		t.Fatal(err)
	}
	c := battletrain.DefaultRunConfig()
	c.Mode, c.MaxTurns, c.Experiment = ec.Mode, ec.MaxTurns, xid
	c.Network.Width, c.Network.Heads, c.Network.Layers = 8, 2, 1
	c.Warmup.Matches, c.Warmup.Epochs = 8, 1
	c.BatchMatches, c.PPO.Epochs, c.PPO.SequenceLength = 2, 1, 2
	base := filepath.Join(root, "base")
	if err := battletrain.Run(ctx, battletrain.RunOptions{Directory: base, Command: command, Config: &c, Experiment: &x, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	model, err := battletrain.ExportCandidate(base, "")
	if err != nil {
		t.Fatal(err)
	}
	environment := filepath.Join(root, "environment.json")
	if err := os.WriteFile(environment, enc(Object{"schema_version": 1, "command": command}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(trainingEnvironmentVariable, environment)
	collection := filepath.Join(root, "collection")
	var out bytes.Buffer
	err = Main(ctx, []string{"collect-feedback", "--data-dir", collection, "--experiment", experiment, "--model", model, "--matches", "8", "--opponent", "basic", "--teacher", "control", "--workers", "2", "--stop-at-data-bytes", "1"}, "test", &out)
	var limit *battletrain.StorageLimitError
	if !errors.As(err, &limit) {
		t.Fatal("collection threshold did not retain ready checkpoint", err)
	}
	if err := Main(ctx, []string{"collect-feedback", "--data-dir", collection, "--resume", "--workers", "1"}, "test", &out); err != nil {
		t.Fatal(err)
	}
	cp, _, _, err := battletrain.LoadFeedbackCollection(ctx, collection)
	if err != nil || len(cp.Games) != 8 {
		t.Fatal("CLI collection incomplete", err)
	}
	t.Setenv(trainingEnvironmentVariable, "")
	if err := Main(ctx, []string{"collect-feedback", "--data-dir", collection, "--resume"}, "test", &out); err != nil {
		t.Fatal("complete resume needs no worker", err)
	}
	train := filepath.Join(root, "training")
	err = Main(ctx, []string{"train-feedback", "--collection", collection, "--data-dir", train, "--epochs", "1", "--stop-at-data-bytes", "1"}, "test", &out)
	if !errors.As(err, &limit) {
		t.Fatal("training threshold failed", err)
	}
	if _, err := os.Stat(filepath.Join(train, "models")); !os.IsNotExist(err) {
		t.Fatal("storage stop exported a model")
	}
	if err := os.Rename(collection, collection+"-moved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(collection+"-moved", collection)
	output := filepath.Join(root, "feedback-model.json")
	if err := Main(ctx, []string{"train-feedback", "--data-dir", train, "--resume", "--epochs", "1", "--output", output}, "test", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLearned(output, 2); err != nil {
		t.Fatal("exported feedback model cannot load", err)
	}
	if _, err := NewLearned(output, 3); err == nil {
		t.Fatal("feedback expanded inference modes")
	}
	saved, _, _, err := battletrain.LoadFeedbackCheckpoint(ctx, train)
	if err != nil || len(saved.Reports) != 1 {
		t.Fatal(err)
	}
	id, _ := battletrain.Digest(saved)
	if err := Main(ctx, []string{"export-model", "--data-dir", train, "--checkpoint", id, "--output", filepath.Join(root, "reexported.json")}, "test", &out); err != nil {
		t.Fatal("export-model missed feedback dispatch", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("feedback_collection_saved")) || !bytes.Contains(out.Bytes(), []byte("feedback_training_saved")) || !bytes.Contains(out.Bytes(), []byte("candidate_saved")) {
		t.Fatal("CLI omitted structured completion events")
	}
	t.Log("real initial warmup candidate -> collect-feedback -> threshold/resume -> train-feedback without original input -> candidate load and pinned export; short-cutoff functional fixture, not strength")
}
