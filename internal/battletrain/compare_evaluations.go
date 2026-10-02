package battletrain

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
)

type PairedEvaluationRow struct {
	Opponent            string      `json:"opponent"`
	OpponentPolicy      string      `json:"opponent_policy"`
	Games               int         `json:"games"`
	Groups              int         `json:"groups"`
	CensoredPairs       int         `json:"censored_pairs"`
	Baseline            Comparison  `json:"baseline"`
	Candidate           Comparison  `json:"candidate"`
	PairedDelta         *float64    `json:"paired_delta,omitempty"`
	CI95                *[2]float64 `json:"paired_ci95,omitempty"`
	AdjustedInterval    *[2]float64 `json:"bonferroni_bootstrap_interval,omitempty"`
	IntervalUnavailable string      `json:"interval_unavailable,omitempty"`
}

type PairedEvaluationReport struct {
	ValidationComparison      string                `json:"validation_comparison,omitempty"`
	MixedExperiment           string                `json:"mixed_experiment,omitempty"`
	Schema                    string                `json:"schema"`
	Method                    string                `json:"method"`
	BaselineReport            string                `json:"baseline_report"`
	CandidateReport           string                `json:"candidate_report"`
	BaselineArtifact          string                `json:"baseline_artifact"`
	CandidateArtifact         string                `json:"candidate_artifact"`
	Experiment                string                `json:"experiment"`
	Split                     string                `json:"split"`
	BootstrapSamples          int                   `json:"bootstrap_samples"`
	BootstrapSeed             int64                 `json:"bootstrap_seed"`
	NominalAlpha              float64               `json:"nominal_alpha"`
	AdjustmentComparisons     int                   `json:"adjustment_comparisons"`
	AdjustmentScope           string                `json:"adjustment_scope"`
	ArenaCertified            bool                  `json:"arena_certified"`
	UnverifiedSourceArtifacts []string              `json:"unverified_source_artifacts,omitempty"`
	Comparisons               []PairedEvaluationRow `json:"comparisons"`
}

// CompareEvaluations verifies both complete raw-evidence archives before any
// comparison. It is a read-only validation analysis, never a promotion gate.
func CompareEvaluations(ctx context.Context, baseline, candidate string) (PairedEvaluationReport, error) {
	var empty PairedEvaluationReport
	if e := ctx.Err(); e != nil {
		return empty, e
	}
	a, e := VerifyEvaluation(ctx, baseline)
	if e != nil {
		return empty, fmt.Errorf("baseline evidence: %w", e)
	}
	b, e := VerifyEvaluation(ctx, candidate)
	if e != nil {
		return empty, fmt.Errorf("candidate evidence: %w", e)
	}
	return compareVerifiedEvaluations(ctx, a, b)
}

