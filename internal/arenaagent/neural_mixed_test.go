package arenaagent

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func mixedNeuralFixture(t *testing.T) (battlepolicy.Artifact, string) {
	t.Helper()
	base, path := neuralFixtureModel(t, 1)
	a := *base.neural
	a.Schema, a.Modes, a.Experiment = 6, []int{1, 5}, strings.Repeat("a", 64)
	// Numerical loader fixture only, not a measured training/strength claim.
	a.Mixed = &battlepolicy.MixedTraining{Schema: "commander-mixed-training-v1", Checkpoint: strings.Repeat("a", 64), Objective: "mode-weighted-ppo-v1", Batches: 1, Updates: 1,
		Modes: []battlepolicy.MixedModeTraining{
			{Mode: 1, Experiment: strings.Repeat("b", 64), Weight: 1, FamiliesPerBatch: 1, Episodes: 8, TeamTurns: 8, Actions: 16},
			{Mode: 5, Experiment: strings.Repeat("c", 64), Weight: 1, FamiliesPerBatch: 1, Episodes: 8, TeamTurns: 8, Actions: 80},
		}}
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	return a, path
}

func TestMixedNeuralArtifactRoutesOneCommanderAcrossDeclaredModes(t *testing.T) {
	a, path := mixedNeuralFixture(t)
	for _, mode := range []int{1, 5} {
		l, err := NewLearned(path, mode)
		if err != nil || !reflect.DeepEqual(l.neural.Network, a.Network) {
			t.Fatal("mixed loader changed shared weights", mode, err)
		}
		team, history := neuralFixtureTeam(t, mode, 0)
		decision, err := l.Decide(context.Background(), team, history)
		if err != nil || decision.Strategy != "learned" || len(decision.Plan.Orders) != mode*2 {
			t.Fatal("mixed commander failed complete team planning", mode, err)
		}
	}
	if _, err := NewLearned(path, 3); err == nil {
		t.Fatal("undeclared mode silently accepted")
	}
	a.Mixed = nil
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLearned(path, 1); err == nil {
		t.Fatal("schema 6 without mixed declaration fell back to old loader")
	}
}
