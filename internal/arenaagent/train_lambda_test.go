package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestTrainingLambdaValidationAndFrozenSettings(t *testing.T) {
	trainingPPOFlagValidation(t, "gae-lambda", []string{"-0.1", "1.1", "NaN", "+Inf"})
}

func TestTrainingEntropyValidationAndFrozenSettings(t *testing.T) {
	trainingPPOFlagValidation(t, "entropy-weight", []string{"-0.1", "NaN", "+Inf"})
}

func trainingPPOFlagValidation(t *testing.T, name string, invalid []string) {
	t.Helper()
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"train", "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), name) {
		t.Fatal("missing PPO flag help", name, err)
	}
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	if err := os.WriteFile(environment, []byte(`{"schema_version":1,"command":["must-not-run"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range invalid {
		directory := filepath.Join(root, value)
		err := Main(context.Background(), []string{"train", "--environment", environment, "--data-dir", directory, "--" + name, value}, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "PPO setting") {
			t.Fatal("invalid PPO parameter reached native worker", name, value, err)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid PPO parameter created training directory", name, value, err)
		}
	}
	for _, args := range [][]string{
		{"train", "--resume", "--environment", environment, "--data-dir", root, "--" + name, "1"},
		{"train", "--database", "unread.sqlite", "--" + name, "1"},
	} {
		if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "--"+name) {
			t.Fatal("PPO parameter override wasn't explicitly rejected", args, err)
		}
	}
}

func TestNativeTrainingLambdaCLIAndResume(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	data, err := json.Marshal(struct {
		Schema  int      `json:"schema_version"`
		Command []string `json:"command"`
	}{1, command})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(environment, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name    string
		args    []string
		want    float64
		entropy float64
	}{
		{"default", nil, .95, .01},
		{"one-step", []string{"--gae-lambda", "0"}, 0, .01},
		{"full-return", []string{"--gae-lambda", "1"}, 1, .01},
		{"no-entropy-bonus", []string{"--entropy-weight", "0"}, .95, 0},
		{"entropy-bonus", []string{"--entropy-weight", ".025"}, .95, .025},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := filepath.Join(root, tc.name)
			args := []string{"train", "--environment", environment, "--data-dir", directory,
				"--warmup-matches", "0", "--batch-matches", "2", "--epochs", "1", "--max-turns", "3", "--pet-points", "0"}
			var out bytes.Buffer
			if err := Main(ctx, append(args, tc.args...), "test", &out); err != nil {
				t.Fatal(err)
			}
			before, _, err := battletrain.LoadCheckpoint(directory)
			if err != nil || before.Config.PPO.Lambda != tc.want || before.Config.PPO.EntropyWeight != tc.entropy || before.CompletedBatches != 1 {
				t.Fatal("CLI PPO settings didn't reach actual training", err, before.Config.PPO)
			}
			if err := Main(ctx, []string{"train", "--environment", environment, "--data-dir", directory, "--resume"}, "test", &out); err != nil {
				t.Fatal(err)
			}
			after, _, err := battletrain.LoadCheckpoint(directory)
			if err != nil || after.Config.PPO != before.Config.PPO || after.CompletedBatches != 2 || after.NextGame != 4 {
				t.Fatal("resume changed the stored algorithm or failed to train", err)
			}
		})
	}
}
