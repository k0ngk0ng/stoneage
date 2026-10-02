package battletrain

import (
	"fmt"
	"math"
	"sort"
)

// These bounds operate on independent configuration-family scores, never on
// the correlated seed/side repeats inside a family. Gates are frozen before
// final-test outcomes; alpha is spent across all attempts, including failures.
type PromotionGate struct {
	MinGroups      int     `json:"min_groups"`
	Alpha          float64 `json:"familywise_alpha"`
	RuleScore      float64 `json:"rule_score_floor"`
	ChampionMargin float64 `json:"champion_noninferiority_margin"`
}

func DefaultPromotionGate() PromotionGate {
	return PromotionGate{MinGroups: 256, Alpha: .05, RuleScore: .5, ChampionMargin: .05}
}

func promotionRules() []string {
	return []string{"basic", "focus", "guard-break", "defensive", "sustain"}
}

func (g PromotionGate) Validate() error {
	if g.MinGroups < 20 || g.MinGroups > 4096 || !finite(g.Alpha) || g.Alpha <= 0 || g.Alpha > .05 || !finite(g.RuleScore) || g.RuleScore < .5 || g.RuleScore >= 1 || !finite(g.ChampionMargin) || g.ChampionMargin < 0 || g.ChampionMargin > .05 {
		return fmt.Errorf("promotion gate requires 20..4096 families, alpha in (0,.05], rule floor >=.5 and champion margin <=.05")
	}
	return nil
}

type PromotionBound struct {
	Mode     int      `json:"mode,omitempty"`
	Opponent string   `json:"opponent"`
	Groups   int      `json:"groups"`
	Mean     *float64 `json:"mean_score,omitempty"`
	Lower    *float64 `json:"lower_bound,omitempty"`
	Required float64  `json:"required_score"`
	Passed   bool     `json:"passed"`
}

type PromotionAssessment struct {
	Passed  bool             `json:"passed"`
	Attempt int              `json:"attempt_number"`
	Alpha   float64          `json:"attempt_alpha"`
	Bounds  []PromotionBound `json:"bounds"`
	Reasons []string         `json:"reasons"`
}

// assessPromotion consumes an already verified, exact scheduled report. It
// does not accept a user-selected subset of opponents or discard cutoffs.
// Hoeffding's one-sided bound remains conservative for all-win samples, unlike
// a degenerate bootstrap interval. Its interpretation assumes independently
// sampled configuration families from the declared experiment distribution.
func assessPromotion(report EvaluationReport, gate PromotionGate, attempt int, champion bool) (PromotionAssessment, error) {
	if report.MixedExperiment != nil || report.MixedExperimentDigest != "" {
		return PromotionAssessment{}, fmt.Errorf("mixed promotion requires all declared modes and shared error budget")
	}
	return assessPromotionScoped(report, gate, attempt, champion, 1)
}

func assessPromotionScoped(report EvaluationReport, gate PromotionGate, attempt int, champion bool, modes int) (PromotionAssessment, error) {
	if report.ValidationComparison != nil || report.ValidationComparisonDigest != "" {
		return PromotionAssessment{}, fmt.Errorf("shared validation comparison cannot qualify a champion")
	}
	if len(report.UnverifiedSourceArtifacts) > 0 {
		return PromotionAssessment{}, fmt.Errorf("promotion requires verified source separation; recorded training configurations may overlap evaluation")
	}
	var out PromotionAssessment
	if e := validatePairing(report.Config.Pairing); e != nil {
		return out, e
	}
	size := familyGames(report.Config.Pairing)
	if e := gate.Validate(); e != nil {
		return out, e
	}
	if attempt < 1 || attempt > 1000000 || modes < 1 || modes > 5 {
		return out, fmt.Errorf("invalid promotion attempt index")
	}
	out.Attempt = attempt
	out.Alpha = gate.Alpha / (float64(attempt) * float64(attempt+1))
	out.Alpha /= float64(modes)
	required := map[string]float64{}
	for _, rule := range promotionRules() {
		required[rule] = gate.RuleScore
	}
	if champion {
		required["champion"] = .5 - gate.ChampionMargin
	}
	byOpponent := map[string]map[string][]EvaluationGame{}
	for _, game := range report.Games {
		if _, ok := required[game.Opponent]; !ok {
			return out, fmt.Errorf("unexpected promotion opponent")
		}
		if byOpponent[game.Opponent] == nil {
			byOpponent[game.Opponent] = map[string][]EvaluationGame{}
		}
		byOpponent[game.Opponent][game.Group] = append(byOpponent[game.Opponent][game.Group], game)
	}
	if len(byOpponent) != len(required) {
		return out, fmt.Errorf("promotion is missing required opponents")
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	out.Passed = true
	for _, name := range names {
		groups := byOpponent[name]
		bound := PromotionBound{Opponent: name, Groups: len(groups), Required: required[name]}
		mean := 0.
		cutoff := false
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			games := groups[key]
			if len(games) != size {
				return out, fmt.Errorf("promotion requires all %d paired games per family", size)
			}
			for _, game := range games {
				if game.Truncated || !game.Terminated {
					cutoff = true
					continue
				}
				if game.Winner < 0 {
					mean += .5 / float64(size)
				} else if game.Winner == game.CandidateSide {
					mean += 1.0 / float64(size)
				}
			}
		}
		if !cutoff {
			mean /= float64(len(groups))
			lower := math.Max(0, mean-math.Sqrt(math.Log(float64(len(required))/out.Alpha)/(2*float64(len(groups)))))
			bound.Mean, bound.Lower = &mean, &lower
			bound.Passed = len(groups) >= gate.MinGroups && lower > bound.Required
		}
		if cutoff {
			out.Reasons = append(out.Reasons, name+": incomplete/cutoff games; no promotion")
		}
		if len(groups) < gate.MinGroups {
			out.Reasons = append(out.Reasons, name+": insufficient independent families")
		}
		if bound.Lower != nil && *bound.Lower <= bound.Required {
			out.Reasons = append(out.Reasons, name+": score lower bound did not exceed frozen threshold")
		}
		out.Passed = out.Passed && bound.Passed
		out.Bounds = append(out.Bounds, bound)
	}
	return out, nil
}
