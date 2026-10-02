package battletrain

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
)

func buildFinalists(s BuildSearchState) []Roster {
	list := []Roster{balancedRoster(s.Spec.Config)}
	seen := map[string]bool{list[0].ID(): true}
	for pass := 0; pass < 2; pass++ {
		for _, r := range rankedBuilds(s.Search) {
			if len(list) >= s.Spec.Config.Finalists {
				break
			}
			if seen[r.Roster.ID()] {
				continue
			}
			diverse := true
			for _, prior := range list {
				a, b := prior.features(s.Spec.Config), r.Roster.features(s.Spec.Config)
				distance := 0.
				for i := range a {
					distance += math.Abs(float64(a[i] - b[i]))
				}
				if distance/(float64(len(a)/4)*2) < .1 {
					diverse = false
				}
			}
			if pass == 1 || diverse || len(list) == 1 {
				list = append(list, r.Roster)
				seen[r.Roster.ID()] = true
			}
		}
	}
	return list
}

func buildFinalSelection(s BuildSearchState) Roster {
	best := rankedBuilds(s.Validation)[0]
	score, _ := buildScore(best)
	baseline := balancedRoster(s.Spec.Config)
	for _, e := range s.Validation {
		value, _ := buildScore(e)
		if e.Roster.ID() == baseline.ID() && value == score {
			return baseline // no measured reason to change on an exact tie.
		}
	}
	return best.Roster
}

