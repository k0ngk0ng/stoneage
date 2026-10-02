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

func TestNativePlanCountsTrainingResumeGradientAndEvidence(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	for _, scope := range []string{"", "member"} {
		c := DefaultRunConfig()
		c.Network = testModel(t).Config
		c.Network.PlanFeatures = "target-counts-v1"
		c.Network.PlanScope = scope
		c.Mode, c.MaxTurns, c.BatchMatches = 2, 6, 2
		c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
		c.Warmup.Matches, c.Warmup.Epochs = 8, 1
		c.Warmup.Teachers = []string{"control"}
		c.RuleOpponents = []string{"sustain", "control"}
		c.HealingMagic, c.HealingItems, c.ReservePets = 20, 2, 2
		full, resumed := t.TempDir(), t.TempDir()
		for _, run := range []RunOptions{{Directory: full, Config: &c, Batches: 2}, {Directory: resumed, Config: &c, Batches: 1}, {Directory: resumed, Resume: true, Batches: 1}} {
			run.Command, run.Stderr = command, io.Discard
			if err := Run(ctx, run); err != nil {
				t.Fatal(err)
			}
		}
		_, want, err := LoadCheckpoint(full)
		if err != nil {
			t.Fatal(err)
		}
		cp, got, err := LoadCheckpoint(resumed)
		if err != nil || !reflect.DeepEqual(want, got) || cp.Config.Network != c.Network {
			t.Fatal("resume changed complete learning state", scope, err)
		}
		learned := false
		for _, w := range got.Model.Parameters["score.plan.w"].Values {
			learned = learned || w != 0
		}
		if !learned {
			t.Fatal("projection stayed untrained")
		}
		path, err := ExportCandidate(resumed, "")
		if err != nil {
			t.Fatal(err)
		}
		a, err := battlepolicy.LoadArtifact(path)
		if err != nil || a.Architecture != battlepolicy.NetworkArchitecture(c.Network) || a.WeightsDigest != cp.LastReport.CandidatePolicy {
			t.Fatal("export lost contract", err)
		}
		engine, err := battleenv.Start(ctx, command, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		scenario, group := scenarioFor(c, 301)
		pair, err := Collect(ctx, engine, scenario, [2]*battlenet.Model[float32]{a.Network, a.Network}, [2]*rand.Rand{rand.New(rand.NewSource(71)), rand.New(rand.NewSource(73))}, group, engine.Metadata().Rules)
		if err != nil {
			engine.Close()
			t.Fatal(err)
		}
		checkPPOGradient(t, a.Network, pair[:], 2)
		ec := DefaultEvaluationConfig()
		ec.Mode, ec.MaxTurns, ec.MatchesPerOpponent = 2, 6, 8
		ec.HealingMagic, ec.HealingItems, ec.ReservePets = 20, 2, 2
		reportPath := filepath.Join(t.TempDir(), "evaluation.json")
		report, err := EvaluateRecorded(ctx, engine, a, []Opponent{{Name: "control", Rule: "control"}}, ec, nil, "", reportPath, nil)
		engine.Close()
		if err != nil {
			t.Fatal(err)
		}
		verified, err := VerifyEvaluation(ctx, reportPath)
		if err != nil || !reflect.DeepEqual(report, verified) {
			t.Fatal("evidence replay failed", err)
		}
		t.Logf("scope=%q: native imitation/PPO changed counts projection; exact resume; gradient checked; 8 short games fully verified, not strength evidence", scope)
	}
}
