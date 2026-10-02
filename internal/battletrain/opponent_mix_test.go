package battletrain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpponentMixKeepsLegacyDrawsAndEncoding(t *testing.T) {
	for _, sampling := range []string{"", "uniform", "weakness-v1"} {
		old := Checkpoint{Config: DefaultRunConfig(), Opponents: []string{"one", "two"}}
		old.Config.OpponentSampling = sampling
		old.OpponentScores = []OpponentScore{{Key: "rule:basic", Losses: 50}, {Key: "history:one", Wins: 50}}
		data, err := json.Marshal(old.Config)
		if err != nil || bytes.Contains(data, []byte("opponent_mix")) {
			t.Fatal("default serialization changed", err)
		}
		var restored RunConfig
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		again, _ := json.Marshal(restored)
		if !bytes.Equal(data, again) {
			t.Fatal("legacy checkpoint no longer round trips")
		}
		explicit := old
		explicit.Config.OpponentMix = &OpponentMix{Rules: 30, History: 50, Self: 20}
		for game := uint64(0); game < 5000; game++ {
			if old.opponentAt(game) != explicit.opponentAt(game) {
				t.Fatal("explicit default changed the seeded schedule", sampling, game)
			}
		}
	}
}

func TestOpponentCategoryMixAndEmptyHistory(t *testing.T) {
	for _, mix := range []OpponentMix{{100, 0, 0}, {0, 100, 0}, {0, 0, 100}, {80, 10, 10}, {37, 42, 21}, {1, 98, 1}} {
		for _, history := range []bool{true, false} {
			c := Checkpoint{Config: DefaultRunConfig()}
			c.Config.OpponentMix = &mix
			if history {
				c.Opponents = []string{"one"}
			}
			counts := [3]int{}
			const games = 20000
			for game := uint64(0); game < games; game++ {
				choice := c.opponentAt(game)
				category := 2
				if strings.HasPrefix(choice.Key, "rule:") {
					category = 0
				} else if strings.HasPrefix(choice.Key, "history:") {
					category = 1
				}
				counts[category]++
				if choice.Side != int(game%2) {
					t.Fatal("mix changed player sides")
				}
			}
			want := [3]int{mix.Rules, mix.History, mix.Self}
			if !history {
				want[2] += want[1]
				want[1] = 0
			}
			for i, percent := range want {
				if percent == 0 && counts[i] != 0 || math.Abs(float64(counts[i])-float64(games*percent)/100) > games*.02 {
					t.Fatal("category mass differs from requested mix", mix, history, counts)
				}
			}
		}
	}
	for _, mix := range []OpponentMix{{}, {-1, 51, 50}, {101, 0, -1}, {30, 50, 21}, {100, 100, 100}} {
		c := DefaultRunConfig()
		c.OpponentMix = &mix
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "opponent mix") {
			t.Fatal("invalid mix accepted", mix, err)
		}
	}
}

func TestNativeExplicitDefaultMixKeepsLearningState(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Network, c.Warmup = testModel(t).Config, nil
	c.BatchMatches, c.MaxTurns, c.PetPoints, c.PPO.Epochs = 4, 3, 0, 1
	var baseline LearningState
	for _, explicit := range []bool{false, true} {
		if explicit {
			c.OpponentMix = &OpponentMix{30, 50, 20}
		}
		dir := t.TempDir()
		if err := Run(ctx, RunOptions{Directory: dir, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); err != nil {
			t.Fatal(err)
		}
		_, state, err := LoadCheckpoint(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !explicit {
			baseline = state
		} else if !reflect.DeepEqual(state, baseline) {
			t.Fatal("explicit default changed actual weights or optimizer")
		}
	}
}
