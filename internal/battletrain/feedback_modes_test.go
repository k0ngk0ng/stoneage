package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// These deliberately short native fixtures exercise the complete feedback
// workflow in every supported team size. They do not measure playing strength.
func TestNativeFeedbackWorkflowAllModes(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprintf("mode%d", mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			engine, err := battleenv.Start(ctx, command, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			ec := DefaultEvaluationConfig()
			ec.Seed = int64(1423 + mode)
			ec.Mode, ec.MaxTurns = mode, 2
			ec.HealingMagic, ec.HealingItems, ec.ReservePets = 20, 2, 2
			x, err := NewExperiment(ctx, engine.Metadata(), ec, [3]int{1, 1, 1})
			if err != nil {
				t.Fatal(err)
			}
			xid, _ := Digest(x)
			root := t.TempDir()
			// Real warmup/PPO export supplies the behavior artifact and its
			// actual ancestry; no fabricated training receipt enters this test.
			config := DefaultRunConfig()
			config.Mode, config.MaxTurns, config.Experiment = mode, ec.MaxTurns, xid
			config.HealingMagic, config.HealingItems, config.ReservePets = ec.HealingMagic, ec.HealingItems, ec.ReservePets
			config.Network.Width, config.Network.Heads, config.Network.Layers = 8, 2, 1
			config.Warmup.Matches, config.Warmup.Epochs = 8, 1
			config.BatchMatches, config.PPO.Epochs, config.PPO.SequenceLength = 2, 1, 2
			base := filepath.Join(root, "base")
			if err := Run(ctx, RunOptions{Directory: base, Command: command, Config: &config, Experiment: &x, Batches: 1, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			initialPath, err := ExportCandidate(base, "")
			if err != nil {
				t.Fatal(err)
			}
			initial, err := battlepolicy.LoadArtifact(initialPath)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := Digest(initial)
			collection := filepath.Join(root, "collection")
			cfg := FeedbackCollectionConfig{Seed: int64(1451 + mode), Matches: 8, Teacher: "sustain", Opponents: []string{"control"}, Greedy: mode%2 == 0}
			if err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: collection, Command: command, Experiment: &x, Model: &initial, Config: &cfg, Workers: 2, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			collected, _, _, err := LoadFeedbackCollection(ctx, collection)
			if err != nil || len(collected.Games) != 8 {
				t.Fatal("incomplete paired collection", err)
			}
			inputs, err := FeedbackCollectionInputs(collection, collected)
			if err != nil {
				t.Fatal(err)
			}
			update := ImitationConfig{BatchEpisodes: 4, SequenceLength: 2, LearningRate: .001, GradientClip: .5}
			train := filepath.Join(root, "feedback")
			if err := RunFeedback(ctx, FeedbackRunOptions{Directory: train, Datasets: inputs, Initial: &initial, Update: &update, Epochs: 1}); err != nil {
				t.Fatal(err)
			}
			checkpoint, state, examples, err := LoadFeedbackCheckpoint(ctx, train)
			if err != nil || len(examples) != 8 || len(checkpoint.Reports) != 1 || state.Optimizer.Step != 2 {
				t.Fatal("feedback update budget or persisted state differs", err)
			}
			for game, example := range examples {
				frame := example.Source.Steps[0].Frame
				if example.Source.Mode != mode || frame.Mode != mode || len(frame.Members) != mode || len(frame.Slots) != 2*mode || example.Source.Side != game%2 || example.Behavior.Greedy != cfg.Greedy || example.Targets.Teacher != cfg.Teacher {
					t.Fatal("feedback lost whole-team observation, behavior or learner perspective", game)
				}
				if len(example.Targets.Choices[0]) != len(frame.Slots) {
					t.Fatal("teacher omitted a member or pet command")
				}
			}
			modelPath, err := ExportFeedbackCandidate(ctx, train, "", "")
			if err != nil {
				t.Fatal(err)
			}
			model, err := battlepolicy.LoadArtifact(modelPath)
			if err != nil || !reflect.DeepEqual(model.Modes, []int{mode}) || !reflect.DeepEqual(model.Network, state.Model) || model.WeightsDigest == initial.WeightsDigest {
				t.Fatal("export did not retain updated weights and exact trained mode", err)
			}
			reportPath := filepath.Join(root, "validation.json")
			report, err := EvaluateRecorded(ctx, engine, model, []Opponent{{Name: "basic", Rule: "basic"}}, ec, &x, "validation", reportPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := VerifyEvaluation(ctx, reportPath)
			if err != nil || len(verified.Games) != 8 || !reflect.DeepEqual(report, verified) {
				t.Fatal("exported mode could not execute verified held-out evaluation", err)
			}
			after, _ := Digest(initial)
			if before != after {
				t.Fatal("feedback mutated frozen initial model")
			}
			t.Logf("mode=%d greedy=%t collection=8 feedback_updates=2 raw_verified_validation=8; short-cutoff functional fixture, not strength evidence", mode, cfg.Greedy)
		})
	}
}