func compareVerifiedEvaluations(ctx context.Context, a, b EvaluationReport) (PairedEvaluationReport, error) {
	var result PairedEvaluationReport
	if e := ctx.Err(); e != nil {
		return result, e
	}
	for _, r := range []EvaluationReport{a, b} {
		if e := ValidateEvaluation(r); e != nil {
			return result, e
		}
		if r.Evidence == "" || r.Experiment == nil || r.Split != "validation" {
			return result, fmt.Errorf("paired comparison requires recorded frozen validation reports")
		}
	}
	if a.Environment != b.Environment || a.Config != b.Config || a.ExperimentDigest != b.ExperimentDigest || a.MixedExperimentDigest != b.MixedExperimentDigest || a.ValidationComparisonDigest != b.ValidationComparisonDigest {
		return result, fmt.Errorf("paired reports require the same experiment, environment and scenario settings")
	}
	left, right := map[string][]EvaluationGame{}, map[string][]EvaluationGame{}
	leftSummary, rightSummary := map[string]Comparison{}, map[string]Comparison{}
	for _, g := range a.Games {
		left[g.Opponent] = append(left[g.Opponent], g)
	}
	for _, g := range b.Games {
		right[g.Opponent] = append(right[g.Opponent], g)
	}
	for _, r := range a.Comparisons {
		leftSummary[r.Opponent] = r
	}
	for _, r := range b.Comparisons {
		rightSummary[r.Opponent] = r
	}
	if len(left) != len(right) {
		return result, fmt.Errorf("paired reports require the complete same opponent set")
	}
	var names []string
	for name, games := range left {
		if len(games) != len(right[name]) {
			return result, fmt.Errorf("paired opponent %q is missing or incomplete", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	aid, e := Digest(a)
	if e != nil {
		return result, e
	}
	bid, e := Digest(b)
	if e != nil {
		return result, e
	}
	result = PairedEvaluationReport{
		MixedExperiment:      a.MixedExperimentDigest,
		ValidationComparison: a.ValidationComparisonDigest,
		Schema:               "commander-paired-evaluation-v1", Method: "paired-family-percentile-bootstrap-v1",
		BaselineReport: aid, CandidateReport: bid,
		BaselineArtifact: a.CandidateArtifact, CandidateArtifact: b.CandidateArtifact,
		Experiment: a.ExperimentDigest, Split: "validation",
		BootstrapSamples: 10000, BootstrapSeed: 8301, NominalAlpha: .05,
		AdjustmentComparisons: len(names), AdjustmentScope: "opponents_within_this_comparison",
		UnverifiedSourceArtifacts: sortedUnion(a.UnverifiedSourceArtifacts, b.UnverifiedSourceArtifacts),
	}
	for _, name := range names {
		row := PairedEvaluationRow{Opponent: name, Games: len(left[name]), Baseline: leftSummary[name], Candidate: rightSummary[name]}
		groups := map[string][]float64{}
		for i, g := range left[name] {
			if e := ctx.Err(); e != nil {
				return PairedEvaluationReport{}, e
			}
			h := right[name][i]
			// Validation already checks within-opponent frozen suite order. Match
			// every actual setup/side/version too, including legitimate repeated
			// identical setups from symmetric roster families.
			if g.OpponentPolicy != h.OpponentPolicy || g.Group != h.Group || g.CandidateSide != h.CandidateSide || !reflect.DeepEqual(g.Scenario, h.Scenario) {
				return PairedEvaluationReport{}, fmt.Errorf("opponent %q game %d differs in setup, side or policy", name, i)
			}
			row.OpponentPolicy = g.OpponentPolicy
			if g.Truncated || h.Truncated || !g.Terminated || !h.Terminated {
				row.CensoredPairs++
				groups[g.Group] = append(groups[g.Group], 0) // Count only; never estimate from censored rows.
				continue
			}
			score := func(v EvaluationGame) float64 {
				if v.Winner < 0 {
					return .5
				}
				if v.Winner == v.CandidateSide {
					return 1
				}
				return 0
			}
			groups[g.Group] = append(groups[g.Group], score(h)-score(g))
		}
		row.Groups = len(groups)
		var ids []string
		for id, values := range groups {
			if len(values) != familyGames(a.Config.Pairing) {
				return PairedEvaluationReport{}, fmt.Errorf("incomplete paired family")
			}
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if row.CensoredPairs > 0 {
			row.IntervalUnavailable = "censored_outcomes"
		} else {
			var means []float64
			total := 0.
			for _, id := range ids {
				sum := 0.
				for _, v := range groups[id] {
					sum += v
				}
				mean := sum / float64(len(groups[id]))
				means = append(means, mean)
				total += mean
			}
			delta := total / float64(len(means))
			row.PairedDelta = &delta
			if len(means) < 20 {
				row.IntervalUnavailable = "fewer_than_20_families"
			} else {
				draws := make([]float64, result.BootstrapSamples)
				rng := rand.New(rand.NewSource(result.BootstrapSeed))
				for i := range draws {
					if i%64 == 0 {
						if e := ctx.Err(); e != nil {
							return PairedEvaluationReport{}, e
						}
					}
					for range means {
						draws[i] += means[rng.Intn(len(means))]
					}
					draws[i] /= float64(len(means))
				}
				sort.Float64s(draws)
				interval := func(alpha float64) *[2]float64 {
					q := func(p float64) float64 {
						return draws[max(0, min(len(draws)-1, int(math.Ceil(p*float64(len(draws))))-1))]
					}
					return &[2]float64{q(alpha / 2), q(1 - alpha/2)}
				}
				row.CI95 = interval(result.NominalAlpha)
				row.AdjustedInterval = interval(result.NominalAlpha / float64(len(names)))
			}
		}
		result.Comparisons = append(result.Comparisons, row)
	}
	return result, nil
}