func validateBuildSearch(s BuildSearchState) error {
	x, c := s.Spec, s.Spec.Config
	if len(x.RecordedSelectionArtifacts) > 10000 || !validGroupList(x.RecordedSelectionArtifacts) {
		return fmt.Errorf("invalid recorded build selection sources")
	}
	if x.Schema != "build-search-v1" {
		return fmt.Errorf("invalid build search schema")
	}
	if e := c.Validate(); e != nil {
		return e
	}
	if e := x.Environment.Validate(); e != nil {
		return e
	}
	if len(x.Opponents) < 1 || len(x.Opponents) > 16 || s.Generation < 0 || s.Generation > c.Generations {
		return fmt.Errorf("invalid build search progress/opponents")
	}
	names := map[string]bool{}
	for i, id := range append([]BuildPolicyIdentity{x.Controller}, x.Opponents...) {
		if id.Name == "" || !digest(id.Version) || (id.Rule == "") == (id.Artifact == "") || id.Artifact != "" && !digest(id.Artifact) || i > 0 && names[id.Name] {
			return fmt.Errorf("invalid frozen build policy identities")
		}
		if id.Rule != "" {
			version, e := (Policy{Rule: id.Rule}).Version()
			if e != nil || version != id.Version {
				return fmt.Errorf("build rule identity mismatch")
			}
		}
		if i > 0 {
			names[id.Name] = true
		}
	}
	seen := map[string]bool{}
	previous := ""
	excluded := map[string]bool{}
	for _, group := range x.ExcludedPolicyTrainingGroups {
		if !digest(group) || group <= previous {
			return fmt.Errorf("invalid excluded combat-policy training groups")
		}
		excluded[group] = true
		previous = group
	}
	for split, rosters := range [][]Roster{x.Search, x.Validation, x.Test} {
		if len(rosters) != []int{c.SearchGroups, c.ValidationGroups, c.TestGroups}[split] {
			return fmt.Errorf("build search opponent distribution changed")
		}
		for _, roster := range rosters {
			if e := roster.validate(c); e != nil {
				return e
			}
			if seen[roster.ID()] {
				return fmt.Errorf("opponent roster leaked across build search partitions")
			}
			seen[roster.ID()] = true
		}
	}
	initial, e := initialRosters(c)
	if e != nil {
		return e
	}
	if len(s.Search) > c.InitialCandidates+s.Generation*c.NativeCandidates {
		return fmt.Errorf("too many measured search candidates")
	}
	for i := 0; i < min(len(s.Search), len(initial)); i++ {
		if s.Search[i].Roster.ID() != initial[i].ID() {
			return fmt.Errorf("initial search roster schedule changed")
		}
	}
	if s.Generation == 0 {
		if s.Scorer != nil || len(s.ScorerTrainingRosters) != 0 {
			return fmt.Errorf("scorer fitted before initial sampling")
		}
	} else {
		n := c.InitialCandidates + (s.Generation-1)*c.NativeCandidates
		if len(s.Search) < n || len(s.ScorerTrainingRosters) != n {
			return fmt.Errorf("build scorer training provenance incomplete")
		}
		if e := s.Scorer.Validate(); e != nil {
			return e
		}
		inputs := c.Mode * 4
		if c.PetPoints > 0 {
			inputs *= 2
		}
		reserveFeatures := 11
		if c.PetSkillMask != 0 {
			reserveFeatures++ // The explicitly configured v8 skill vocabulary includes guardian.
		}
		inputs += c.Mode * c.ReservePets * reserveFeatures
		if s.Scorer.Inputs != inputs {
			return fmt.Errorf("build scorer feature dimensions mismatch")
		}
		for i, id := range s.ScorerTrainingRosters {
			if id != s.Search[i].Roster.ID() {
				return fmt.Errorf("build scorer learned outside its search prefix")
			}
		}
	}
	stage := -1
	switch s.Stage {
	case "search":
		stage = 0
	case "validation":
		stage = 1
	case "test":
		stage = 2
	case "complete":
		stage = 3
	}
	if stage < 0 {
		return fmt.Errorf("invalid build search stage")
	}
	if stage > 0 && (s.Generation != c.Generations || len(s.Search) != c.InitialCandidates+c.Generations*c.NativeCandidates) {
		return fmt.Errorf("build validation before search completion")
	}
	if stage < 1 && len(s.Validation) != 0 || stage < 2 && (len(s.Test) != 0 || s.Selected != "") {
		return fmt.Errorf("held-out results observed before selection")
	}
	finalists := buildFinalists(s)
	if len(s.Validation) > c.Finalists || stage > 1 && len(s.Validation) != c.Finalists {
		return fmt.Errorf("build finalist evaluation incomplete")
	}
	for i, result := range s.Validation {
		if i >= len(finalists) || result.Roster.ID() != finalists[i].ID() {
			return fmt.Errorf("build finalist schedule changed")
		}
	}
	var test []Roster
	if stage >= 2 {
		selected := buildFinalSelection(s)
		if s.Selected != selected.ID() {
			return fmt.Errorf("selected build differs from validation choice")
		}
		test = []Roster{selected}
		baseline := balancedRoster(c)
		if baseline.ID() != s.Selected {
			test = append(test, baseline)
		}
	}
	if len(s.Test) > len(test) || stage == 3 && len(s.Test) != len(test) {
		return fmt.Errorf("build final test incomplete")
	}
	for i, result := range s.Test {
		if result.Roster.ID() != test[i].ID() {
			return fmt.Errorf("build final choice changed after testing")
		}
	}
	var pending []Roster
	switch stage {
	case 0:
		if s.Generation == 0 {
			pending = initial
		} else {
			if len(s.Pending) != c.NativeCandidates {
				return fmt.Errorf("build proposal count mismatch")
			}
			pending = s.Pending // hash-pinned before any of its games are observed.
			start := c.InitialCandidates + (s.Generation-1)*c.NativeCandidates
			for i := start; i < len(s.Search); i++ {
				if s.Search[i].Roster.ID() != pending[i-start].ID() {
					return fmt.Errorf("measured build differs from proposal schedule")
				}
			}
		}
	case 1:
		pending = finalists
	case 2:
		pending = test
	}
	if !reflect.DeepEqual(s.Pending, pending) {
		return fmt.Errorf("build search pending schedule mismatch")
	}
	pendingIDs := map[string]bool{}
	for _, r := range pending {
		if e := r.validate(c); e != nil {
			return e
		}
		if pendingIDs[r.ID()] {
			return fmt.Errorf("duplicate pending build")
		}
		pendingIDs[r.ID()] = true
	}
	for split, evaluations := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		enemies := [][]Roster{x.Search, x.Validation, x.Test}[split]
		seen := map[string]bool{}
		for _, evaluation := range evaluations {
			if e := evaluation.Roster.validate(c); e != nil {
				return e
			}
			if seen[evaluation.Roster.ID()] {
				return fmt.Errorf("duplicate measured build")
			}
			seen[evaluation.Roster.ID()] = true
			limit := len(enemies) * len(x.Opponents) * 4
			if len(evaluation.Games) > limit || (split < stage || !pendingIDs[evaluation.Roster.ID()]) && len(evaluation.Games) != limit {
				return fmt.Errorf("build game schedule incomplete")
			}
			for game, g := range evaluation.Games {
				oi, group, repeat := game/(len(enemies)*4), (game/4)%len(enemies), game%4
				want := buildScenario(c, evaluation.Roster, enemies[group], group, repeat, split)
				if split > 0 && excluded[ScenarioGroup(want)] {
					return fmt.Errorf("held-out build scenario was used to train a combat policy")
				}
				if !reflect.DeepEqual(g.Scenario, want) || g.CandidateSide != repeat%2 || g.Group != enemies[group].ID() || g.Opponent != x.Opponents[oi].Name || g.OpponentPolicy != x.Opponents[oi].Version || !digest(g.Shard) || g.Winner < -1 || g.Winner > 1 || g.Terminated == g.Truncated || g.Truncated && g.Winner != -1 || g.Turns < 1 || g.Turns > c.MaxTurns {
					return fmt.Errorf("build game metadata differs from frozen schedule")
				}
				if split < stage && g.Truncated {
					return fmt.Errorf("build selection used truncated comparison")
				}
			}
		}
	}
	return nil
}

