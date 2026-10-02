package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestNativeIndependentControlCollectionAndImitation(t *testing.T) {
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
	policy := Policy{Rule: "independent-control"}
	version, err := policy.Version()
	if err != nil {
		t.Fatal(err)
	}
	central, _ := (Policy{Rule: "control"}).Version()
	if version == central {
		t.Fatal("independent decisions reuse centralized identity")
	}
	for mode := 1; mode <= 5; mode++ {
		config := DefaultRunConfig()
		config.Mode, config.MaxTurns = mode, 60
		config.HealingMagic, config.HealingItems, config.ReservePets = 20, 2, 2
		scenario, group := scenarioFor(config, uint64(mode*32))
		trajectories, err := CollectPolicies(ctx, engine, scenario, [2]Policy{policy, {Rule: "control"}}, [2]*rand.Rand{}, group, engine.Metadata().Rules)
		if err != nil {
			t.Fatal(mode, err)
		}
		directory := t.TempDir()
		manifest, err := SaveShard(directory, trajectories[:])
		if err != nil {
			t.Fatal(err)
		}
		episodes, _, err := LoadShard(directory, manifest.Digest)
		if err != nil {
			t.Fatal("native evidence failed round trip", err)
		}
		originalID, err := Digest(trajectories[:])
		if err != nil {
			t.Fatal(err)
		}
		loadedID, err := Digest(episodes)
		if err != nil || originalID != loadedID {
			t.Fatal("serialized native evidence changed", originalID, loadedID, err)
		}
		if episodes[0].Policy != version || episodes[0].PolicyKind != "rule:independent-control" {
			t.Fatal("independent provenance lost")
		}
		for _, step := range episodes[0].Steps {
			choices, err := battlepolicy.RuleChoices(step.Frame, "independent-control")
			if err != nil || !reflect.DeepEqual(choices, step.Choices) {
				t.Fatal("saved observation does not reproduce decisions", err)
			}
			if mode == 1 {
				central, err := battlepolicy.RuleChoices(step.Frame, "control")
				if err != nil || !reflect.DeepEqual(central, choices) {
					t.Fatal("1v1 rule behavior differs", err)
				}
			}
		}
		model, optimizer := testModel(t), &battlenet.Adam[float32]{}
		imitation := DefaultWarmupConfig().Update
		imitation.SequenceLength = 4
		report, err := Imitate(ctx, model, optimizer, episodes[:1], imitation)
		if err != nil || report.BeforePolicy == report.AfterPolicy {
			t.Fatal("explicit teacher could not train", err)
		}
		before, _ := ModelDigest(model)
		step := optimizer.Step
		if _, err := Train(ctx, model, optimizer, episodes[:1], DefaultPPOConfig()); err == nil {
			t.Fatal("rule data entered on-policy PPO")
		}
		after, _ := ModelDigest(model)
		if before != after || step != optimizer.Step {
			t.Fatal("rejected PPO changed learning state")
		}
		t.Logf("mode=%d turns=%d terminated=%t truncated=%t imitation_actions=%d", mode, len(episodes[0].Steps), episodes[0].Terminated, episodes[0].Truncated, report.Actions)
	}
}
