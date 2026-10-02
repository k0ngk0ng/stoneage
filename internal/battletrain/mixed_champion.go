package battletrain

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func (r ChampionRegistry) validateMixed() error {
	if r.Schema != "native-mixed-champion-registry-v1" || len(r.MixedConditions) < 2 || len(r.MixedConditions) > 5 || r.Conditions != (EvaluationConfig{}) {
		return fmt.Errorf("invalid mixed champion registry conditions")
	}
	previous := 0
	for _, c := range r.MixedConditions {
		if c.Mode <= previous || c.Pairing != BalancedPairing {
			return fmt.Errorf("mixed champion modes must be sorted, unique and use balanced families")
		}
		one := ChampionRegistry{Schema: championSchema(c.Pairing), Environment: r.Environment, Conditions: c, Gate: r.Gate, Rules: r.Rules}
		if err := one.Validate(); err != nil {
			return err
		}
		previous = c.Mode
	}
	return nil
}

// SupportsMode concerns the registry's frozen conditions, not evidence that
// any particular model passed. Callers must separately verify its event chain.
func (r ChampionRegistry) SupportsMode(mode int) bool {
	if len(r.MixedConditions) == 0 {
		return r.Conditions.Mode == mode
	}
	for _, c := range r.MixedConditions {
		if c.Mode == mode {
			return true
		}
	}
	return false
}

func (r ChampionRegistry) eventSchema() string {
	if len(r.MixedConditions) > 0 {
		return "native-mixed-champion-event-v1"
	}
	return "native-champion-event-v1"
}

func InitMixedChampionRegistry(root string, x MixedExperiment, gate PromotionGate) error {
	if err := x.Validate(); err != nil {
		return err
	}
	r := ChampionRegistry{Schema: "native-mixed-champion-registry-v1", Environment: x.Parts[0].Experiment.Environment, Gate: gate, Rules: map[string]string{}}
	for _, p := range x.Parts {
		r.MixedConditions = append(r.MixedConditions, championConditions(p.Experiment))
	}
	for _, rule := range promotionRules() {
		version, err := (Policy{Rule: rule}).Version()
		if err != nil {
			return err
		}
		r.Rules[rule] = version
	}
	if err := r.Validate(); err != nil {
		return err
	}
	lock, err := championLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	return writeObject(filepath.Join(root, "registry.json"), r)
}

func (a ChampionAttempt) experiments() []Experiment {
	if a.Mixed == nil {
		return []Experiment{a.Experiment}
	}
	xs := make([]Experiment, len(a.Mixed.Parts))
	for i, p := range a.Mixed.Parts {
		xs[i] = p.Experiment
	}
	return xs
}

func validateMixedChampionAttempt(ctx context.Context, root string, s ChampionStatus, a ChampionAttempt, previous string) error {
	rid, _ := Digest(s.Registry)
	if a.Mixed == nil || len(s.Registry.MixedConditions) == 0 || a.Schema != "native-mixed-champion-attempt-v1" || !reflect.DeepEqual(a.Experiment, Experiment{}) || a.Registry != rid || a.Previous != previous || a.Champion != s.Champion || a.Number != s.Attempts+1 || a.Candidate == a.Champion {
		return fmt.Errorf("mixed champion attempt differs from frozen registry/history")
	}
	if err := a.Mixed.Validate(); err != nil {
		return err
	}
	if len(a.Mixed.Parts) != len(s.Registry.MixedConditions) {
		return fmt.Errorf("mixed champion attempt omitted a declared mode")
	}
	candidate, err := championModel(root, a.Candidate)
	if err != nil {
		return err
	}
	opponents, err := championOpponents(root, a.Champion)
	if err != nil {
		return err
	}
	for i, p := range a.Mixed.Parts {
		x := p.Experiment
		if x.Environment != s.Registry.Environment || championConditions(x) != s.Registry.MixedConditions[i] {
			return fmt.Errorf("mixed champion scenario changed")
		}
		if len(x.groups("test")) < s.Registry.Gate.MinGroups {
			return fmt.Errorf("mixed test split has too few independent families")
		}
		for _, family := range x.groups("test") {
			if s.used[family.Group] {
				return fmt.Errorf("mixed promotion test family was already exposed")
			}
		}
		if _, err := prepareEvaluationWithMixed(ctx, s.Registry.Environment, candidate, opponents, experimentEvaluationConfig(x, "test"), &x, a.Mixed, "test"); err != nil {
			return err
		}
	}
	return nil
}

