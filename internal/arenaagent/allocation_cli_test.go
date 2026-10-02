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

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestAllocationCLIHelpAndBoundaries(t *testing.T) {
	for sub, want := range map[string]string{"init": "from-model", "run": "resume", "verify": "report", "compare": "baseline"} {
		var out bytes.Buffer
		if err := Main(context.Background(), []string{"build-validate", sub, "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), want) {
			t.Fatal(sub, err, out.String())
		}
	}
	for _, args := range [][]string{
		{"build-validate", "wat"}, {"build-validate", "run"}, {"build-validate", "verify", "--search-dir", "unread"}, {"build-validate", "compare", "--search-dir", "unread", "--baseline", "unread"},
		{"build-validate", "init", "--search-dir", "unread", "--experiment", "unread", "--from-model", "unread", "--output", "unused", "--groups", "0"},
		{"build-validate", "run", "--search-dir", "unread", "--mode", "5"},
		{"build-validate", "compare", "--search-dir", "unread", "--seed", "2"},
	} {
		var out bytes.Buffer
		if err := Main(context.Background(), args, "test", &out); err == nil {
			t.Fatal("invalid allocation arguments accepted", args)
		}
	}
}

type allocationStopWriter struct {
	bytes.Buffer
	games int
	stop  error
}

func (w *allocationStopWriter) Write(p []byte) (int, error) {
	n, _ := w.Buffer.Write(p)
	var event struct {
		Event string `json:"event"`
	}
	if json.Unmarshal(p, &event) == nil && event.Event == "allocation_game" {
		w.games++
		if w.games == 5 {
			return n, w.stop
		}
	}
	return n, nil
}

// Uses source/parent/child evidence retained by the native library tests. It
// exercises the actual CLI dispatcher and a fresh interrupted eight-game run.
func TestNativeAllocationCLI(t *testing.T) {
	fixture, childPath, environment, work := os.Getenv("STONEAGE_ALLOCATION_CLI_FIXTURE"), os.Getenv("STONEAGE_ALLOCATION_CLI_CHILD"), os.Getenv("STONEAGE_ALLOCATION_CLI_ENV"), os.Getenv("STONEAGE_ALLOCATION_CLI_OUTPUT")
	if fixture == "" || childPath == "" || environment == "" || work == "" {
		t.Skip("explicit retained native CLI fixture required")
	}
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join(fixture, "parent-evaluation.json.data/spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Validation battletrain.AllocationValidation `json:"validation"`
		Candidate  string                           `json:"candidate_artifact"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	experiment := filepath.Join(work, "experiment.json")
	if _, err = battletrain.SaveExperiment(experiment, spec.Validation.Experiment); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(fixture, "parent-evaluation.json.data/models", spec.Candidate+".json")
	source := filepath.Join(fixture, "search")
	manifest := filepath.Join(work, "manifest.json")
	var out bytes.Buffer
	seedBytes, _ := json.Marshal(spec.Validation.Seed)
	if err = Main(ctx, []string{"build-validate", "init", "--search-dir", source, "--experiment", experiment, "--from-model", parent, "--output", manifest, "--seed", string(seedBytes), "--groups", "1"}, "test", &out); err != nil {
		t.Fatal(err)
	}
	actual, err := battletrain.LoadAllocationValidation(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := battletrain.Digest(actual)
	want, _ := battletrain.Digest(spec.Validation)
	if got != want {
		t.Fatal("CLI rewrote original frozen comparison")
	}
	for _, args := range [][]string{{"build-validate", "verify", "--search-dir", source, "--report", filepath.Join(fixture, "parent-evaluation.json")}, {"build-validate", "compare", "--search-dir", source, "--baseline", filepath.Join(fixture, "parent-evaluation.json"), "--candidate", childPath}} {
		out.Reset()
		if err = Main(ctx, args, "test", &out); err != nil {
			t.Fatal(err)
		}
		var event Object
		if err = json.Unmarshal(out.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["arena_certified"] != false {
			t.Fatal("supplementary evidence certified")
		}
		if args[1] == "compare" {
			var result battletrain.AllocationComparison
			if err = json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.CensoredGames == 0 || len(result.Effects) != 5 {
				t.Fatal("fixture cutoff missing")
			}
			for _, row := range result.Effects {
				if row.Delta != nil || row.CI95 != nil || row.IntervalUnavailable != "censored_outcomes" {
					t.Fatal("CLI claimed improvement from cutoffs")
				}
			}
		}
	}
	report := filepath.Join(work, "cli-parent.json")
	args := []string{"build-validate", "run", "--search-dir", source, "--manifest", manifest, "--model", parent, "--environment", environment, "--output", report}
	stop := errors.New("CLI interruption after five games")
	writer := &allocationStopWriter{stop: stop}
	if err = Main(ctx, args, "test", writer); !errors.Is(err, stop) || writer.games != 5 {
		t.Fatal("CLI interruption lost", writer.games, err)
	}
	if _, err = os.Stat(report); !os.IsNotExist(err) {
		t.Fatal("partial CLI report published")
	}
	out.Reset()
	if err = Main(ctx, append(args, "--resume"), "test", &out); err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	newGames, restored, complete := 0, 0, false
	for {
		var event Object
		err = d.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch event["event"] {
		case "allocation_game":
			newGames++
		case "allocation_ready":
			restored = int(event["restored_games"].(float64))
		case "allocation_evaluation_complete":
			complete = true
		}
	}
	if newGames != 3 || restored != 5 || !complete {
		t.Fatal("CLI resume counts", newGames, restored, complete)
	}
	if _, err = battletrain.VerifyAllocationEvaluation(ctx, report, source); err != nil {
		t.Fatal(err)
	}
	t.Logf("mode=%d CLI init/verify/compare/run/resume passed;5 reused3 new,cutoff effects withheld", spec.Validation.Experiment.Mode)
}
