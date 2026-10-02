package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestScenarioScheduleEqualBudgetsAndGroups(t *testing.T) {
	c := DefaultRunConfig()
	c.Mode = 5
	a, ga := scenarioFor(c, 0)
	b, gb := scenarioFor(c, 1)
	d, gd := scenarioFor(c, 4)
	if ga != gb || ga != gd || a.Seed != b.Seed || a.Seed == d.Seed {
		t.Fatal("repeat/swap grouping or seed schedule broken")
	}
	b.MaxTurns++
	if ScenarioGroup(b) != ga {
		t.Fatal("collection cutoff split identical configurations into different groups")
	}
	for i := 0; i < c.Mode; i++ {
		if a.Builds[i] != b.Builds[i+c.Mode] || a.PetBuilds[i] != b.PetBuilds[i+c.Mode] {
			t.Fatal("swap did not preserve configurations")
		}
	}
	for _, s := range []battleenv.Scenario{a, b, d} {
		for _, list := range [][]battleenv.Build{s.Builds, s.PetBuilds} {
			for _, build := range list {
				sum := 0
				for _, n := range build {
					if n < 1 {
						t.Fatal("illegal allocation")
					}
					sum += n
				}
				if sum != 120 {
					t.Fatal("unequal point budget")
				}
			}
		}
	}
}

func TestCheckpointRoundTripAndOptimizerValidation(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t)
	state := LearningState{Schema: 1, Model: m, Optimizer: &battlenet.Adam[float32]{}}
	id, e := saveLearning(filepath.Join(dir, "learning"), state)
	if e != nil {
		t.Fatal(e)
	}
	cfg := DefaultRunConfig()
	cfg.Pairing = "" // Original fixed-roster schedule.
	cfg.Warmup = nil // Historical schema-1 checkpoint must keep its checksum.
	cfg.OpponentSampling = ""
	cfg.RuleOpponents = nil // Historical rule scheduling must also remain unchanged.
	cfg.Network = m.Config
	c := Checkpoint{Schema: 1, Features: battlepolicy.FeatureVersion, Actions: battlepolicy.ActionVersion, Config: cfg, Environment: battleenv.Metadata{Rules: strings.Repeat("0", 64), Platform: "linux-amd64", Scenario: "controlled-battle-v8"}, Learning: id}
	if e = saveCheckpoint(dir, c); e != nil {
		t.Fatal(e)
	}
	got, s, e := LoadCheckpoint(dir)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c, got) || !reflect.DeepEqual(state, s) {
		t.Fatal("checkpoint changed state")
	}
	s.Optimizer.Step = 1
	if _, e = saveLearning(filepath.Join(dir, "learning"), s); e == nil {
		t.Fatal("incomplete optimizer checkpoint accepted")
	}
	if e = os.WriteFile(filepath.Join(dir, "learning", id+".json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadCheckpoint(dir); e == nil {
		t.Fatal("corrupt learning state loaded")
	}
}

func TestTrainingDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	f, e := os.OpenFile(filepath.Join(dir, ".training.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = lockTraining(f); e != nil {
		t.Fatal(e)
	}
	c := DefaultRunConfig()
	e = Run(context.Background(), RunOptions{Directory: dir, Command: []string{"must-not-start"}, Config: &c, Batches: 1})
	if e == nil || !strings.Contains(e.Error(), "already in use") {
		t.Fatal("second writer not rejected before engine launch", e)
	}
}

func TestNativeTrainingResumeAndExport(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Pairing = "" // Keep explicit native resume coverage for the legacy schedule.
	c.Network = testModel(t).Config
	c.Warmup = nil // This test isolates the pre-existing PPO resume path.
	c.OpponentSampling = ""
	c.RuleOpponents = nil
	c.PPO.Epochs = 1
	c.PPO.SequenceLength = 2
	c.MaxTurns = 5
	c.BatchMatches = 2
	one, two := t.TempDir(), t.TempDir()
	if e := Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	interrupted := errors.New("test interruption after committed match")
	e := Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Batches: 2, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "collected" {
			return interrupted
		}
		return nil
	}})
	if !errors.Is(e, interrupted) {
		t.Fatal("interruption not observed", e)
	}
	partial, _, e := LoadCheckpoint(two)
	if e != nil {
		t.Fatal(e)
	}
	if partial.NextGame != 1 || len(partial.Pending) != 1 || partial.CompletedBatches != 0 {
		t.Fatal("partial batch wasn't saved", partial)
	}
	if e = Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	a, sa, e := LoadCheckpoint(one)
	if e != nil {
		t.Fatal(e)
	}
	b, sb, e := LoadCheckpoint(two)
	if e != nil {
		t.Fatal(e)
	}
	if a.NextGame != 4 || b.NextGame != 4 || a.CompletedBatches != 2 || b.CompletedBatches != 2 || !reflect.DeepEqual(sa, sb) {
		t.Fatal("resumed weights/optimizer diverged from uninterrupted run")
	}
	path, e := ExportCandidate(two, "")
	if e != nil {
		t.Fatal(e)
	}
	artifact, e := battlepolicy.LoadArtifact(path)
	if e != nil {
		t.Fatal(e)
	}
	version, _ := ModelDigest(sb.Model)
	if artifact.Status != "candidate" || artifact.WeightsDigest != version || len(artifact.TrainingShards) != 4 {
		t.Fatal("export didn't describe actual trained candidate")
	}
	if e = Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); e == nil {
		t.Fatal("existing training directory reset without resume")
	}
	t.Logf("interrupted/resumed model equals uninterrupted; games=%d optimizer_steps=%d parameters=%d", b.NextGame, sb.Optimizer.Step, sb.Model.Count())
}
