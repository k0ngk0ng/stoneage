package battletrain

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type EvaluationConfig struct {
	PetSkillMask       int    `json:"pet_skill_mask,omitempty"` // Optional active set; zero preserves the original seven skills.
	Pairing            string `json:"pairing,omitempty"`
	ReservePets        int    `json:"reserve_pets,omitempty"`  // Extra pets per member, 0..2.
	HealingItems       int    `json:"healing_items,omitempty"` // Number of single-use template 1234 items per character, 0..15.
	HealingMagic       int    `json:"healing_magic,omitempty"`
	Seed               int64  `json:"seed"`
	Mode               int    `json:"mode"`
	Points             int    `json:"points"`
	PetPoints          int    `json:"pet_points"`
	Level              int    `json:"level"`
	MaxTurns           int    `json:"max_turns"`
	MatchesPerOpponent int    `json:"matches_per_opponent"`
}

func DefaultEvaluationConfig() EvaluationConfig {
	return EvaluationConfig{Pairing: BalancedPairing, Seed: 20260929, Mode: 1, Points: 120, PetPoints: 120, Level: 35, MaxTurns: 200, MatchesPerOpponent: 1000}
}

type Opponent struct {
	Name  string
	Rule  string
	Model *battlepolicy.Artifact
}
type EvaluationGame struct {
	Shard          string             `json:"shard,omitempty"`
	Opponent       string             `json:"opponent"`
	OpponentPolicy string             `json:"opponent_policy"`
	Group          string             `json:"group"`
	Scenario       battleenv.Scenario `json:"scenario"`
	CandidateSide  int                `json:"candidate_side"`
	Turns          int                `json:"turns"`
	Winner         int                `json:"winner"`
	Terminated     bool               `json:"terminated"`
	Truncated      bool               `json:"truncated"`
}
type Comparison struct {
	Opponent       string      `json:"opponent"`
	Games          int         `json:"games"`
	Groups         int         `json:"groups"`
	Wins           int         `json:"wins"`
	Losses         int         `json:"losses"`
	Draws          int         `json:"draws"`
	Truncated      int         `json:"truncated"`
	MeanTurns      float64     `json:"mean_turns"`
	CompletedScore *float64    `json:"completed_score,omitempty"` // win=1, draw=.5; excludes cutoffs explicitly.
	ClusterCI95    *[2]float64 `json:"cluster_score_ci95,omitempty"`
}
type EvaluationReport struct {
	ValidationComparison       *MixedValidationComparison `json:"validation_comparison,omitempty"`
	ValidationComparisonDigest string                     `json:"validation_comparison_digest,omitempty"`
	MixedExperiment            *MixedExperiment           `json:"mixed_experiment,omitempty"`
	MixedExperimentDigest      string                     `json:"mixed_experiment_digest,omitempty"`
	UnverifiedSourceArtifacts  []string                   `json:"unverified_source_artifacts,omitempty"`
	Evidence                   string                     `json:"evidence,omitempty"`
	Experiment                 *Experiment                `json:"experiment,omitempty"`
	ExperimentDigest           string                     `json:"experiment_digest,omitempty"`
	Split                      string                     `json:"split,omitempty"`
	CandidateArtifact          string                     `json:"candidate_artifact,omitempty"`
	Schema                     string                     `json:"schema"`
	Candidate                  string                     `json:"candidate_weights"`
	Environment                battleenv.Metadata         `json:"environment"`
	Config                     EvaluationConfig           `json:"config"`
	ExcludedTrainingGroups     int                        `json:"excluded_training_groups"`
	Comparisons                []Comparison               `json:"comparisons"`
	Games                      []EvaluationGame           `json:"games"`
}

