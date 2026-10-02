package battletrain

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type AllocationEffect struct {
	Name                string      `json:"name"`
	Delta               *float64    `json:"delta,omitempty"`
	CI95                *[2]float64 `json:"ci95,omitempty"`
	AdjustedInterval    *[2]float64 `json:"bonferroni_bootstrap_interval,omitempty"`
	IntervalUnavailable string      `json:"interval_unavailable,omitempty"`
}
type AllocationComparison struct {
	Schema                    string             `json:"schema"`
	Method                    string             `json:"method"`
	Validation                string             `json:"validation"`
	ParentReport              string             `json:"parent_report"`
	ChildReport               string             `json:"child_report"`
	ParentArtifact            string             `json:"parent_artifact"`
	ChildArtifact             string             `json:"child_artifact"`
	Groups                    int                `json:"opponent_roster_groups"`
	GamesPerArm               int                `json:"games_per_arm"`
	CensoredGames             int                `json:"censored_games"`
	BootstrapSamples          int                `json:"bootstrap_samples"`
	BootstrapSeed             int64              `json:"bootstrap_seed"`
	NominalAlpha              float64            `json:"nominal_alpha"`
	AdjustmentComparisons     int                `json:"adjustment_comparisons"`
	AdjustmentScope           string             `json:"adjustment_scope"`
	ArenaCertified            bool               `json:"arena_certified"`
	UnverifiedSourceArtifacts []string           `json:"unverified_source_artifacts,omitempty"`
	Effects                   []AllocationEffect `json:"effects"`
}

// CompareAllocationEvaluations is read-only supplementary validation, not an
// automatic promotion or final-test gate. All five contrasts and the bootstrap
// recipe are fixed by this format; users cannot select favorable rows after play.
func CompareAllocationEvaluations(ctx context.Context, parentPath, childPath, sourceRoot string) (AllocationComparison, error) {
	var empty AllocationComparison
	a, err := VerifyAllocationEvaluation(ctx, parentPath, sourceRoot)
	if err != nil {
		return empty, fmt.Errorf("parent evidence: %w", err)
	}
	b, err := VerifyAllocationEvaluation(ctx, childPath, sourceRoot)
	if err != nil {
		return empty, fmt.Errorf("child evidence: %w", err)
	}
	as, parent, ops, _, err := loadAllocationSpec(parentPath + ".data")
	if err != nil {
		return empty, err
	}
	bs, child, _, _, err := loadAllocationSpec(childPath + ".data")
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(as.Validation, bs.Validation) {
		return empty, fmt.Errorf("allocation comparisons require the same complete manifest")
	}
	r, err := compareVerifiedAllocations(ctx, as.Validation, parent, child, a, b)
	if err != nil {
		return r, err
	}
	for _, model := range []battlepolicy.Artifact{parent, child} {
		if model.HasRecordedSource() {
			id, _ := Digest(model)
			r.UnverifiedSourceArtifacts = sortedUnion(r.UnverifiedSourceArtifacts, []string{id})
		}
	}
	for _, o := range ops {
		if o.Model != nil && o.Model.HasRecordedSource() {
			id, _ := Digest(*o.Model)
			r.UnverifiedSourceArtifacts = sortedUnion(r.UnverifiedSourceArtifacts, []string{id})
		}
	}
	r.UnverifiedSourceArtifacts = sortedUnion(r.UnverifiedSourceArtifacts, as.Validation.Source.Spec.RecordedSelectionArtifacts)
	return r, nil
}