// Recheck references against immutable native trajectories on resume/report.
// A saved summary is never sufficient evidence for the scoring labels.
func verifyBuildSearchData(root string, s BuildSearchState) error {
	return verifyBuildSearchDataContext(context.Background(), root, s)
}

func verifyBuildSearchDataContext(ctx context.Context, root string, s BuildSearchState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Rebuild all source exclusions from the pinned policies before trusting
	// the search state. A valid state checksum alone cannot prove provenance.
	controller, err := LoadBuildSearchPolicy(root, s.Spec.Controller)
	if err != nil {
		return err
	}
	var opponents []Opponent
	for _, id := range s.Spec.Opponents {
		o, err := LoadBuildSearchPolicy(root, id)
		if err != nil {
			return err
		}
		opponents = append(opponents, o)
	}
	expected, _, _, err := newBuildSearchSpec(s.Spec.Config, s.Spec.Environment, controller, opponents)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, s.Spec) {
		return fmt.Errorf("build-search specification differs from frozen source policies")
	}
	seen := map[string]bool{}
	for _, results := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		for _, result := range results {
			for _, game := range result.Games {
				if err := ctx.Err(); err != nil {
					return err
				}
				if seen[game.Shard] {
					return fmt.Errorf("duplicate build search game shard")
				}
				seen[game.Shard] = true
				tr, _, e := LoadShard(filepath.Join(root, "shards"), game.Shard)
				if e != nil {
					return e
				}
				if len(tr) != 2 {
					return fmt.Errorf("build game requires both native perspectives")
				}
				for side, t := range tr {
					policy, opponent := s.Spec.Controller.Version, game.OpponentPolicy
					if side != game.CandidateSide {
						policy, opponent = opponent, policy
					}
					if t.Side != side || t.Policy != policy || t.Opponent != opponent || t.Rules != s.Spec.Environment.Rules || t.Platform != s.Spec.Environment.Platform || t.Environment != s.Spec.Environment.Scenario || !reflect.DeepEqual(t.Setup, game.Scenario) || t.Winner != game.Winner || t.Terminated != game.Terminated || t.Truncated != game.Truncated || len(t.Steps) != game.Turns {
						return fmt.Errorf("build result differs from referenced native trajectory")
					}
				}
			}
		}
	}
	return nil
}
