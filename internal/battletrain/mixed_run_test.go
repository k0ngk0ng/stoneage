package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func mixedRunConfigFixture(t *testing.T, x MixedExperiment) MixedRunConfig {
	t.Helper()
	id, err := Digest(x)
	if err != nil {
		t.Fatal(err)
	}
	c := MixedRunConfig{Schema: "commander-mixed-run-v1", Experiment: id, Seed: 2909, Network: testModel(t).Config, PPO: DefaultPPOConfig(), RuleOpponents: []string{"basic", "focus"}, OpponentMix: OpponentMix{Rules: 50, History: 50}}
	c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
	return c
}

func TestMixedRunCloseFailuresPreserveCheckpoint(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	markers := filepath.Join(root, "markers")
	if err := os.Mkdir(markers, 0700); err != nil {
		t.Fatal(err)
	}
	x := mixedExperimentFixture(t)
	for i := range x.Parts {
		x.Parts[i].Experiment.Environment.Rules = strings.Repeat("a", 64)
		x.Parts[i].Digest, _ = Digest(x.Parts[i].Experiment)
	}
	c := mixedRunConfigFixture(t, x)
	want := errors.New("stop at committed mixed ready")
	dir := filepath.Join(root, "run")
	err = RunMixed(context.Background(), MixedRunOptions{Directory: dir, Config: &c, Experiment: &x, Batches: 1, Workers: 3, Command: []string{exe, "-test.run=^TestCollectionCloseHelper$", "--", "--collection-close=" + markers}, Progress: func(p MixedProgress) error { return want }})
	var exit *exec.ExitError
	if !errors.Is(err, want) || !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), "collection worker 1 shutdown") || !strings.Contains(err.Error(), "collection worker 2 shutdown") {
		t.Fatal("lost mixed original or worker shutdown failures", err)
	}
	files, err := os.ReadDir(markers)
	if err != nil || len(files) != 3 {
		t.Fatal("mixed workers did not drain on close", len(files), err)
	}
	loaded, err := LoadMixedCheckpoint(context.Background(), dir)
	if err != nil || loaded.Checkpoint.NextGame != 0 || loaded.Learning.Optimizer.Step != 0 {
		t.Fatal("close failure lost mixed initialization", err)
	}
	if _, _, err := LoadCheckpoint(dir); err == nil {
		t.Fatal("single-mode loader accepted mixed state")
	}
	if _, err := ExportMixedCandidate(context.Background(), dir, "", ""); err == nil {
		t.Fatal("initialization without an update exported as trained")
	}
	if err := RunMixed(context.Background(), MixedRunOptions{Directory: dir, Config: &c, Experiment: &x, Batches: 1, Command: []string{"must-not-start"}}); err == nil || !strings.Contains(err.Error(), "empty data directory") {
		t.Fatal("new run overwrote retained initialization", err)
	}
	if err := RunMixed(context.Background(), MixedRunOptions{Directory: dir, Resume: true, Config: &c, Batches: 1, Command: []string{"must-not-start"}}); err == nil || !strings.Contains(err.Error(), "frozen inputs") {
		t.Fatal("resume accepted replacement recipe", err)
	}
}

