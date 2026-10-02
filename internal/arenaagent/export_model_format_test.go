package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestModelConversionPreservesOnlineDecisions(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		old, source := neuralFixtureModel(t, mode)
		before, _ := os.ReadFile(source)
		output := filepath.Join(t.TempDir(), "model.safetensors")
		var out bytes.Buffer
		if err := Main(context.Background(), []string{"export-model", "--model", source, "--output", output}, "test", &out); err != nil {
			t.Fatal(err)
		}
		converted, err := NewLearned(output, mode)
		if err != nil {
			t.Fatal(err)
		}
		if converted.Version() != old.Version() {
			t.Fatal("format changed policy identity")
		}
		team, history := neuralFixtureTeam(t, mode, 0)
		a, err := old.Decide(context.Background(), team, history)
		if err != nil {
			t.Fatal(err)
		}
		b, err := converted.Decide(context.Background(), team, history)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("inference differs")
		}
		history = append(history, neuralTurnRecord(team, a))
		restored, err := converted.Decide(context.Background(), team, history)
		if err != nil || !reflect.DeepEqual(a.Plan, restored.Plan) {
			t.Fatal("saved plan cannot be recovered after conversion", err)
		}
		after, _ := os.ReadFile(source)
		if !bytes.Equal(before, after) {
			t.Fatal("source modified")
		}
	}
}

func TestModelConversionRejectsConflictingInputs(t *testing.T) {
	_, source := neuralFixtureModel(t, 1)
	for _, args := range [][]string{
		{"--model", source},
		{"--model", source, "--output", source},
		{"--model", source, "--output", "unused.safetensors", "--data-dir", "unused"},
		{"--model", source, "--output", "unused.safetensors", "--checkpoint", "latest"},
	} {
		var out bytes.Buffer
		if err := Main(context.Background(), append([]string{"export-model"}, args...), "test", &out); err == nil {
			t.Fatal("invalid conversion accepted", args)
		}
	}
}
