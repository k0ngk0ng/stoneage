package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestDemonstrationCLITrainsAndResumesWithoutOriginalDataset(t *testing.T) {
	database := recordedFixture(t, nil)
	parent := t.TempDir()
	dataset := filepath.Join(parent, "demonstrations.jsonl")
	ctx := context.Background()
	if _, err := ImportDemonstrations(ctx, []string{database}, dataset, battlepolicy.FeatureVersion); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "training")
	var out bytes.Buffer
	if err := Main(ctx, []string{"train", "--demonstrations", dataset, "--data-dir", root, "--batch-episodes", "1", "--seed", "913"}, "test", &out); err != nil {
		t.Fatal(err)
	}
	a, state, _, err := battletrain.LoadDemonstrationCheckpoint(ctx, root)
	if err != nil || len(a.Reports) != 1 || state.Model.Count() != 197954 || a.Config.Seed != 913 || a.Mode != 2 || !bytes.Contains(out.Bytes(), []byte("demonstration_training_saved")) {
		t.Fatal(a, err, out.String())
	}
	if err := os.Remove(dataset); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataset + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := Main(ctx, []string{"train", "--data-dir", root, "--resume", "--epochs", "1"}, "test", &out); err != nil {
		t.Fatal(err)
	}
	b, _, _, err := battletrain.LoadDemonstrationCheckpoint(ctx, root)
	if err != nil || len(b.Reports) != 2 || b.Learning == a.Learning {
		t.Fatal("CLI resume did not advance exactly one epoch", err)
	}
	for _, flags := range [][]string{
		{"--seed", "914"}, {"--demonstrations", dataset}, {"--environment", "not-opened.json"}, {"--database", database},
		{"--output", filepath.Join(parent, "not-a-model.json")}, {"--batch-episodes", "1"}, {"--gradient-clip", "1"}, {"--from-model", "not-opened.json"},
	} {
		args := append([]string{"train", "--data-dir", root, "--resume"}, flags...)
		if err := Main(ctx, args, "test", &out); err == nil {
			t.Fatal("resume override accepted", flags)
		}
	}
	got, _, _, err := battletrain.LoadDemonstrationCheckpoint(ctx, root)
	if err != nil || got.Learning != b.Learning || len(got.Reports) != 2 {
		t.Fatal("rejected arguments changed state", err)
	}
	modelPath := filepath.Join(parent, "recorded-model.json")
	if err := Main(ctx, []string{"export-model", "--data-dir", root, "--output", modelPath}, "test", &out); err != nil {
		t.Fatal(err)
	}
	model, err := NewLearned(modelPath, 2)
	if err != nil || model.neural == nil || model.neural.Recorded == nil {
		t.Fatal("exported demonstration model cannot load", err)
	}
	if _, err := NewLearned(modelPath, 1); err == nil {
		t.Fatal("source modes silently expanded")
	}
	var history []Object
	for turn := 0; turn < 3; turn++ {
		team, events := neuralFixtureTeam(t, 2, turn)
		history = append(history, events...)
		decision, err := model.Decide(ctx, team, history)
		if err != nil || decision.Strategy != "learned" || validatePlan(team, decision.Plan) != nil {
			t.Fatal("recorded model cannot command complete team", turn, err)
		}
		repeat, err := model.Decide(ctx, team, history)
		if err != nil || !reflect.DeepEqual(repeat.Plan, decision.Plan) {
			t.Fatal("repeated observation advanced model memory", err)
		}
		history = append(history, neuralTurnRecord(team, decision))
	}
	// Selecting an immutable checkpoint must not depend on a mutable latest
	// pointer still being present, and must never select the other trainer.
	checkpointID, _ := battletrain.Digest(got)
	if err := os.Remove(filepath.Join(root, battletrain.DemonstrationPointer)); err != nil {
		t.Fatal(err)
	}
	if err := Main(ctx, []string{"export-model", "--data-dir", root, "--checkpoint", checkpointID, "--output", filepath.Join(parent, "pinned-model.json")}, "test", &out); err != nil {
		t.Fatal("lost immutable checkpoint dispatch", err)
	}
	if err := os.WriteFile(filepath.Join(root, "latest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Main(ctx, []string{"export-model", "--data-dir", root, "--checkpoint", checkpointID}, "test", &out); err == nil {
		t.Fatal("ambiguous training directory accepted")
	}
}

func TestDemonstrationCLIRejectsMixedTrainingBeforeIO(t *testing.T) {
	for _, flags := range [][]string{
		{"--environment", "no-engine.json"}, {"--database", "no-db.sqlite"}, {"--batches", "1"}, {"--mode", "1"},
		{"--from-model", "no-model.json"}, {"--output", "no-output.json"}, {"--warmup-matches", "0"}, {"--experiment", "no-experiment.json"},
	} {
		var out bytes.Buffer
		root := filepath.Join(t.TempDir(), "not-created")
		args := append([]string{"train", "--demonstrations", "not-opened.jsonl", "--data-dir", root}, flags...)
		if err := Main(context.Background(), args, "test", &out); err == nil {
			t.Fatal("mixed training accepted", flags)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("invalid invocation created output")
		}
	}
	for _, name := range []string{"--batch-episodes", "--gradient-clip"} {
		var out bytes.Buffer
		if err := Main(context.Background(), []string{"train", "--database", "old.sqlite", name, "1"}, "test", &out); err == nil {
			t.Fatal("demonstration-only option ignored")
		}
	}
}
