package battletrain

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Synthetic complete outcomes test the arithmetic; public comparison separately
// requires both native evidence archives and the completed source search.
func allocationComparisonFixture(t *testing.T, groups int) (AllocationValidation, battlepolicy.Artifact, battlepolicy.Artifact, AllocationEvaluationReport, AllocationEvaluationReport) {
	t.Helper()
	v, parent := allocationValidationFixture(t, 1)
	var err error
	v, err = NewAllocationValidation(context.Background(), v.Experiment, v.Source, parent, 1007, groups)
	if err != nil {
		t.Fatal(err)
	}
	child := experimentCandidate(t, v.Experiment)
	child.Parent = v.Experiment.InitialModel
	child.SelectionGroups = v.Experiment.SelectionGroups
	child.TrainingGroups = sortedUnion(v.Experiment.InitialTrainingGroups, child.TrainingGroups)
	pid, _ := Digest(parent)
	cid, _ := Digest(child)
	vid, _ := Digest(v)
	scheduled, err := v.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	n := len(scheduled) / 2
	reports := [2]AllocationEvaluationReport{}
	for m := range reports {
		reports[m] = AllocationEvaluationReport{Schema: allocationReportSchema, Evidence: strings.Repeat("e", 64), Validation: vid, Candidate: []string{pid, cid}[m], Status: "complete"}
		for ai, name := range allocationNames {
			row := AllocationEvaluationResult{Allocation: name}
			arm := m*2 + ai
			for i, want := range scheduled[ai*n : (ai+1)*n] {
				winner := 1 - want.CandidateSide
				if i%4 < arm {
					winner = want.CandidateSide
				}
				row.Games = append(row.Games, EvaluationGame{Shard: strings.Repeat("d", 64), Opponent: want.Opponent.Name, OpponentPolicy: want.Opponent.Version, Group: ScenarioGroup(want.Scenario), Scenario: want.Scenario, CandidateSide: want.CandidateSide, Turns: 1, Winner: winner, Terminated: true})
			}
			reports[m].Results = append(reports[m].Results, row)
		}
	}
	return v, parent, child, reports[0], reports[1]
}

func TestAllocationContrastsAndClusterIntervals(t *testing.T) {
	v, parent, child, a, b := allocationComparisonFixture(t, 20)
	r, err := compareVerifiedAllocations(context.Background(), v, parent, child, a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{.75, .5, .5, .25, .25}
	if r.Groups != 20 || r.GamesPerArm != 160 || r.AdjustmentComparisons != 5 || r.ArenaCertified {
		t.Fatal("scope changed")
	}
	for i, row := range r.Effects {
		if row.Delta == nil || math.Abs(*row.Delta-want[i]) > 1e-12 || row.CI95 == nil || row.AdjustedInterval == nil {
			t.Fatal("contrast mismatch", row)
		}
		for _, ci := range []*[2]float64{row.CI95, row.AdjustedInterval} {
			for _, end := range ci {
				if math.Abs(end-want[i]) > 1e-12 {
					t.Fatal("constant cluster interval", row)
				}
			}
		}
	}
	again, err := compareVerifiedAllocations(context.Background(), v, parent, child, a, b)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("nondeterministic bootstrap", err)
	}
	// Entire opponent-roster effects vary together across every rule/repeat.
	n := len(a.Results[0].Games)
	for ai := 0; ai < 2; ai++ {
		for i := 0; i < n; i++ {
			group := (i / 4) % 20
			win := group < 10
			ga, gb := &a.Results[ai].Games[i], &b.Results[ai].Games[i]
			ga.Winner, gb.Winner = 1-ga.CandidateSide, gb.CandidateSide
			if win {
				ga.Winner, gb.Winner = ga.CandidateSide, 1-gb.CandidateSide
			}
		}
	}
	r, err = compareVerifiedAllocations(context.Background(), v, parent, child, a, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 2} {
		row := r.Effects[i]
		if math.Abs(*row.Delta) > 1e-12 || row.CI95[0] >= 0 || row.CI95[1] <= 0 || row.AdjustedInterval[0] > row.CI95[0] || row.AdjustedInterval[1] < row.CI95[1] {
			t.Fatal("lost roster-level variation or adjustment", row)
		}
	}
	for _, i := range []int{3, 4} {
		if *r.Effects[i].Delta != 0 || *r.Effects[i].AdjustedInterval != [2]float64{} {
			t.Fatal("identical allocations gained effect")
		}
	}
}

func TestAllocationComparisonCutoffsSmallSamplesAndIdentity(t *testing.T) {
	v, parent, child, a, b := allocationComparisonFixture(t, 3)
	r, err := compareVerifiedAllocations(context.Background(), v, parent, child, a, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Effects {
		if row.Delta == nil || row.CI95 != nil || row.AdjustedInterval != nil || row.IntervalUnavailable != "fewer_than_20_opponent_rosters" {
			t.Fatal("games mistaken for independent rosters")
		}
	}
	b.Results[0].Games[0].Terminated = false
	b.Results[0].Games[0].Truncated = true
	b.Results[0].Games[0].Winner = -1
	b.Status = "incomplete-outcomes"
	r, err = compareVerifiedAllocations(context.Background(), v, parent, child, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.CensoredGames != 1 {
		t.Fatal("cutoff missing")
	}
	for _, row := range r.Effects {
		if row.Delta != nil || row.CI95 != nil || row.IntervalUnavailable != "censored_outcomes" {
			t.Fatal("cutoff used to estimate improvement")
		}
	}
	b.Status = "complete"
	if _, err = compareVerifiedAllocations(context.Background(), v, parent, child, a, b); err == nil {
		t.Fatal("hidden cutoff accepted")
	}
	v, parent, child, a, b = allocationComparisonFixture(t, 3)
	if _, err = compareVerifiedAllocations(context.Background(), v, child, parent, b, a); err == nil {
		t.Fatal("swapped parent/child accepted")
	}
	b.Results[1].Games[0].Scenario.Seed++
	if _, err = compareVerifiedAllocations(context.Background(), v, parent, child, a, b); err == nil {
		t.Fatal("unpaired game accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = compareVerifiedAllocations(ctx, v, parent, child, a, b); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
