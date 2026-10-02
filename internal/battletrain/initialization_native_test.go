package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestNativePolicyInitializationResume(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit cached native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	parent := experimentCandidate(t, experimentFixture(t))
	parent.Environment = meta
	before := mustJSON(t, parent)
	root := os.Getenv("STONEAGE_INITIALIZATION_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("retain prior fixtures; use a new root", err)
	}
	for _, mode := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("mode%d", mode), func(t *testing.T) {
			e := DefaultEvaluationConfig()
			e.Mode, e.Points, e.PetPoints, e.Level, e.MaxTurns = mode, 20, 20, 10, 4
			if mode == 1 {
				e.MaxTurns = 100
			}
			x, err := NewExperimentFromSources(ctx, meta, e, [3]int{2, 1, 1}, nil, &parent)
			if err != nil {
				t.Fatal(err)
			}
			c := DefaultRunConfig()
			c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = e.Mode, e.Points, e.PetPoints, e.Level, e.MaxTurns
			c.Network, c.Warmup, c.BatchMatches, c.InitialPolicyScale = parent.Network.Config, nil, 2, .5
			c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
			c.InitialModel, c.Experiment = x.InitialModel, mustDigest(t, x)
			if mode == 1 {
				c.OpeningRollouts = 2
			}
			serial := filepath.Join(root, fmt.Sprintf("mode%d-serial", mode))
			resumed := filepath.Join(root, fmt.Sprintf("mode%d-resumed", mode))
			stop := errors.New("intentional interruption")
			options := RunOptions{Directory: resumed, Command: command, Config: &c, Experiment: &x, InitialModel: &parent, Batches: 2, Stderr: io.Discard}
			options.Progress = func(p Progress) error {
				if p.Event == "ready" {
					cp, state, err := LoadCheckpoint(resumed)
					if err != nil {
						t.Fatal(err)
					}
					want, err := newInitialLearning(c.Network, c.Seed, &parent, .5)
					if err != nil || !reflect.DeepEqual(state, want) || cp.Schema != 5 || cp.Initialization == "" {
						t.Fatal("wrong ready state", err)
					}
					bad := cp
					bad.Schema = 4
					if bad.Validate() == nil {
						t.Fatal("old schema accepted scaled state")
					}
				}
				if p.Event == "collected" {
					return stop
				}
				return nil
			}
			if err := Run(ctx, options); !errors.Is(err, stop) {
				t.Fatal("collection interrupt missing", err)
			}
			cp, _, err := LoadCheckpoint(resumed)
			if err != nil || cp.NextGame != 1 {
				t.Fatal("partial collection missing", err)
			}
			resume := RunOptions{Directory: resumed, Command: command, Resume: true, Batches: 2, Stderr: io.Discard, Progress: func(p Progress) error {
				if p.CompletedBatches == 1 {
					return stop
				}
				return nil
			}}
			if err := Run(ctx, resume); !errors.Is(err, stop) {
				t.Fatal("update interrupt missing", err)
			}
			resume.Batches, resume.Progress = 1, nil
			if err := Run(ctx, resume); err != nil {
				t.Fatal(err)
			}
			options.Directory, options.Progress = serial, nil
			if err := Run(ctx, options); err != nil {
				t.Fatal(err)
			}
			a, sa, err := LoadCheckpoint(serial)
			if err != nil {
				t.Fatal(err)
			}
			b, sb, err := LoadCheckpoint(resumed)
			// Engine process/session IDs are deliberately unique. Their raw
			// observations and receipt hashes differ across runs; learned
			// numerics, schedule, ancestry and committed progress must not.
			if err != nil || !reflect.DeepEqual(sa, sb) || !reflect.DeepEqual(a.Config, b.Config) || a.Initialization != b.Initialization || a.NextGame != b.NextGame || a.CompletedBatches != b.CompletedBatches || !reflect.DeepEqual(a.TrainingGroups, b.TrainingGroups) || !reflect.DeepEqual(a.Opponents, b.Opponents) || !reflect.DeepEqual(a.LastReport, b.LastReport) {
				t.Fatal("resume changed weights, Adam, schedule or provenance", err)
			}
			path, err := ExportCandidate(resumed, "")
			if err != nil {
				t.Fatal(err)
			}
			child, err := battlepolicy.LoadArtifact(path)
			if err != nil || !reflect.DeepEqual(child.Network, sb.Model) || child.Parent != c.InitialModel {
				t.Fatal("inference export changed weights/parent", err)
			}
			var report struct {
				Checkpoint     Checkpoint            `json:"checkpoint"`
				Initialization *PolicyInitialization `json:"initialization"`
			}
			data, err := os.ReadFile(filepath.Join(resumed, "reports", child.TrainingReport+".json"))
			if err != nil || json.Unmarshal(data, &report) != nil || report.Initialization == nil || report.Initialization.Scale != .5 {
				t.Fatal("missing export initialization provenance", err)
			}
			// Rehash a consistent different recipe and receipt, but retain the
			// actual .5-sampled trajectories. Restore must reject their identity.
			bad := a
			bad.Config.InitialPolicyScale = .25
			initial, err := newInitialLearning(c.Network, c.Seed, &parent, .25)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := saveLearning(filepath.Join(serial, "learning"), initial); err != nil {
				t.Fatal(err)
			}
			bad.Initialization, err = savePolicyInitialization(serial, bad.Config, &parent, .25, initial)
			if err != nil {
				t.Fatal(err)
			}
			id := mustDigest(t, bad)
			if err := writeObject(filepath.Join(serial, "checkpoints", id+".json"), bad); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadCheckpointID(serial, id); err == nil || !strings.Contains(err.Error(), "initial policy data") {
				t.Fatal("rehashed transform was not rejected at the sample identity boundary", err)
			}
			t.Logf("mode=%d initialization=%s full resumed learning=%s", mode, a.Initialization, mustDigest(t, sa))
		})
	}
	if string(before) != string(mustJSON(t, parent)) {
		t.Fatal("native training mutated parent")
	}
}

