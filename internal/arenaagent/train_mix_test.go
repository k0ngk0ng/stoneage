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

func TestTrainingOpponentMixRejectsInvalidAndOverrides(t *testing.T) {
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"train", "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "opponent-mix") {
		t.Fatal("missing opponent mix help", err)
	}
	root := t.TempDir()
	for _, value := range []string{"", "30,50", "30,50,20,0", "30.5,49.5,20", "-1,81,20", "80,20,20", "NaN,0,100", "1000000000000000000000000,0,0"} {
		directory := filepath.Join(root, "must-not-exist")
		err := Main(context.Background(), []string{"train", "--environment", "must-not-read", "--data-dir", directory, "--opponent-mix", value}, "test", &out)
		if err == nil || !(strings.Contains(err.Error(), "opponent-mix") || strings.Contains(err.Error(), "opponent mix")) {
			t.Fatal("invalid mix reached environment setup", value, err)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid mix created data directory", err)
		}
	}
	for _, args := range [][]string{
		{"train", "--resume", "--environment", "must-not-read", "--data-dir", root, "--opponent-mix", "30,50,20"},
		{"train", "--database", "must-not-read", "--opponent-mix", "30,50,20"},
	} {
		if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "--opponent-mix") {
			t.Fatal("unsupported override accepted", err)
		}
	}
}

func TestNativeTrainingOpponentMixCLIAndResume(t *testing.T) {
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
	if err := os.WriteFile(environment, enc(Object{"schema_version": 1, "command": command}), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	directory := filepath.Join(root, "training")
	var out bytes.Buffer
	args := []string{"train", "--environment", environment, "--data-dir", directory,
		"--warmup-matches", "0", "--batch-matches", "2", "--epochs", "1", "--max-turns", "3", "--pet-points", "0", "--opponent-mix", "80,10,10"}
	if err := Main(ctx, args, "test", &out); err != nil {
		t.Fatal(err)
	}
	if err := Main(ctx, []string{"train", "--environment", environment, "--data-dir", directory, "--resume"}, "test", &out); err != nil {
		t.Fatal(err)
	}
	checkpoint, _, err := battletrain.LoadCheckpoint(directory)
	want := battletrain.OpponentMix{Rules: 80, History: 10, Self: 10}
	if err != nil || checkpoint.Config.OpponentMix == nil || *checkpoint.Config.OpponentMix != want || checkpoint.CompletedBatches != 2 || checkpoint.NextGame != 4 {
		t.Fatal("CLI mix did not persist through actual collection/learning/resume", err)
	}
	league, err := battletrain.InspectLeague(ctx, directory)
	if err != nil || league.Mix != want || len(league.Reports) != 2 {
		t.Fatal("resumed league lost mix or evidence", league.Mix, err)
	}
}
