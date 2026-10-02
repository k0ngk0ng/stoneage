package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

type stopEvaluationWriter struct{ err error }

func (w stopEvaluationWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":"evaluation_game"`)) {
		return 0, w.err
	}
	return len(p), nil
}

func TestEvaluationResumeCLIBeforeEngineStart(t *testing.T) {
	for _, test := range []struct {
		name            string
		resume, partial bool
		want            string
	}{
		{"implicit-restart", false, true, "--resume"},
		{"missing-evidence", true, false, "existing frozen"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "report.json")
			if test.partial {
				if err := os.MkdirAll(output+".data", 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(output+".data", "spec.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately nonexistent sources: recovery guards must reject
			// before loading an engine command, model, or writing an engine log.
			args := []string{"evaluate", "--environment", "unread-environment.json", "--model", "unread-model.json", "--output", output}
			if test.resume {
				args = append(args, "--resume")
			}
			var out bytes.Buffer
			err := Main(context.Background(), args, "test", &out)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal("late or missing recovery rejection", err)
			}
			if _, err := os.Stat(output + ".engine.log"); !os.IsNotExist(err) {
				t.Fatal("engine started or log created", err)
			}
		})
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"evaluate", "--database", "unread.sqlite", "--model", "unread.json", "--resume"}, "test", &out)
	if err == nil || !strings.Contains(err.Error(), "legacy prediction") {
		t.Fatal("resume accepted by legacy prediction", err)
	}
}

func TestNativeEvaluationResumeCLI(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit isolated native environment required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	training := filepath.Join(root, "training")
	c := battletrain.DefaultRunConfig()
	c.Warmup = nil
	c.Network = battlepolicy.NetworkConfig()
	c.Network.Width, c.Network.Heads, c.Network.Layers = 8, 2, 1
	c.BatchMatches, c.MaxTurns, c.PPO.Epochs = 2, 6, 1
	if err := battletrain.Run(ctx, battletrain.RunOptions{Directory: training, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	model, err := battletrain.ExportCandidate(training, "")
	if err != nil {
		t.Fatal(err)
	}
	environment := filepath.Join(root, "environment.json")
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "command": command})
	if err := os.WriteFile(environment, b, 0600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root, "report.json")
	args := []string{"evaluate", "--environment", environment, "--model", model, "--output", report, "--matches", "8", "--max-turns", "6", "--opponent", "basic"}
	stop := errors.New("intentional output interruption")
	if err := Main(ctx, args, "test", stopEvaluationWriter{stop}); !errors.Is(err, stop) {
		t.Fatal("lost output interruption", err)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatal("partial report published", err)
	}
	first, err := os.ReadFile(filepath.Join(report+".data", "commits", "000000000.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Main(ctx, args, "test", &out); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatal("implicit CLI restart", err)
	}
	out.Reset()
	if err := Main(ctx, append(args, "--resume"), "test", &out); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	newGames, resumed := 0, false
	for dec.More() {
		var event map[string]any
		if err := dec.Decode(&event); err != nil {
			t.Fatal(err)
		}
		switch event["event"] {
		case "evaluation_resumed":
			if resumed || event["reused"] != float64(1) || event["remaining"] != float64(7) || event["total"] != float64(8) || event["executed"] != float64(0) {
				t.Fatal(event)
			}
			resumed = true
		case "evaluation_game":
			newGames++
			if !resumed || event["completed"] != float64(newGames+1) || event["executed"] != float64(newGames) {
				t.Fatal(event)
			}
		}
	}
	if !resumed || newGames != 7 {
		t.Fatal("wrong restored/executed count", resumed, newGames)
	}
	after, err := os.ReadFile(filepath.Join(report+".data", "commits", "000000000.json"))
	if err != nil || !bytes.Equal(first, after) {
		t.Fatal("original commit changed", err)
	}
	verified, err := battletrain.VerifyEvaluation(ctx, report)
	if err != nil || len(verified.Games) != 8 {
		t.Fatal("CLI resume evidence invalid", err)
	}
	t.Log("2 training +8 native evaluation matches; CLI reused1/executed7 and complete independent replay")
}