func championModeReportPath(root, attempt string, a ChampionAttempt, mode int) string {
	if a.Mixed == nil {
		return championReportPath(root, attempt)
	}
	return filepath.Join(root, "evaluations", fmt.Sprintf("%s-mode-%d.json", attempt, mode))
}

func verifyChampionReports(ctx context.Context, root, attemptID string, a ChampionAttempt, verify func(context.Context, string) (EvaluationReport, error)) ([]EvaluationReport, error) {
	var reports []EvaluationReport
	for _, x := range a.experiments() {
		r, err := verify(ctx, championModeReportPath(root, attemptID, a, x.Mode))
		if err != nil {
			return nil, err
		}
		if r.Config.Mode != x.Mode {
			return nil, fmt.Errorf("champion report occupies the wrong mode slot")
		}
		if err := matchChampionReport(root, a, r); err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, nil
}

func championReportsDigest(a ChampionAttempt, reports []EvaluationReport) (string, error) {
	if a.Mixed == nil {
		if len(reports) != 1 {
			return "", fmt.Errorf("single-mode champion requires one report")
		}
		return Digest(reports[0])
	}
	return Digest(reports)
}

func assessChampionReports(reports []EvaluationReport, a ChampionAttempt, gate PromotionGate) (PromotionAssessment, error) {
	if a.Mixed == nil {
		if len(reports) != 1 {
			return PromotionAssessment{}, fmt.Errorf("single-mode champion requires one report")
		}
		return assessPromotion(reports[0], gate, a.Number, a.Champion != "")
	}
	var out PromotionAssessment
	if err := a.Mixed.Validate(); err != nil {
		return out, err
	}
	if len(reports) != len(a.Mixed.Parts) {
		return out, fmt.Errorf("mixed promotion requires every declared mode")
	}
	if err := gate.Validate(); err != nil {
		return out, err
	}
	if a.Number < 1 || a.Number > 1000000 {
		return out, fmt.Errorf("invalid mixed promotion attempt")
	}
	out.Passed, out.Attempt = true, a.Number
	out.Alpha = gate.Alpha / (float64(a.Number) * float64(a.Number+1))
	xid, _ := Digest(*a.Mixed)
	for i, r := range reports {
		p := a.Mixed.Parts[i]
		if r.Config.Mode != p.Experiment.Mode || r.ExperimentDigest != p.Digest || r.MixedExperimentDigest != xid || r.CandidateArtifact != a.Candidate || r.Split != "test" {
			return PromotionAssessment{}, fmt.Errorf("mixed promotion reports have different candidates, experiments or mode order")
		}
		assessment, err := assessPromotionScoped(r, gate, a.Number, a.Champion != "", len(reports))
		if err != nil {
			return PromotionAssessment{}, err
		}
		out.Passed = out.Passed && assessment.Passed
		for _, bound := range assessment.Bounds {
			bound.Mode = r.Config.Mode
			out.Bounds = append(out.Bounds, bound)
		}
		for _, reason := range assessment.Reasons {
			out.Reasons = append(out.Reasons, fmt.Sprintf("mode %d: %s", r.Config.Mode, reason))
		}
	}
	return out, nil
}

func freezeChampionSelection(ctx context.Context, path string, a ChampionAttempt, candidate battlepolicy.Artifact, opponents []Opponent) error {
	if a.Mixed == nil {
		return FreezeFinalSelection(path, a.Experiment, candidate, opponents)
	}
	for _, p := range a.Mixed.Parts {
		if err := FreezeMixedFinalSelection(ctx, path, *a.Mixed, p.Experiment.Mode, candidate, opponents); err != nil {
			return err
		}
	}
	return nil
}

// ChallengeMixedChampion uses one immutable event and one error budget for
// the complete commander. No mode is adopted before all modes finish and pass.
func ChallengeMixedChampion(ctx context.Context, root string, engine *battleenv.Native, candidate battlepolicy.Artifact, x MixedExperiment, selectionPath string, progress func(EvaluationGame) error) (ChampionEvent, error) {
	return challengeChampion(ctx, root, engine, candidate, Experiment{}, &x, selectionPath, progress)
}
