package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestSimulationOpponentValidationAndContainerArguments(t *testing.T) {
	root := t.TempDir()
	base := Simulation{Root: root, Work: "build/match", Mode: 2, Matches: 2, Strategy: "basic"}
	defaults, err := base.validate()
	if err != nil || defaults.OpponentStrategy != "basic" {
		t.Fatal("default opponent changed", err)
	}
	if strings.Contains(strings.Join(defaults.containerArguments(), " "), "--opponent-") {
		t.Fatal("default basic run requires new worker flags")
	}
	for _, strategy := range []string{"learned", "explore"} {
		s := base
		s.OpponentStrategy = strategy
		if strategy == "learned" {
			s.OpponentModel = "build/opponent.json"
		}
		validated, err := s.validate()
		if err != nil {
			t.Fatal(err)
		}
		args := validated.containerArguments()
		flags := map[string]string{}
		for i, arg := range args[:len(args)-1] {
			if strings.HasPrefix(arg, "--opponent-") {
				flags[arg] = args[i+1]
			}
		}
		if flags["--opponent-strategy"] != strategy || strategy == "learned" && flags["--opponent-model"] != "/repo/build/opponent.json" || strategy == "explore" && flags["--opponent-model"] != "" {
			t.Fatal("explicit opponent lost at container boundary", flags)
		}
	}
	for _, tc := range []struct{ strategy, model string }{
		{"learned", ""}, {"basic", "build/model.json"}, {"explore", "build/model.json"},
		{"llm", ""}, {"learned", "elsewhere.json"}, {"learned", "build/../../outside.json"},
	} {
		s := base
		s.OpponentStrategy, s.OpponentModel = tc.strategy, tc.model
		if _, err := s.validate(); err == nil {
			t.Fatal("ambiguous or unmounted opponent accepted", tc)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "build"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.json", filepath.Join(root, "build/link.json")); err != nil {
		t.Fatal(err)
	}
	base.OpponentStrategy, base.OpponentModel = "learned", "build/link.json"
	if _, err := base.validate(); err == nil {
		t.Fatal("symlinked opponent accepted")
	}
}

func TestSimulationLoadsBothCommanderModelsIndependently(t *testing.T) {
	first, firstPath := neuralFixtureModel(t, 2)
	_, otherPath := neuralFixtureModel(t, 2)
	raw, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact battlepolicy.Artifact
	if err := decode(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.TrainingReport = strings.Repeat("1", 64)
	if err := os.WriteFile(otherPath, enc(artifact), 0600); err != nil {
		t.Fatal(err)
	}
	other, err := NewLearned(otherPath, 2)
	if err != nil || other.Version() == first.Version() {
		t.Fatal("fixture lacks distinct artifact identities", err)
	}
	s := Simulation{Strategy: "learned", Model: firstPath, OpponentStrategy: "learned", OpponentModel: otherPath, Seed: 19}
	for side, want := range []*Learned{first, other} {
		c := Config{Schema: 1, Mode: 2, StateDir: filepath.Join(t.TempDir(), strconv.Itoa(side)), Fallback: "basic"}
		r, err := s.newCommander(side, c)
		if err != nil {
			t.Fatal(err)
		}
		if r.strategy.ID() != "learned" || r.strategy.Version() != want.Version() || r.config.Model != []string{firstPath, otherPath}[side] {
			t.Fatal("opponent became basic or used the other side's model")
		}
		r.Close()
	}
	// Both sides may explore; keep the existing declared seed semantics.
	s.Strategy, s.OpponentStrategy = "explore", "explore"
	for side := 0; side < 2; side++ {
		r, err := s.newCommander(side, Config{Schema: 1, Mode: 2, StateDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		p, ok := r.strategy.(*Explore)
		if !ok || p.Seed != 19 || r.config.Model != "" {
			t.Fatal("exploration selection or seed changed")
		}
		r.Close()
	}
	// A bad second model must fail, not silently use basic.
	s.OpponentStrategy, s.OpponentModel = "learned", firstPath
	if r, err := s.newCommander(1, Config{Schema: 1, Mode: 3, StateDir: t.TempDir()}); err == nil {
		r.Close()
		t.Fatal("wrong-mode opponent silently accepted")
	}
}

func TestSimulationCLIRejectsIncompleteOpponentBeforeStarting(t *testing.T) {
	for _, args := range [][]string{
		{"--opponent-strategy", "learned"},
		{"--opponent-model", "build/model.json"},
		{"--opponent-strategy", "unknown"},
	} {
		root := t.TempDir()
		var out bytes.Buffer
		command := []string{"simulate", "--root", root, "--work", "build/uncreated"}
		if err := Main(context.Background(), append(command, args...), "test", &out); err == nil || !strings.Contains(err.Error(), "opponent") {
			t.Fatal("invalid opponent reached Docker/native startup", err)
		}
		if _, err := os.Stat(filepath.Join(root, "build")); !os.IsNotExist(err) {
			t.Fatal("invalid opponent mutated work directory")
		}
	}
}
