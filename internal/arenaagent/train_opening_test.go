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

func TestTrainingOpeningFlagsValidateBeforeWorker(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	if e := os.WriteFile(environment, []byte(`{"schema_version":1,"command":["must-not-run"]}`), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{
		{"--opening-rollouts", "0"}, {"--opening-rollouts", "-1"}, {"--opening-rollouts", "65"},
		{"--opening-rollouts", "3"}, {"--policy-advantage", "bad"}, {"--policy-advantage", "opening-loo"},
	} {
		dir := filepath.Join(root, "must-not-create")
		err := Main(context.Background(), append([]string{"train", "--environment", environment, "--data-dir", dir}, args...), "test", &bytes.Buffer{})
		if err == nil || strings.Contains(err.Error(), "must-not-run") {
			t.Fatal("invalid flags reached worker", args, err)
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("invalid config mutated directory", e)
		}
	}
	for _, flag := range []string{"opening-rollouts", "policy-advantage"} {
		value := "2"
		if flag == "policy-advantage" {
			value = "gae"
		}
		var help bytes.Buffer
		if e := Main(context.Background(), []string{"train", "--help"}, "test", &help); e != nil || !strings.Contains(help.String(), flag) {
			t.Fatal("missing help", flag, e)
		}
		for _, args := range [][]string{
			{"train", "--environment", environment, "--data-dir", root, "--resume"},
			{"train", "--database", "unread.sqlite"},
			{"train", "--demonstrations", "unread.jsonl", "--data-dir", root},
		} {
			err := Main(context.Background(), append(args, "--"+flag, value), "test", &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "--"+flag) {
				t.Fatal("incompatible invocation accepted", args, err)
			}
		}
	}
}

func TestNativeTrainingOpeningCLI(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	data, _ := json.Marshal(struct {
		Schema  int      `json:"schema_version"`
		Command []string `json:"command"`
	}{1, command})
	if e := os.WriteFile(environment, data, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, estimator := range []string{"gae", "opening-loo"} {
		dir := filepath.Join(root, estimator)
		args := []string{"train", "--environment", environment, "--data-dir", dir, "--warmup-matches", "0", "--batch-matches", "2", "--opening-rollouts", "2", "--policy-advantage", estimator, "--epochs", "1", "--pet-points", "0"}
		if e := Main(ctx, args, "test", &bytes.Buffer{}); e != nil {
			t.Fatal(e)
		}
		before, _, e := battletrain.LoadCheckpoint(dir)
		if e != nil || before.Schema != 4 || before.CompletedBatches != 1 || before.Config.OpeningRollouts != 2 || len(before.OpeningReports) != 1 {
			t.Fatal("CLI settings missing", e)
		}
		if e := Main(ctx, []string{"train", "--environment", environment, "--data-dir", dir, "--resume"}, "test", &bytes.Buffer{}); e != nil {
			t.Fatal(e)
		}
		after, _, e := battletrain.LoadCheckpoint(dir)
		if e != nil || after.CompletedBatches != 2 || after.NextGame != 4 || after.Config.PolicyAdvantage != before.Config.PolicyAdvantage || len(after.OpeningReports) != 2 {
			t.Fatal("CLI resume changed settings", e)
		}
	}
}