func supports(a battlepolicy.Artifact, mode int) bool {
	for _, m := range a.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Evaluate freezes the complete scenario suite before any games. New schedules
// cross seeds, roster ownership and arena side, all sharing a statistical
// group. Known synthetic training groups are excluded; recorded sources whose
// hidden configurations cannot be recovered are explicitly marked unverified.
// This is real native play, separate from legacy victory-prediction metrics.
func Evaluate(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, progress func(EvaluationGame) error) (EvaluationReport, error) {
	if candidate.Experiment != "" {
		return EvaluationReport{}, fmt.Errorf("experiment-bound candidate requires its frozen experiment evaluation")
	}
	return evaluate(ctx, engine, candidate, opponents, c, nil, "", progress, nil)
}

// EvaluateExperiment uses the entire predeclared validation or test split.
// It refuses overlap instead of silently replacing unfavorable/seen families.
// Validation also accepts the exact declared parent as a pre-training baseline.
func EvaluateExperiment(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, x Experiment, split string, progress func(EvaluationGame) error) (EvaluationReport, error) {
	if e := x.Validate(); e != nil {
		return EvaluationReport{}, e
	}
	if e := x.validateEvaluationCandidate(candidate, split); e != nil {
		return EvaluationReport{}, e
	}
	c := experimentEvaluationConfig(x, split)
	return evaluate(ctx, engine, candidate, opponents, c, &x, split, progress, nil)
}

type evaluationSchedule struct {
	StrictPolicyStatistics bool
	Report                 EvaluationReport
	Suite                  []battleenv.Scenario
	Policies               []Policy
	Versions               []string
}

func prepareEvaluation(ctx context.Context, meta battleenv.Metadata, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, split string) (schedule evaluationSchedule, err error) {
	return prepareEvaluationWithMixed(ctx, meta, candidate, opponents, c, experiment, nil, split)
}

func prepareEvaluationWithMixed(ctx context.Context, meta battleenv.Metadata, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, split string) (schedule evaluationSchedule, err error) {
	return prepareEvaluationWithComparison(ctx, meta, candidate, opponents, c, experiment, mixed, nil, split)
}

func prepareEvaluationWithComparison(ctx context.Context, meta battleenv.Metadata, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, comparison *MixedValidationComparison, split string) (schedule evaluationSchedule, err error) {
	if c.PetSkillMask != 0 && meta.Scenario != "controlled-battle-v8" {
		return schedule, fmt.Errorf("active pet skill configuration requires controlled-battle-v8")
	}
	// Return the partially prepared report on failure as before, while sharing
	// the exact schedule/provenance checks with evidence verification.
	report := EvaluationReport{Schema: "commander-evaluation-v2", Candidate: candidate.WeightsDigest, Config: c}
	defer func() { schedule.Report = report }()
	report.CandidateArtifact, _ = Digest(candidate)
	if experiment != nil {
		if e := experiment.Validate(); e != nil {
			return schedule, e
		}
		var e error
		if mixed != nil {
			e = mixed.validateEvaluationCandidate(candidate, *experiment, split)
		} else {
			e = experiment.validateEvaluationCandidate(candidate, split)
		}
		if e != nil {
			return schedule, e
		}
		c = experimentEvaluationConfig(*experiment, split)
		report.Config = c
		report.Experiment, report.Split = experiment, split
		report.ExperimentDigest, _ = Digest(*experiment)
		if mixed != nil {
			report.MixedExperiment = mixed
			report.MixedExperimentDigest, _ = Digest(*mixed)
		}
	} else if candidate.Experiment != "" || split != "" || mixed != nil {
		return schedule, fmt.Errorf("experiment-bound candidate requires its frozen experiment evaluation")
	}
	if e := validatePairing(c.Pairing); e != nil {
		return schedule, e
	}
	report.Schema = evaluationSchema(c.Pairing)
	if mixed != nil {
		report.Schema = "commander-mixed-evaluation-v1"
	}
	sharedValidation := map[string]bool{}
	if comparison != nil {
		if mixed == nil || experiment == nil || split != "validation" || comparison.LeftDigest != report.MixedExperimentDigest || len(opponents) != 1 || opponents[0].Model == nil || opponents[0].Rule != "" {
			return schedule, fmt.Errorf("explicit mixed comparison requires its left envelope, validation split and exactly one right model")
		}
		groups, e := comparison.ValidatePair(candidate, *opponents[0].Model, c.Mode, split)
		if e != nil {
			return schedule, e
		}
		for _, id := range groups {
			sharedValidation[id] = true
		}
		report.ValidationComparison = comparison
		report.ValidationComparisonDigest, _ = Digest(*comparison)
		report.Schema = "commander-mixed-comparison-evaluation-v1"
	}
	if e := candidate.Validate(); e != nil {
		return schedule, e
	}
	size := familyGames(c.Pairing)
	if len(opponents) == 0 || len(opponents) > 32 || c.MatchesPerOpponent < size || c.MatchesPerOpponent > 100000 || c.MatchesPerOpponent%size != 0 {
		return schedule, fmt.Errorf("evaluation requires opponents and a positive multiple of %d matches", size)
	}
	base := DefaultRunConfig()
	base.Pairing = c.Pairing
	base.Seed = c.Seed
	base.Mode = c.Mode
	base.Points = c.Points
	base.PetPoints = c.PetPoints
	base.HealingMagic = c.HealingMagic
	base.HealingItems = c.HealingItems
	base.ReservePets = c.ReservePets
	base.PetSkillMask = c.PetSkillMask
	base.Level = c.Level
	base.MaxTurns = c.MaxTurns
	if e := base.Validate(); e != nil {
		return schedule, e
	}
	if e := meta.Validate(); e != nil {
		return schedule, e
	}
	if !candidate.CompatibleNativeEnvironment(meta) || !supports(candidate, c.Mode) {
		return schedule, fmt.Errorf("candidate environment or mode incompatible with evaluation")
	}
	if experiment != nil && experiment.Environment != meta {
		return schedule, fmt.Errorf("evaluation engine differs from frozen experiment")
	}
	report.Environment = meta
	if candidate.HasRecordedSource() {
		report.UnverifiedSourceArtifacts = []string{report.CandidateArtifact}
	}
	excluded := map[string]bool{}
	for _, g := range candidate.DataGroups() {
		excluded[g] = true
	}
	policies := make([]Policy, len(opponents))
	versions := make([]string, len(opponents))
	names := map[string]bool{}
	for i, o := range opponents {
		if o.Name == "" || names[o.Name] || (o.Rule == "") == (o.Model == nil) {
			return schedule, fmt.Errorf("each opponent needs a unique name and exactly one rule/model")
		}
		names[o.Name] = true
		if o.Model != nil {
			if e := o.Model.Validate(); e != nil {
				return schedule, e
			}
			if !o.Model.CompatibleNativeEnvironment(meta) || !supports(*o.Model, c.Mode) {
				return schedule, fmt.Errorf("opponent environment/mode incompatible")
			}
			if o.Model.HasRecordedSource() {
				id, _ := Digest(*o.Model)
				report.UnverifiedSourceArtifacts = sortedUnion(report.UnverifiedSourceArtifacts, []string{id})
			}
			for _, g := range o.Model.DataGroups() {
				excluded[g] = true
			}
			comparisonExperiment := candidate.Experiment
			if experiment != nil {
				comparisonExperiment = report.ExperimentDigest
			}
			if mixed != nil {
				comparisonExperiment = report.MixedExperimentDigest
			}
			if o.Model.Experiment != comparisonExperiment {
				for _, g := range o.Model.HeldoutGroups {
					if !sharedValidation[g] {
						excluded[g] = true
					}
				}
			}
			policies[i] = Policy{Model: o.Model.Network, Greedy: true}
		} else {
			policies[i] = Policy{Rule: o.Rule}
		}
		var e error
		versions[i], e = policies[i].Version()
		if e != nil {
			return schedule, e
		}
	}
	report.ExcludedTrainingGroups = len(excluded)
	var suite []battleenv.Scenario
	if experiment != nil {
		var e error
		suite, e = experiment.evaluationSuite(split)
		if e != nil {
			return schedule, e
		}
		for _, s := range suite {
			if excluded[ScenarioGroup(s)] {
				return schedule, fmt.Errorf("frozen %s split overlaps candidate/opponent training; experiment rejected", split)
			}
		}
	}
	seen := map[string]bool{}
	for family := uint64(0); len(suite) < c.MatchesPerOpponent; family++ {
		if e := ctx.Err(); e != nil {
			return schedule, e
		}
		if family > uint64(c.MatchesPerOpponent*100+10000) {
			return schedule, fmt.Errorf("not enough distinct unseen configurations for independent evaluation")
		}
		s, group := scenarioFor(base, family*uint64(size))
		if excluded[group] || seen[group] {
			continue
		}
		seen[group] = true
		suite = append(suite, s)
		for k := uint64(1); k < uint64(size); k++ {
			s, _ = scenarioFor(base, family*uint64(size)+k)
			suite = append(suite, s)
		}
	}
	schedule.Suite, schedule.Policies, schedule.Versions = suite, policies, versions
	return schedule, nil
}

func experimentEvaluationConfig(x Experiment, split string) EvaluationConfig {
	return EvaluationConfig{Pairing: x.Pairing, Seed: x.Seed, Mode: x.Mode, Points: x.Points, PetPoints: x.PetPoints, Level: x.Level, HealingMagic: x.HealingMagic, HealingItems: x.HealingItems, PetSkillMask: x.PetSkillMask, ReservePets: x.ReservePets, MaxTurns: x.MaxTurns, MatchesPerOpponent: familyGames(x.Pairing) * len(x.groups(split))}
}

func evaluate(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, split string, progress func(EvaluationGame) error, recorder *evaluationRecorder) (EvaluationReport, error) {
	return evaluateWithMixed(ctx, engine, candidate, opponents, c, experiment, nil, split, progress, recorder)
}

func evaluateWithMixed(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, split string, progress func(EvaluationGame) error, recorder *evaluationRecorder) (EvaluationReport, error) {
	return evaluateWithComparison(ctx, engine, candidate, opponents, c, experiment, mixed, nil, split, progress, recorder)
}

func evaluateWithComparison(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, c EvaluationConfig, experiment *Experiment, mixed *MixedExperiment, comparison *MixedValidationComparison, split string, progress func(EvaluationGame) error, recorder *evaluationRecorder) (EvaluationReport, error) {
	if engine == nil {
		return EvaluationReport{}, fmt.Errorf("evaluation engine required")
	}
	schedule, e := prepareEvaluationWithComparison(ctx, engine.Metadata(), candidate, opponents, c, experiment, mixed, comparison, split)
	report := schedule.Report
	if e != nil {
		return report, e
	}
	if recorder != nil {
		if e = recorder.freeze(report, candidate, opponents); e != nil {
			return report, e
		}
		report.Evidence = recorder.id
		report.Games, e = recorder.restore(ctx, candidate, opponents, schedule)
		if e != nil {
			return report, e
		}
	}
	return collectPreparedEvaluation(ctx, engine, candidate, opponents, schedule, report, progress, recorder)
}

// collectPreparedEvaluation shares native collection/commits across explicitly
// validated evaluation domains. Callers preflight and restore before entry.
func collectPreparedEvaluation(ctx context.Context, engine *battleenv.Native, candidate battlepolicy.Artifact, opponents []Opponent, schedule evaluationSchedule, report EvaluationReport, progress func(EvaluationGame) error, recorder *evaluationRecorder) (EvaluationReport, error) {
	suite, policies, versions, meta := schedule.Suite, schedule.Policies, schedule.Versions, report.Environment

	restored := len(report.Games)
	for oi, o := range opponents {
		for i, s := range suite {
			index := oi*len(suite) + i
			if index < restored {
				continue
			}
			side := i % 2
			pair := [2]Policy{}
			pair[side] = Policy{Model: candidate.Network, Greedy: true}
			pair[1-side] = policies[oi]
			trajectories, e := CollectPolicies(ctx, engine, s, pair, [2]*rand.Rand{}, ScenarioGroup(s), meta.Rules)
			if e != nil {
				return report, e
			}
			t := trajectories[side]
			game := EvaluationGame{Opponent: o.Name, OpponentPolicy: versions[oi], Group: t.Group, Scenario: s, CandidateSide: side, Turns: len(t.Steps), Winner: t.Winner, Terminated: t.Terminated, Truncated: t.Truncated}
			if recorder != nil {
				shard, e := SaveShard(filepath.Join(recorder.root, "shards"), trajectories[:])
				if e != nil {
					return report, e
				}
				game.Shard = shard.Digest
				if e := recorder.commit(index, game.Shard); e != nil {
					return report, e
				}
			}
			report.Games = append(report.Games, game)
			if progress != nil {
				if e = progress(game); e != nil {
					return report, e
				}
			}
		}
	}
	report.Comparisons = Summarize(report.Games, report.Config.Seed)
	return report, nil
}

// Summarize resamples whole configuration families, preserving correlation
// across repeats/swaps. It never treats a collection cutoff as a loss/draw.
func Summarize(games []EvaluationGame, seed int64) []Comparison {
	byOpponent := map[string][]EvaluationGame{}
	for _, g := range games {
		byOpponent[g.Opponent] = append(byOpponent[g.Opponent], g)
	}
	var names []string
	for name := range byOpponent {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Comparison
	for ni, name := range names {
		c := Comparison{Opponent: name}
		type aggregate struct {
			score float64
			count int
		}
		groups := map[string]aggregate{}
		for _, g := range byOpponent[name] {
			c.Games++
			c.MeanTurns += float64(g.Turns)
			a := groups[g.Group]
			if g.Truncated || !g.Terminated {
				c.Truncated++
				groups[g.Group] = a
				continue
			}
			a.count++
			if g.Winner < 0 {
				c.Draws++
				a.score += .5
			} else if g.Winner == g.CandidateSide {
				c.Wins++
				a.score++
			} else {
				c.Losses++
			}
			groups[g.Group] = a
		}
		c.MeanTurns /= float64(c.Games)
		c.Groups = len(groups)
		n := c.Wins + c.Losses + c.Draws
		if n > 0 {
			v := (float64(c.Wins) + float64(c.Draws)*.5) / float64(n)
			c.CompletedScore = &v
		}
		var keys []string
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) >= 20 && n > 0 {
			rng := rand.New(rand.NewSource(gameSeed(seed, uint64(ni), 90)))
			values := make([]float64, 0, 2000)
			for i := 0; i < 2000; i++ {
				score, count := 0., 0
				for range keys {
					a := groups[keys[rng.Intn(len(keys))]]
					score += a.score
					count += a.count
				}
				if count > 0 {
					values = append(values, score/float64(count))
				}
			}
			if len(values) >= 1000 {
				sort.Float64s(values)
				c.ClusterCI95 = &[2]float64{values[int(math.Floor(.025*float64(len(values)-1)))], values[int(math.Ceil(.975*float64(len(values)-1)))]}
			}
		}
		out = append(out, c)
	}
	return out
}

func ValidateEvaluation(report EvaluationReport) error {
	if len(report.UnverifiedSourceArtifacts) > 33 || !validGroupList(report.UnverifiedSourceArtifacts) {
		return fmt.Errorf("invalid unverified-source artifact manifest")
	}
	if e := validatePairing(report.Config.Pairing); e != nil {
		return e
	}
	size := familyGames(report.Config.Pairing)
	if report.Evidence != "" && !digest(report.Evidence) {
		return fmt.Errorf("invalid evaluation evidence identity")
	}
	schema := evaluationSchema(report.Config.Pairing)
	if report.MixedExperiment != nil {
		if err := report.MixedExperiment.validateEvaluationPart(report.Experiment); err != nil {
			return err
		}
		id, _ := Digest(*report.MixedExperiment)
		if id != report.MixedExperimentDigest {
			return fmt.Errorf("mixed evaluation envelope checksum mismatch")
		}
		schema = "commander-mixed-evaluation-v1"
	} else if report.MixedExperimentDigest != "" {
		return fmt.Errorf("mixed evaluation manifest missing")
	}
	if report.ValidationComparison != nil {
		x := report.ValidationComparison
		if err := x.Validate(); err != nil {
			return err
		}
		id, _ := Digest(*x)
		if id != report.ValidationComparisonDigest || report.MixedExperiment == nil || x.LeftDigest != report.MixedExperimentDigest || report.Split != "validation" || len(report.Comparisons) != 1 {
			return fmt.Errorf("mixed validation comparison provenance or split mismatch")
		}
		schema = "commander-mixed-comparison-evaluation-v1"
	} else if report.ValidationComparisonDigest != "" {
		return fmt.Errorf("mixed validation comparison declaration missing")
	}
	if report.Schema != schema || !digest(report.Candidate) || len(report.Games) == 0 || report.Config.MatchesPerOpponent < size || report.Config.MatchesPerOpponent%size != 0 {
		return fmt.Errorf("incomplete evaluation report")
	}
	if e := report.Environment.Validate(); e != nil {
		return e
	}
	if report.Experiment != nil {
		if e := report.Experiment.Validate(); e != nil {
			return e
		}
		x := report.Experiment
		if x.InitialRecorded != nil || len(x.RecordedSelectionArtifacts) > 0 && report.CandidateArtifact != x.InitialModel {
			found := false
			for _, id := range report.UnverifiedSourceArtifacts {
				found = found || id == report.CandidateArtifact
			}
			if !found {
				return fmt.Errorf("experiment recorded-source uncertainty missing from report")
			}
		}
		id, _ := Digest(*x)
		if id != report.ExperimentDigest || x.Environment != report.Environment || !digest(report.CandidateArtifact) {
			return fmt.Errorf("evaluation experiment provenance mismatch")
		}
		suite, e := x.evaluationSuite(report.Split)
		if e != nil {
			return e
		}
		want := experimentEvaluationConfig(*x, report.Split)
		if want != report.Config {
			return fmt.Errorf("evaluation settings differ from frozen split")
		}
		positions := map[string]int{}
		versions := map[string]string{}
		for _, g := range report.Games {
			i := positions[g.Opponent]
			if i >= len(suite) || !reflect.DeepEqual(g.Scenario, suite[i]) || g.CandidateSide != i%2 || versions[g.Opponent] != "" && versions[g.Opponent] != g.OpponentPolicy {
				return fmt.Errorf("evaluation games differ from frozen split schedule")
			}
			positions[g.Opponent]++
			versions[g.Opponent] = g.OpponentPolicy
		}
		for _, n := range positions {
			if n != len(suite) {
				return fmt.Errorf("evaluation split incomplete")
			}
		}
	} else if report.ExperimentDigest != "" || report.Split != "" {
		return fmt.Errorf("evaluation experiment manifest missing")
	}
	for _, g := range report.Games {
		if report.Evidence != "" && !digest(g.Shard) || report.Evidence == "" && g.Shard != "" {
			return fmt.Errorf("evaluation evidence and trajectory reference mismatch")
		}
		if e := g.Scenario.Validate(); e != nil {
			return e
		}
		if g.Group != ScenarioGroup(g.Scenario) || !digest(g.OpponentPolicy) || g.Opponent == "" || g.Terminated == g.Truncated || g.Winner < -1 || g.Winner > 1 || g.Truncated && g.Winner != -1 || g.CandidateSide < 0 || g.CandidateSide > 1 || g.Turns < 1 || g.Turns > g.Scenario.MaxTurns || g.Scenario.Mode != report.Config.Mode {
			return fmt.Errorf("invalid evaluation game")
		}
	}
	if !reflect.DeepEqual(report.Comparisons, Summarize(report.Games, report.Config.Seed)) {
		return fmt.Errorf("evaluation summaries do not match games")
	}
	for _, c := range report.Comparisons {
		if c.Games != report.Config.MatchesPerOpponent || c.Groups*size != c.Games {
			return fmt.Errorf("evaluation suite incomplete")
		}
	}
	if report.Config.Pairing == BalancedPairing {
		return validateBalancedGames(report.Games)
	}
	return nil
}

func SaveEvaluation(path string, report EvaluationReport) error {
	if e := ValidateEvaluation(report); e != nil {
		return e
	}

	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	return writeObject(path, report)
}
