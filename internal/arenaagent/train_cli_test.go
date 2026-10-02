package arenaagent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTrainingCLIHelpAndResumeContract(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"train", "--help"}, "test", &out); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"environment", "data-dir", "experiment", "from-model", "resume", "database", "batch-matches", "warmup-matches", "warmup-epochs", "warmup-batch-episodes", "warmup-teacher", "warmup-learning-rate", "rule-opponent"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("missing actionable training help", s)
		}
	}
	for _, args := range [][]string{
		{"train"},
		{"train", "--database", "old.sqlite", "--environment", "engine.json"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--points", "100"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--experiment", "new.json"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--from-model", "new.json"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--warmup-matches", "0"},
		{"train", "--database", "old.sqlite", "--warmup-epochs", "1"},
		{"train", "--database", "old.sqlite", "--warmup-batch-episodes", "8"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--warmup-batch-episodes", "8"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--rule-opponent", "sustain"},
		{"train", "--database", "old.sqlite", "--rule-opponent", "sustain"},
		{"train", "--resume", "--environment", "unread.json", "--data-dir", "unused", "--opponent-sampling", "uniform"},
		{"train", "--database", "old.sqlite", "--opponent-sampling", "weakness-v1"},
		{"train", "extra"},
	} {
		if e := Main(context.Background(), args, "test", &out); e == nil {
			t.Fatal("invalid/ambiguous training invocation accepted", args)
		}
	}
}

func TestReserveCLIHelpAndFrozenSettings(t *testing.T) {
	for _, cmd := range []string{"train", "evaluate", "experiment", "build-search"} {
		var out bytes.Buffer
		if err := Main(context.Background(), []string{cmd, "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "reserve-pets") {
			t.Fatal("missing reserve help", cmd, err)
		}
	}
	for _, args := range [][]string{
		{"train", "--environment", "unread.json", "--data-dir", "unused", "--resume", "--reserve-pets", "2"},
		{"build-search", "--environment", "unread.json", "--data-dir", "unused", "--resume", "--reserve-pets", "2"},
		{"evaluate", "--experiment", "unread.json", "--reserve-pets", "2"},
		{"experiment", "--environment", "unread.json", "--output", "unused.json", "--build-pool", "unread.json", "--reserve-pets", "2"},
		{"train", "--database", "unread.sqlite", "--reserve-pets", "2"},
	} {
		var out bytes.Buffer
		err := Main(context.Background(), args, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "--reserve-pets") {
			t.Fatal("reserve override not rejected before opening data", args, err)
		}
	}
}

func TestEvaluationCLIHelpAndBoundaries(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"evaluate", "--help"}, "test", &out); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"environment", "opponent-model", "matches", "output", "database", "experiment", "split"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("missing evaluation help", s)
		}
	}
	for _, args := range [][]string{{"evaluate"}, {"evaluate", "--database", "old.sqlite", "--opponent", "basic"}, {"evaluate", "extra"}, {"evaluate", "--split", "test"}, {"evaluate", "--experiment", "unread.json", "--seed", "1"}, {"evaluate", "--experiment", "unread.json", "--split", "train"}, {"evaluate", "--experiment", "unread.json", "--matches", "4"}} {
		if e := Main(context.Background(), args, "test", &out); e == nil {
			t.Fatal("ambiguous evaluation accepted", args)
		}
	}
}

func TestExperimentCLIHelp(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"experiment", "--help"}, "test", &out); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"train-groups", "validation-groups", "test-groups", "environment", "output", "mode", "build-pool", "from-model"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("missing experiment help", s)
		}
	}
	if e := Main(context.Background(), []string{"experiment"}, "test", &out); e == nil {
		t.Fatal("experiment started without an environment/output")
	}
}

func TestModelExportAndBuildPoolCLI(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"export-model", "--data-dir", t.TempDir(), "--checkpoint", "../latest"}, "test", &out); e == nil || !strings.Contains(e.Error(), "invalid checkpoint digest") {
		t.Fatal("explicit export checkpoint did not reach validated local selector", e)
	}
	for _, command := range []string{"export-model", "build-pool", "league", "verify-evaluation"} {
		if e := Main(context.Background(), []string{command, "--help"}, "test", &out); e != nil {
			t.Fatal(e)
		}
		if e := Main(context.Background(), []string{command}, "test", &out); e == nil {
			t.Fatal("missing source accepted", command)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := Main(ctx, []string{"export-model", "--data-dir", t.TempDir()}, "test", &out); e != context.Canceled {
		t.Fatal("canceled export attempted filesystem access", e)
	}
}

func TestChampionCLIContracts(t *testing.T) {
	for _, command := range []string{"init", "challenge", "status", "rollback", "abandon"} {
		var out bytes.Buffer
		if e := Main(context.Background(), []string{"champion", command, "--help"}, "test", &out); e != nil || !strings.Contains(out.String(), "directory") {
			t.Fatal(command, e)
		}
		if e := Main(context.Background(), []string{"champion", command}, "test", &out); e == nil {
			t.Fatal("missing directory accepted", command)
		}
	}
	for _, args := range [][]string{
		{"champion", "unknown"},
		{"champion", "challenge", "--directory", "unread", "--min-groups", "1"},
		{"champion", "challenge", "--directory", "unread"},
		{"champion", "rollback", "--directory", "unread", "--to", "id"},
		{"champion", "abandon", "--directory", "unread"},
		{"champion", "init", "--directory", "unread"},
	} {
		var out bytes.Buffer
		if e := Main(context.Background(), args, "test", &out); e == nil {
			t.Fatal("ambiguous champion invocation accepted", args)
		}
	}
}

func TestBuildSearchCLIContract(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"build-search", "--help"}, "test", &out); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"initial-candidates", "native-candidates", "search-groups", "validation-groups", "test-groups", "resume", "policy", "model"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("missing build search help", s)
		}
	}
	for _, args := range [][]string{
		{"build-search"},
		{"build-search", "--environment", "unread.json", "--data-dir", "unused", "--resume", "--model", "changed.json"},
		{"build-search", "--environment", "unread.json", "--data-dir", "unused", "--policy", "basic", "--model", "changed.json"},
		{"build-search", "--environment", "unread.json", "--data-dir", "unused", "--points", "3"},
	} {
		if e := Main(context.Background(), args, "test", &out); e == nil {
			t.Fatal("ambiguous/invalid search accepted", args)
		}
	}
}