func TestMixedReportRejectsFalseCountsAndModeBounds(t *testing.T) {
	beforeModel := testModel(t)
	before := LearningState{Schema: 1, Model: beforeModel, Optimizer: &battlenet.Adam[float32]{}}
	var after LearningState
	if err := json.Unmarshal(mustJSON(t, before), &after); err != nil {
		t.Fatal(err)
	}
	episodes := []Trajectory{mixedTrajectory(t, beforeModel, 1, 2), mixedTrajectory(t, beforeModel, 5, 3)}
	x := mixedExperimentFixture(t)
	c := mixedRunConfigFixture(t, x)
	r, err := TrainMixed(context.Background(), after.Model, after.Optimizer, episodes, c.PPO, x.Mixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMixedReport(r, c, x.Mixture, episodes, before, after); err != nil {
		t.Fatal("valid mixed numerical report rejected", err)
	}
	for _, kind := range []string{"counts", "combined", "mode-kl", "mode-order", "lost-mixture", "lost-attempt", "optimizer"} {
		t.Run(kind, func(t *testing.T) {
			var bad Report
			if err := json.Unmarshal(mustJSON(t, r), &bad); err != nil {
				t.Fatal(err)
			}
			var changed LearningState
			if err := json.Unmarshal(mustJSON(t, after), &changed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "counts":
				bad.Modes[1].Actions++
			case "combined":
				bad.Epochs[0].Loss += .01
			case "mode-kl":
				*bad.Epochs[0].Modes[1].PostUpdateKL = 1
			case "mode-order":
				bad.Epochs[0].Modes[0].Mode = 5
			case "lost-mixture":
				bad.ModeMixture = nil
			case "lost-attempt":
				bad.RejectedUpdates++
			case "optimizer":
				changed.Optimizer.Step++
			}
			if validateMixedReport(bad, c, x.Mixture, episodes, before, changed) == nil {
				t.Fatal("false mixed report accepted")
			}
		})
	}
}

func copyMixedRunFixture(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeMixedTrainingPartialResumeAndProvenance(t *testing.T) {
	raw := os.Getenv("STONEAGE_MIXED_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit bounded mixed native fixture required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := os.Getenv("STONEAGE_MIXED_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("native fixture root must be new; preserve earlier attempts", err)
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	var parts []MixedExperimentPart
	for _, mode := range []int{1, 5} {
		e := DefaultEvaluationConfig()
		e.Mode, e.Seed, e.MaxTurns, e.PetPoints = mode, int64(401+mode), 3, 0
		x, err := NewExperiment(ctx, meta, e, [3]int{3, 2, 2})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
	}
	x, err := NewMixedExperiment(testModeMixture(), parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := mixedRunConfigFixture(t, x)
	one, two := filepath.Join(root, "serial"), filepath.Join(root, "resumed")
	if err := RunMixed(ctx, MixedRunOptions{Directory: one, Config: &c, Experiment: &x, Command: command, Batches: 2, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("fixture interrupted after first mode5 game")
	err = RunMixed(ctx, MixedRunOptions{Directory: two, Config: &c, Experiment: &x, Command: command, Batches: 2, Workers: 2, Progress: func(p MixedProgress) error {
		if p.Games == 9 {
			return want
		}
		return nil
	}})
	if !errors.Is(err, want) {
		t.Fatal("partial collection interruption not observed", err)
	}
	partial, err := LoadMixedCheckpoint(ctx, two)
	if err != nil || partial.Checkpoint.NextGame != 9 || len(partial.Checkpoint.Pending) != 9 || len(partial.Checkpoint.Reports) != 0 || partial.Learning.Optimizer.Step != 0 {
		t.Fatal("mixed partial batch was not preserved", err)
	}
	want = errors.New("fixture interrupted after committed first mixed update")
	err = RunMixed(ctx, MixedRunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Workers: 2, Progress: func(p MixedProgress) error {
		if p.Event == "mixed_batch_committed" {
			return want
		}
		return nil
	}})
	if !errors.Is(err, want) {
		t.Fatal("post-update interruption not observed", err)
	}
	partial, err = LoadMixedCheckpoint(ctx, two)
	if err != nil || partial.Checkpoint.NextGame != 16 || len(partial.Checkpoint.Pending) != 0 || len(partial.Checkpoint.Reports) != 1 || len(partial.History) != 1 {
		t.Fatal("mixed accepted batch/eligible historical pool was not preserved", err)
	}
	if err := RunMixed(ctx, MixedRunOptions{Directory: two, Command: command, Resume: true, Batches: 1, Workers: 3}); err != nil {
		t.Fatal(err)
	}
	a, err := LoadMixedCheckpoint(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadMixedCheckpoint(ctx, two)
	if err != nil || !reflect.DeepEqual(a.Learning, b.Learning) || !reflect.DeepEqual(a.TrainingGroups, b.TrainingGroups) || !reflect.DeepEqual(a.History, b.History) || b.Checkpoint.NextGame != 32 || len(b.Checkpoint.Reports) != 2 {
		t.Fatal("mixed serial/resumed model, Adam or provenance differs", err)
	}
	for _, kind := range []string{"prefix", "mode-slot", "probability", "report"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(root, "invalid-"+kind)
			copyMixedRunFixture(t, one, dir)
			cp := a.Checkpoint
			cp.Reports = append([]string(nil), cp.Reports...)
			if kind == "prefix" {
				cp.NextGame++
			} else {
				var r MixedBatchReceipt
				if err := readObject(filepath.Join(dir, "mixed-reports", cp.Reports[0]+".json"), &r, 4<<20); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "mode-slot":
					r.Shards[0], r.Shards[8] = r.Shards[8], r.Shards[0]
				case "probability":
					pair, _, err := LoadShard(filepath.Join(dir, "shards"), r.Shards[0])
					if err != nil {
						t.Fatal(err)
					}
					for i := range pair {
						if pair[i].PolicyKind == "network-sampled" {
							v := &pair[i].Steps[0].Value
							if *v > 0 {
								*v -= .02
							} else {
								*v += .02
							}
							break
						}
					}
					shard, err := SaveShard(filepath.Join(dir, "shards"), pair)
					if err != nil {
						t.Fatal("forged numerical value must pass structural validation", err)
					}
					r.Shards[0] = shard.Digest
				case "report":
					r.Report.Modes[1].Actions++
				}
				id, _ := Digest(r)
				if err := writeObject(filepath.Join(dir, "mixed-reports", id+".json"), r); err != nil {
					t.Fatal(err)
				}
				cp.Reports[0] = id
			}
			// Deliberately bypass the saver to test the read boundary. Keep all
			// original evidence intact in the original serial directory.
			if err := saveFeedbackCheckpointObject(dir, "mixed-checkpoints", MixedPointer, cp); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadMixedCheckpoint(ctx, dir); err == nil {
				t.Fatal("corrupted mixed provenance accepted")
			}
		})
	}
	id, _ := Digest(a.Learning)
	t.Logf("native modes=1,5; 32 committed games per run; serial vs 2/3-worker interrupted resume model+Adam=%s; full behavior replay and four corruption rejections passed", id)
	verifyMixedExport(t, one)
}