func compareVerifiedAllocations(ctx context.Context, v AllocationValidation, parent, child battlepolicy.Artifact, a, b AllocationEvaluationReport) (AllocationComparison, error) {
	var r AllocationComparison
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := v.ValidateCandidate(parent); err != nil {
		return r, err
	}
	if err := v.ValidateCandidate(child); err != nil {
		return r, err
	}
	pid, err := Digest(parent)
	if err != nil {
		return r, err
	}
	cid, err := Digest(child)
	if err != nil {
		return r, err
	}
	vid, err := Digest(v)
	if err != nil {
		return r, err
	}
	if pid != v.Experiment.InitialModel || child.Experiment != v.ExperimentDigest || pid == cid || a.Candidate != pid || b.Candidate != cid || a.Validation != vid || b.Validation != vid {
		return r, fmt.Errorf("allocation comparison requires the exact declared parent and its trained child")
	}
	scheduled, err := v.Schedule()
	if err != nil {
		return r, err
	}
	perArm := len(scheduled) / 2
	// [parent balanced,parent selected,child balanced,child selected], averaged
	// WITHIN each whole enemy-roster cluster over all rules/seeds/arena sides.
	cluster := make([][4]float64, v.Groups)
	counts := make([][4]int, v.Groups)
	enemyIndex := map[string]int{}
	for i, enemy := range v.OpponentRosters {
		enemyIndex[enemy.ID()] = i
	}
	censored := 0
	for model, report := range []AllocationEvaluationReport{a, b} {
		if report.Schema != allocationReportSchema || !digest(report.Evidence) || len(report.Results) != 2 {
			return r, fmt.Errorf("invalid supplementary report envelope")
		}
		expectedStatus := "complete"
		for ai, row := range report.Results {
			if row.Allocation != allocationNames[ai] || len(row.Games) != perArm {
				return r, fmt.Errorf("incomplete allocation comparison arm")
			}
			for i, g := range row.Games {
				if err := ctx.Err(); err != nil {
					return r, err
				}
				want := scheduled[ai*perArm+i]
				if g.Opponent != want.Opponent.Name || g.OpponentPolicy != want.Opponent.Version || g.CandidateSide != want.CandidateSide || g.Group != ScenarioGroup(want.Scenario) || !reflect.DeepEqual(g.Scenario, want.Scenario) || !digest(g.Shard) || g.Winner < -1 || g.Winner > 1 || g.Terminated == g.Truncated {
					return r, fmt.Errorf("allocation comparison game differs from frozen schedule")
				}
				index := enemyIndex[want.OpponentRoster]
				arm := model*2 + ai
				counts[index][arm]++
				if g.Truncated || !g.Terminated {
					censored++
					expectedStatus = "incomplete-outcomes"
					continue
				}
				score := 0.
				if g.Winner < 0 {
					score = .5
				} else if g.Winner == g.CandidateSide {
					score = 1
				}
				cluster[index][arm] += score
			}
		}
		if report.Status != expectedStatus {
			return r, fmt.Errorf("allocation report cutoff status mismatch")
		}
	}
	for i := range cluster {
		for arm := 0; arm < 4; arm++ {
			if counts[i][arm] != len(v.Source.Spec.Opponents)*4 {
				return r, fmt.Errorf("incomplete opponent-roster cluster")
			}
			cluster[i][arm] /= float64(counts[i][arm])
		}
	}
	aid, _ := Digest(a)
	bid, _ := Digest(b)
	r = AllocationComparison{Schema: "commander-allocation-comparison-v1", Method: "paired-opponent-roster-percentile-bootstrap-v1", Validation: vid, ParentReport: aid, ChildReport: bid, ParentArtifact: pid, ChildArtifact: cid, Groups: v.Groups, GamesPerArm: perArm, CensoredGames: censored, BootstrapSamples: 10000, BootstrapSeed: 8461, NominalAlpha: .05, AdjustmentComparisons: 5, AdjustmentScope: "all_five_allocation_and_policy_contrasts_in_this_comparison"}
	names := []string{"joint_gain", "policy_on_balanced", "policy_on_selected", "allocation_under_parent", "allocation_under_child"}
	effect := func(x [4]float64) [5]float64 {
		return [5]float64{x[3] - x[0], x[2] - x[0], x[3] - x[1], x[1] - x[0], x[3] - x[2]}
	}
	var total [5]float64
	means := make([][5]float64, len(cluster))
	for i, c := range cluster {
		means[i] = effect(c)
		for j, d := range means[i] {
			total[j] += d / float64(len(cluster))
		}
	}
	for i, name := range names {
		row := AllocationEffect{Name: name}
		if censored > 0 {
			row.IntervalUnavailable = "censored_outcomes"
		} else {
			d := total[i]
			row.Delta = &d
			if v.Groups < 20 {
				row.IntervalUnavailable = "fewer_than_20_opponent_rosters"
			}
		}
		r.Effects = append(r.Effects, row)
	}
	if censored > 0 || v.Groups < 20 {
		return r, nil
	}
	var draws [5][]float64
	for i := range draws {
		draws[i] = make([]float64, r.BootstrapSamples)
	}
	rng := rand.New(rand.NewSource(r.BootstrapSeed))
	for sample := 0; sample < r.BootstrapSamples; sample++ {
		if sample%64 == 0 {
			if err := ctx.Err(); err != nil {
				return AllocationComparison{}, err
			}
		}
		for range means {
			chosen := means[rng.Intn(len(means))]
			for effect, d := range chosen {
				draws[effect][sample] += d / float64(len(means))
			}
		}
	}
	for i := range draws {
		sort.Float64s(draws[i])
		q := func(p float64) float64 {
			return draws[i][max(0, min(len(draws[i])-1, int(math.Ceil(p*float64(len(draws[i]))))-1))]
		}
		interval := func(alpha float64) *[2]float64 { return &[2]float64{q(alpha / 2), q(1 - alpha/2)} }
		r.Effects[i].CI95 = interval(r.NominalAlpha)
		r.Effects[i].AdjustedInterval = interval(r.NominalAlpha / float64(r.AdjustmentComparisons))
	}
	return r, nil
}
