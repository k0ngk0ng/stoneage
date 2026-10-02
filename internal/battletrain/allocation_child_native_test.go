package battletrain

import (
	"context"
	"encoding/json"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeAllocationChildEvidence(t *testing.T) {
	fixture, output, raw := os.Getenv("STONEAGE_ALLOCATION_RETAINED_FIXTURE"), os.Getenv("STONEAGE_ALLOCATION_CHILD_OUTPUT"), os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if fixture == "" || output == "" || raw == "" {
		t.Skip("explicit retained source,output,native engine required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	spec, parent, _, _, err := loadAllocationSpec(filepath.Join(fixture, "parent-evaluation.json.data"))
	if err != nil {
		t.Fatal(err)
	}
	x := spec.Validation.Experiment
	c := DefaultRunConfig()
	c.Seed = x.Seed
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = x.Mode, x.Points, x.PetPoints, x.Level, x.MaxTurns
	c.ReservePets, c.HealingItems, c.HealingMagic, c.PetSkillMask = x.ReservePets, x.HealingItems, x.HealingMagic, x.PetSkillMask
	c.Network = parent.Network.Config
	c.Warmup = nil
	c.BatchMatches = 16
	c.PPO.Epochs = 1
	c.Experiment = spec.Validation.ExperimentDigest
	c.InitialModel = x.InitialModel
	training := filepath.Join(output, "child-training")
	if err = Run(ctx, RunOptions{Directory: training, Command: command, Config: &c, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	cp, learning, err := LoadCheckpoint(training)
	if err != nil {
		t.Fatal(err)
	}
	if learning.Optimizer.Step == 0 {
		t.Fatal("no child update")
	}
	seen := map[string]bool{}
	for _, id := range cp.TrainingGroups {
		seen[id] = true
	}
	for _, f := range x.groups("train") {
		if !seen[f.Group] {
			t.Fatal("child did not see both training sources")
		}
	}
	path, err := ExportCandidate(training, "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if child.WeightsDigest == parent.WeightsDigest {
		t.Fatal("child weights unchanged")
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	path = filepath.Join(output, "child-evaluation.json")
	report, err := RunAllocationValidation(ctx, engine, spec.Validation, child, filepath.Join(fixture, "search"), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyAllocationEvaluation(ctx, path, filepath.Join(fixture, "search"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidate == spec.Candidate || verified.Candidate != report.Candidate {
		t.Fatal("child report identity lost")
	}
	planID, _ := Digest(spec.Validation)
	if report.Validation != planID {
		t.Fatal("child used different comparison")
	}
	if len(report.Results) != 2 || len(report.Results[0].Games) != 4 || len(report.Results[1].Games) != 4 {
		t.Fatal("incomplete child schedule")
	}
	t.Logf("mode=%d child collected16training games,updated weights;8supplementary games verified under exact parent comparison;strength not measured", x.Mode)
}
