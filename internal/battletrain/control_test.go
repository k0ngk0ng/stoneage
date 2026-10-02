package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestControlIsExplicitWithoutChangingDefaults(t *testing.T) {
	want := []string{"basic", "focus", "guard-break", "defensive", "sustain"}
	c := DefaultRunConfig()
	if !reflect.DeepEqual(c.ruleOpponents(), want) || !reflect.DeepEqual(c.Warmup.Teachers, want) {
		t.Fatal("experimental teacher changed default benchmark")
	}
	c.RuleOpponents, c.Warmup.Teachers = []string{"control"}, []string{"control"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	w := DefaultWarmupConfig()
	w.Teachers = battlepolicy.RuleNames()
	if err := w.validate(); err != nil {
		t.Fatal("explicit teachers rejected", err)
	}
	ids := map[string]bool{}
	for _, name := range battlepolicy.RuleNames() {
		id, err := (Policy{Rule: name}).Version()
		if err != nil || ids[id] {
			t.Fatal("rule identity collision", name, err)
		}
		ids[id] = true
	}
}

func TestNativeControlTeacherEffectsAndImitation(t *testing.T) {
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
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mode := range []int{1, 2, 3, 4, 5} {
		var episodes []Trajectory
		attempts, effects := 0, 0
		statuses := map[int]int{}
		for game, opponent := range []string{"basic", "focus", "sustain"} {
			c := DefaultRunConfig()
			c.Mode, c.HealingMagic, c.HealingItems, c.ReservePets, c.MaxTurns = mode, 20, 2, 2, 200
			s, group := scenarioFor(c, uint64(game*8+mode*32))
			tr, err := CollectPolicies(ctx, engine, s, [2]Policy{{Rule: "control"}, {Rule: opponent}}, [2]*rand.Rand{}, group, engine.Metadata().Rules)
			if err != nil {
				t.Fatal(err)
			}
			if !tr[0].Terminated {
				t.Fatal("control match did not finish", mode, game)
			}
			batches := []aigame.BattleEventBatch{tr[0].FinalHistory}
			for _, step := range tr[0].Steps {
				batches = append(batches, step.History)
				for i, j := range step.Choices {
					if step.Frame.Slots[i].Candidates[j].Features[15] == 1 {
						attempts++
					}
				}
			}
			for _, batch := range batches {
				for _, event := range batch.Events {
					for _, fx := range event.Effects {
						// Opponents do not use status skills. Count actual public status
						// applications to the enemy, including terminal-turn effects.
						if fx.Kind == "BM" && fx.Status != nil && *fx.Status > 0 && fx.Target >= 10 && fx.Target < 20 {
							effects++
							statuses[*fx.Status]++
						}
					}
				}
			}
			episodes = append(episodes, tr[0])
		}
		if attempts == 0 || effects == 0 {
			t.Fatal("no real control coverage", mode, attempts, effects)
		}
		model, adam := testModel(t), &battlenet.Adam[float32]{}
		config := DefaultWarmupConfig().Update
		config.SequenceLength = 4
		report, err := Imitate(ctx, model, adam, episodes, config)
		if err != nil || report.AfterPolicy == report.BeforePolicy {
			t.Fatal("control imitation failed", err)
		}
		before, _ := ModelDigest(model)
		step := adam.Step
		if _, err := Train(ctx, model, adam, episodes, DefaultPPOConfig()); err == nil {
			t.Fatal("rule trajectories entered on-policy PPO")
		}
		after, _ := ModelDigest(model)
		if before != after || adam.Step != step {
			t.Fatal("rejected PPO mutated learning state")
		}
		t.Logf("mode=%d attempts=%d public_statuses=%d kinds=%v imitation_actions=%d", mode, attempts, effects, statuses, report.Actions)
	}
}

func TestNativeControlPoolResume(t *testing.T) {
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
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.MaxTurns, c.BatchMatches, c.PPO.Epochs, c.PPO.SequenceLength = 6, 8, 1, 2
	c.Warmup.Matches, c.Warmup.Epochs, c.Warmup.Update.SequenceLength = 8, 1, 2
	c.Warmup.Teachers, c.RuleOpponents = []string{"control"}, []string{"control"}
	one, two := t.TempDir(), t.TempDir()
	if err := Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("after control warmup")
	err := Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "warmup_trained" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal("interruption not reached", err)
	}
	if err := Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	a, sa, err := LoadCheckpoint(one)
	if err != nil {
		t.Fatal(err)
	}
	b, sb, err := LoadCheckpoint(two)
	if err != nil {
		t.Fatal(err)
	}
	// Raw observation streams have per-session identities. Compare learning,
	// scheduling and reports rather than hashes of independently captured logs.
	if !reflect.DeepEqual(sa, sb) || !reflect.DeepEqual(a.Config, b.Config) ||
		!reflect.DeepEqual(a.TrainingGroups, b.TrainingGroups) || !reflect.DeepEqual(a.Opponents, b.Opponents) ||
		!reflect.DeepEqual(a.OpponentScores, b.OpponentScores) || !reflect.DeepEqual(a.LastReport, b.LastReport) ||
		!reflect.DeepEqual(a.Warmup.LastReport, b.Warmup.LastReport) || b.CompletedBatches != 1 || b.NextGame != 8 || b.Warmup.CompletedEpochs != 1 {
		t.Fatal("explicit control pool changed after resume")
	}
	for _, dir := range []string{one, two} {
		if _, err := InspectLeague(ctx, dir); err != nil {
			t.Fatal("saved rule pool evidence failed verification", err)
		}
	}
}
