package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestModeTransferPreservesParentAndInferenceBoundary(t *testing.T) {
	parent := experimentCandidate(t, experimentFixture(t))
	before, _ := json.Marshal(parent)
	for _, mode := range []int{2, 3, 4, 5} {
		c := DefaultEvaluationConfig()
		c.Mode = mode
		x, err := NewExperimentFromSources(context.Background(), parent.Environment, c, [3]int{2, 1, 1}, nil, &parent)
		if err != nil {
			t.Fatal("transfer initialization rejected", mode, err)
		}
		if err := x.validateInitialModel(&parent); err != nil {
			t.Fatal(err)
		}
		if supports(parent, mode) || x.Mode != mode {
			t.Fatal("transfer granted parent a new inference mode")
		}
		if !reflect.DeepEqual(x.InitialTrainingGroups, parent.TrainingGroups) ||
			!reflect.DeepEqual(x.SelectionGroups, sortedUnion(parent.SelectionGroups, parent.HeldoutGroups)) {
			t.Fatal("transfer lost source exclusions")
		}
		unbound := parent
		unbound.Experiment, unbound.HeldoutGroups = "", nil
		if _, err := prepareEvaluation(context.Background(), parent.Environment, unbound, []Opponent{{Name: "basic", Rule: "basic"}}, c, nil, ""); err == nil || !strings.Contains(err.Error(), "mode incompatible") {
			t.Fatal("untrained parent admitted to target-mode evaluation")
		}
		bad := parent
		bad.Modes = []int{mode}
		if x.validateInitialModel(&bad) == nil {
			t.Fatal("relabeling parent modes accepted")
		}
		bad = parent
		bad.Environment.Rules = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		if _, err := NewExperimentFromSources(context.Background(), bad.Environment, c, [3]int{2, 1, 1}, nil, &parent); err == nil {
			t.Fatal("transfer bypassed rule compatibility")
		}
	}
	after, _ := json.Marshal(parent)
	if string(before) != string(after) {
		t.Fatal("transfer changed parent artifact")
	}
}

func TestNativeModeTransferTrainingResumeAndExport(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base := DefaultRunConfig()
	base.Network = testModel(t).Config
	base.Warmup = nil
	base.Points, base.PetPoints, base.Level, base.MaxTurns, base.BatchMatches = 20, 20, 10, 4, 2
	base.PPO.Epochs, base.PPO.SequenceLength = 1, 2
	parentRoot := t.TempDir()
	if err := Run(ctx, RunOptions{Directory: parentRoot, Command: command, Config: &base, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	path, err := ExportCandidate(parentRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(parent)
	for _, mode := range []int{2, 3, 4, 5} {
		c := DefaultEvaluationConfig()
		c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = mode, 20, 20, 10, 4
		x, err := NewExperimentFromSources(ctx, parent.Environment, c, [3]int{2, 1, 1}, nil, &parent)
		if err != nil {
			t.Fatal(err)
		}
		config := base
		config.Mode, config.InitialModel = mode, x.InitialModel
		config.Experiment, _ = Digest(x)
		one, two := t.TempDir(), t.TempDir()
		stop := errors.New("stop after committed transfer game")
		err = Run(ctx, RunOptions{Directory: one, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
			if p.Event == "ready" {
				_, state, err := LoadCheckpoint(one)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := ModelDigest(state.Model)
				if id != parent.WeightsDigest || state.Optimizer.Step != 0 {
					t.Fatal("did not transfer exact weights with fresh optimizer")
				}
			}
			if p.Event == "collected" {
				return stop
			}
			return nil
		}})
		if !errors.Is(err, stop) {
			t.Fatal("transfer did not reach collection", err)
		}
		if err := Run(ctx, RunOptions{Directory: one, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, RunOptions{Directory: two, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard}); err != nil {
			t.Fatal(err)
		}
		_, a, err := LoadCheckpoint(one)
		if err != nil {
			t.Fatal(err)
		}
		_, b, err := LoadCheckpoint(two)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("transferred training resume diverged", mode)
		}
		path, err := ExportCandidate(one, "")
		if err != nil {
			t.Fatal(err)
		}
		child, err := battlepolicy.LoadArtifact(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(child.Modes, []int{mode}) || child.Parent != x.InitialModel || child.Status != "candidate" {
			t.Fatal("child inherited untrained inference modes/status")
		}
		if err := x.validateCandidateProvenance(child); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareEvaluation(ctx, parent.Environment, child, []Opponent{{Name: "basic", Rule: "basic"}}, c, &x, "validation"); err != nil {
			t.Fatal("trained target-mode child cannot be evaluated", err)
		}
		old := parent
		old.Experiment = ""
		if _, err := prepareEvaluation(ctx, parent.Environment, old, []Opponent{{Name: "basic", Rule: "basic"}}, c, nil, ""); err == nil || !strings.Contains(err.Error(), "mode incompatible") {
			t.Fatal("parent can run new mode without training")
		}
	}
	after, _ := json.Marshal(parent)
	if string(before) != string(after) {
		t.Fatal("native transfer mutated parent")
	}
}
