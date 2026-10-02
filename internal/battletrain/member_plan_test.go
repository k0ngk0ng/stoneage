package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestNativeIndependentMemberTrainingResumeAndEvaluation(t *testing.T) {
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
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.Network.PlanScope = "member"
	c.Mode, c.MaxTurns, c.BatchMatches = 2, 6, 2
	c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
	c.Warmup.Matches, c.Warmup.Epochs = 8, 1
	c.Warmup.Teachers = []string{"independent-control"}
	c.RuleOpponents = []string{"control", "independent-control"}
	c.HealingMagic, c.HealingItems, c.ReservePets = 20, 2, 2
	full, resumed := t.TempDir(), t.TempDir()
	if err := Run(ctx, RunOptions{Directory: full, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, RunOptions{Directory: resumed, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, RunOptions{Directory: resumed, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	_, want, err := LoadCheckpoint(full)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, got, err := LoadCheckpoint(resumed)
	if err != nil || !reflect.DeepEqual(want, got) || checkpoint.Config.Network.PlanScope != "member" {
		t.Fatal("independent training/resume changed weights or Adam", err)
	}
	path, err := ExportCandidate(resumed, "")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := battlepolicy.LoadArtifact(path)
	if err != nil || baseline.Architecture != "independent-member-policy-v1" || baseline.Network.Config.PlanScope != "member" || baseline.WeightsDigest != checkpoint.LastReport.CandidatePolicy {
		t.Fatal("export changed independent architecture/provenance", err)
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	scenario, group := scenarioFor(c, 301)
	trajectories, err := Collect(ctx, engine, scenario, [2]*battlenet.Model[float32]{baseline.Network, baseline.Network}, [2]*rand.Rand{rand.New(rand.NewSource(71)), rand.New(rand.NewSource(73))}, group, engine.Metadata().Rules)
	if err != nil {
		t.Fatal(err)
	}
	checkPPOGradient(t, baseline.Network, trajectories[:], 2)
	// A separately initialized/trained commander, never a relabeled baseline.
	c.Network.PlanScope = ""
	commanderRoot := t.TempDir()
	if err := Run(ctx, RunOptions{Directory: commanderRoot, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	commanderPath, err := ExportCandidate(commanderRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	commander, err := battlepolicy.LoadArtifact(commanderPath)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultEvaluationConfig()
	config.Mode, config.MaxTurns, config.MatchesPerOpponent = 2, 6, 8
	config.HealingMagic, config.HealingItems, config.ReservePets = c.HealingMagic, c.HealingItems, c.ReservePets
	reportPath := filepath.Join(t.TempDir(), "independent-opponent.json")
	report, err := EvaluateRecorded(ctx, engine, commander, []Opponent{{Name: "independent-trained", Model: &baseline}}, config, nil, "", reportPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyEvaluation(ctx, reportPath)
	if err != nil || !reflect.DeepEqual(report, verified) || len(verified.Games) != 8 {
		t.Fatal("independent opponent evidence failed replay", err)
	}
	t.Logf("independent warmup=%d PPO=%d; exact resume; 2v2 gradient and 8-game trained-opponent report verified; short cutoffs are not strength evidence", c.Warmup.Matches, checkpoint.NextGame)
}