func mustDigest(t *testing.T, value any) string {
	t.Helper()
	id, err := Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNativeMixedPolicyInitializationResume(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit cached native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	parent := experimentCandidate(t, experimentFixture(t))
	parent.Environment = meta
	before := mustJSON(t, parent)
	var parts []MixedExperimentPart
	for _, mode := range []int{1, 5} {
		e := DefaultEvaluationConfig()
		e.Mode, e.MaxTurns, e.PetPoints, e.Seed = mode, 3, 0, int64(401+mode)
		x, err := NewExperimentFromSources(ctx, meta, e, [3]int{3, 2, 2}, nil, &parent)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
	}
	x, err := NewMixedExperiment(testModeMixture(), parts, &parent)
	if err != nil {
		t.Fatal(err)
	}
	c := mixedRunConfigFixture(t, x)
	c.Schema, c.InitialPolicyScale = "commander-mixed-run-v2", .5
	root := os.Getenv("STONEAGE_INITIALIZATION_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	one, two := filepath.Join(root, "serial"), filepath.Join(root, "resumed")
	opts := MixedRunOptions{Directory: one, Config: &c, Experiment: &x, InitialModel: &parent, Command: command, Batches: 2, Workers: 1, Stderr: io.Discard}
	if err := RunMixed(ctx, opts); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("interrupt mixed initialization fixture")
	opts.Directory, opts.Workers = two, 2
	opts.Progress = func(p MixedProgress) error {
		if p.Games == 9 {
			return stop
		}
		return nil
	}
	if err := RunMixed(ctx, opts); !errors.Is(err, stop) {
		t.Fatal("missing partial interrupt", err)
	}
	partial, err := LoadMixedCheckpoint(ctx, two)
	if err != nil || partial.Checkpoint.NextGame != 9 || partial.Learning.Optimizer.Step != 0 || partial.Checkpoint.Schema != "commander-mixed-checkpoint-v2" {
		t.Fatal("wrong partial state", err)
	}
	resume := MixedRunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Workers: 2, Stderr: io.Discard, Progress: func(p MixedProgress) error {
		if p.Event == "mixed_batch_committed" {
			return stop
		}
		return nil
	}}
	if err := RunMixed(ctx, resume); !errors.Is(err, stop) {
		t.Fatal("missing update interrupt", err)
	}
	resume.Batches, resume.Progress = 1, nil
	if err := RunMixed(ctx, resume); err != nil {
		t.Fatal(err)
	}
	a, err := LoadMixedCheckpoint(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadMixedCheckpoint(ctx, two)
	if err != nil || !reflect.DeepEqual(a.Learning, b.Learning) || !reflect.DeepEqual(a.History, b.History) || !reflect.DeepEqual(a.TrainingGroups, b.TrainingGroups) || a.Checkpoint.Initialization != b.Checkpoint.Initialization {
		t.Fatal("mixed resumed weights/Adam/provenance diverged", err)
	}
	path, err := ExportMixedCandidate(ctx, two, "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := battlepolicy.LoadArtifact(path)
	if err != nil || !reflect.DeepEqual(child.Network, b.Learning.Model) {
		t.Fatal("mixed inference export changed", err)
	}
	var report MixedExportReport
	if err := readObject(filepath.Join(two, "reports", child.TrainingReport+".json"), &report, mixedExperimentBytes+(8<<20)); err != nil {
		t.Fatal(err)
	}
	if report.Schema != "commander-mixed-export-v2" || report.Initialization == nil || report.Initialization.Scale != .5 || report.Initialization.Learning != b.Checkpoint.Start {
		t.Fatal("mixed export lost transformed provenance")
	}
	bad := b.Checkpoint
	bad.Schema = "commander-mixed-checkpoint-v1"
	if bad.validate(x) == nil {
		t.Fatal("old schema accepted initialization")
	}
	unscaled, err := newInitialLearning(c.Network, c.Seed, &parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveLearning(filepath.Join(one, "learning"), unscaled); err != nil {
		t.Fatal(err)
	}
	bad = a.Checkpoint
	bad.Start = mustDigest(t, unscaled)
	badID := mustDigest(t, bad)
	if err := writeObject(filepath.Join(one, "mixed-checkpoints", badID+".json"), bad); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMixedCheckpointID(ctx, one, badID); err == nil || !strings.Contains(err.Error(), "initialization receipt differs from initial learning") {
		t.Fatal("unscaled mixed start was not rejected at the receipt boundary", err)
	}
	if string(before) != string(mustJSON(t, parent)) {
		t.Fatal("mixed run changed parent")
	}
	t.Logf("mixed modes1,5; 32 matches per run; full resumed learning=%s", mustDigest(t, a.Learning))
}
