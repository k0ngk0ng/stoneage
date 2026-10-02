package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Synthetic scores exercise manifest semantics only. There are deliberately no
// native shards, so this fixture must never count as combat-strength evidence.
func allocationValidationFixture(t *testing.T, mode int) (AllocationValidation, battlepolicy.Artifact) {
	t.Helper()
	ctx := context.Background()
	parent := experimentCandidate(t, experimentFixture(t))
	parent.Modes = []int{mode}
	c := DefaultBuildSearchConfig()
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = mode, 20, 20, 10, 3
	c.ReservePets, c.HealingItems, c.HealingMagic = 1, 1, 20
	c.InitialCandidates, c.Generations, c.NativeCandidates, c.Proposals, c.Finalists, c.FitEpochs = 5, 1, 1, 4, 2, 1
	c.SearchGroups, c.ValidationGroups, c.TestGroups = 1, 1, 1
	spec, _, _, err := newBuildSearchSpec(c, parent.Environment, Opponent{Name: "parent", Model: &parent}, []Opponent{{Name: "basic", Rule: "basic"}, {Name: "sustain", Rule: "sustain"}})
	if err != nil {
		t.Fatal(err)
	}
	s := BuildSearchState{Spec: spec, Stage: "complete", Generation: 1}
	fill := func(r Roster, split int) BuildEvaluation {
		ev := BuildEvaluation{Roster: r}
		enemies := [][]Roster{spec.Search, spec.Validation, spec.Test}[split]
		for _, op := range spec.Opponents {
			for i, enemy := range enemies {
				for repeat := 0; repeat < 4; repeat++ {
					sc := buildScenario(c, r, enemy, i, repeat, split)
					side := repeat % 2
					winner := side
					if r.ID() == balancedRoster(c).ID() {
						winner = 1 - side
					}
					ev.Games = append(ev.Games, BuildGame{EvaluationGame: EvaluationGame{Opponent: op.Name, OpponentPolicy: op.Version, Group: enemy.ID(), Scenario: sc, CandidateSide: side, Turns: 1, Winner: winner, Terminated: true}, Shard: strings.Repeat("e", 64)})
				}
			}
		}
		return ev
	}
	initial, err := initialRosters(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range initial {
		s.Search = append(s.Search, fill(r, 0))
	}
	proposal, err := proposeBuilds(ctx, &s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range proposal {
		s.Search = append(s.Search, fill(r, 0))
	}
	for _, r := range buildFinalists(s) {
		s.Validation = append(s.Validation, fill(r, 1))
	}
	selected := buildFinalSelection(s)
	s.Selected = selected.ID()
	s.Test = []BuildEvaluation{fill(selected, 2), fill(balancedRoster(c), 2)}
	if err = validateBuildSearch(s); err != nil {
		t.Fatal(err)
	}
	sourceID, _ := Digest(s)
	pool := BuildPool{Schema: "commander-build-pool-v1", Environment: spec.Environment, Config: c, SourceState: sourceID, SourcePolicy: spec.Controller}
	groups := append([]string(nil), spec.ExcludedPolicyTrainingGroups...)
	for _, ev := range s.Validation {
		pool.Rosters = append(pool.Rosters, ev.Roster)
	}
	sort.Slice(pool.Rosters, func(i, j int) bool { return pool.Rosters[i].ID() < pool.Rosters[j].ID() })
	for _, list := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		for _, ev := range list {
			for _, g := range ev.Games {
				groups = append(groups, ScenarioGroup(g.Scenario))
			}
		}
	}
	pool.SelectionGroups = sortedUnion(groups)
	ec := DefaultEvaluationConfig()
	ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = mode, 20, 20, 10, 3
	ec.ReservePets, ec.HealingItems, ec.HealingMagic = 1, 1, 20
	x, err := NewExperimentWithPoolMix(ctx, spec.Environment, ec, [3]int{4, 2, 2}, &pool, &parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewAllocationValidation(ctx, x, s, parent, 973, 3)
	if err != nil {
		t.Fatal(err)
	}
	return v, parent
}

func TestAllocationValidationScheduleAndOwnership(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			v, parent := allocationValidationFixture(t, mode)
			games, err := v.Schedule()
			if err != nil {
				t.Fatal(err)
			}
			if len(games) != 2*2*3*4 {
				t.Fatal("incorrect two-allocation schedule", len(games))
			}
			if err = v.ValidateCandidate(parent); err != nil {
				t.Fatal(err)
			}
			priorRosters, priorGroups := map[string]bool{}, map[string]bool{}
			for _, list := range [][]Roster{v.Source.Spec.Search, v.Source.Spec.Validation, v.Source.Spec.Test} {
				for _, r := range list {
					priorRosters[r.ID()] = true
				}
			}
			for _, list := range [][]BuildEvaluation{v.Source.Search, v.Source.Validation, v.Source.Test} {
				for _, evaluation := range list {
					priorRosters[evaluation.Roster.ID()] = true
				}
			}
			for _, f := range v.Experiment.Families {
				priorGroups[f.Group] = true
				for side := 0; side < 2; side++ {
					priorRosters[rosterSide(f.Scenario, side).ID()] = true
				}
			}
			for _, id := range sortedUnion(v.Experiment.InitialTrainingGroups, v.Experiment.SelectionGroups) {
				priorGroups[id] = true
			}
			for _, g := range games {
				if priorRosters[g.OpponentRoster] || priorGroups[ScenarioGroup(g.Scenario)] {
					t.Fatal("comparison reused a known opponent or reserved configuration")
				}
			}
			half := len(games) / 2
			for i, g := range games[:half] {
				other := games[i+half]
				if g.Allocation != "balanced" || other.Allocation != "selected" || g.Opponent != other.Opponent || g.OpponentRoster != other.OpponentRoster || g.Scenario.Seed != other.Scenario.Seed || g.CandidateSide != other.CandidateSide {
					t.Fatal("arms not paired")
				}
				if rosterSide(g.Scenario, g.CandidateSide).ID() != balancedRoster(v.Source.Spec.Config).ID() || rosterSide(other.Scenario, other.CandidateSide).ID() != v.Source.Selected {
					t.Fatal("candidate lost own allocation on side swap")
				}
				if rosterSide(g.Scenario, 1-g.CandidateSide).ID() != g.OpponentRoster || rosterSide(other.Scenario, 1-other.CandidateSide).ID() != g.OpponentRoster {
					t.Fatal("enemy changed between arms")
				}
				if i%2 == 0 {
					next := games[i+1]
					if g.Scenario.Seed != next.Scenario.Seed || g.CandidateSide == next.CandidateSide {
						t.Fatal("seed/side not crossed")
					}
				}
			}
			original, _ := Digest(v)
			again, err := NewAllocationValidation(context.Background(), v.Experiment, v.Source, parent, v.Seed, v.Groups)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := Digest(again)
			if id != original {
				t.Fatal("generation not deterministic")
			}
			again.Source.Search[0].Roster.Players[0][0]++
			again.Experiment.Pool.Rosters[0].Players[0][0]++
			again.OpponentRosters[0].Players[0][0]++
			after, _ := Digest(v)
			if after != original {
				t.Fatal("constructor retained caller aliases")
			}
			path := filepath.Join(t.TempDir(), "allocation.json")
			id, err = SaveAllocationValidation(path, v)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadAllocationValidation(path)
			if err != nil || !reflect.DeepEqual(v, loaded) {
				t.Fatal("manifest round trip", err)
			}
			changed, err := NewAllocationValidation(context.Background(), v.Experiment, v.Source, parent, v.Seed+1, v.Groups)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = SaveAllocationValidation(path, changed); err == nil {
				t.Fatal("changed immutable manifest accepted")
			}
			unchanged, err := LoadAllocationValidation(path)
			if err != nil || !reflect.DeepEqual(unchanged, v) {
				t.Fatal("conflicting save damaged original manifest", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = NewAllocationValidation(ctx, v.Experiment, v.Source, parent, 973, 3); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

func TestAllocationValidationChoiceIgnoresFinalScores(t *testing.T) {
	v, parent := allocationValidationFixture(t, 2)
	selected := v.Source.Selected
	// Reverse every final result so the balanced roster now wins all tests.
	// Its validation score remains worse; no final-result reselection is allowed.
	for i := range v.Source.Test {
		for j := range v.Source.Test[i].Games {
			v.Source.Test[i].Games[j].Winner = 1 - v.Source.Test[i].Games[j].Winner
		}
	}
	v.Experiment.Pool.SourceState, _ = Digest(v.Source)
	got, err := NewAllocationValidation(context.Background(), v.Experiment, v.Source, parent, v.Seed, v.Groups)
	if err != nil {
		t.Fatal(err)
	}
	if got.ownedRosters()[1].ID() != selected {
		t.Fatal("final outcomes changed the validation-selected allocation")
	}
}

func TestAllocationValidationRejectsRelabelingAndSourceLoss(t *testing.T) {
	original, parent := allocationValidationFixture(t, 2)
	clone := func() AllocationValidation {
		raw, _ := json.Marshal(original)
		var v AllocationValidation
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, name := range []string{"schema", "experiment-id", "pool-source", "selection-groups", "finalists", "source-choice", "source-incomplete", "opponent", "negative-count"} {
		t.Run(name, func(t *testing.T) {
			v := clone()
			switch name {
			case "schema":
				v.Schema = "commander-experiment-v1"
			case "experiment-id":
				v.ExperimentDigest = strings.Repeat("9", 64)
			case "pool-source":
				v.Experiment.Pool.SourceState = strings.Repeat("9", 64)
				v.ExperimentDigest, _ = Digest(v.Experiment)
			case "selection-groups":
				v.Experiment.Pool.SelectionGroups = v.Experiment.Pool.SelectionGroups[1:]
				v.ExperimentDigest, _ = Digest(v.Experiment)
			case "finalists":
				v.Experiment.Pool.Rosters[0].Players[0][0]++
				v.ExperimentDigest, _ = Digest(v.Experiment)
			case "source-choice":
				v.Source.Selected = balancedRoster(v.Source.Spec.Config).ID()
				v.Experiment.Pool.SourceState, _ = Digest(v.Source)
				v.ExperimentDigest, _ = Digest(v.Experiment)
			case "source-incomplete":
				v.Source.Stage = "test"
			case "opponent":
				v.OpponentRosters[0] = v.Source.Spec.Test[0]
			case "negative-count":
				v.Groups = -1
			}
			if v.Validate() == nil {
				t.Fatal("invalid comparison accepted")
			}
		})
	}
	child := experimentCandidate(t, original.Experiment)
	child.Parent = original.Experiment.InitialModel
	child.SelectionGroups = original.Experiment.SelectionGroups
	child.TrainingGroups = sortedUnion(original.Experiment.InitialTrainingGroups, child.TrainingGroups)
	if err := original.ValidateCandidate(child); err != nil {
		t.Fatal("valid child rejected", err)
	}
	for _, name := range []string{"foreign-experiment", "dropped-parent", "wrong-mode", "new-training", "changed-parent"} {
		t.Run(name, func(t *testing.T) {
			a := child
			switch name {
			case "foreign-experiment":
				a.Experiment = strings.Repeat("9", 64)
			case "dropped-parent":
				a.Parent = ""
			case "wrong-mode":
				a.Modes = []int{1}
			case "new-training":
				games, _ := original.Schedule()
				a.TrainingGroups = sortedUnion(a.TrainingGroups, []string{ScenarioGroup(games[0].Scenario)})
			case "changed-parent":
				a = parent
				a.TrainingReport = strings.Repeat("9", 64)
			}
			if original.ValidateCandidate(a) == nil {
				t.Fatal("invalid model accepted")
			}
		})
	}
}
