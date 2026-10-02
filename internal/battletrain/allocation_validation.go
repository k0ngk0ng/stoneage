package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const allocationValidationSchema = "commander-allocation-validation-v1"
const allocationValidationBytes = 64 << 20

// AllocationValidation freezes a supplementary validation comparison before
// child training. The same two owned rosters face the same opponents under the
// declared parent and its child. This is distinct from Experiment's unseen-pair
// evaluation and never releases its reserved tests or grants champion status.
// Source retains the completed search, including the validation-only choice;
// its test results cannot select a different roster for this comparison.
// Hidden allocations define the environment, not inputs to the commander.
type AllocationValidation struct {
	Schema           string           `json:"schema"`
	Experiment       Experiment       `json:"experiment"`
	ExperimentDigest string           `json:"experiment_digest"`
	Source           BuildSearchState `json:"source_search"`
	Seed             int64            `json:"seed"`
	Groups           int              `json:"groups"`
	OpponentRosters  []Roster         `json:"opponent_rosters"`
}

type AllocationValidationGame struct {
	Allocation     string              `json:"allocation"`
	Opponent       BuildPolicyIdentity `json:"opponent"`
	OpponentRoster string              `json:"opponent_roster"` // statistical cluster, not ScenarioGroup
	Scenario       battleenv.Scenario  `json:"scenario"`
	CandidateSide  int                 `json:"candidate_side"`
}

// NewAllocationValidation requires the exact pool's completed source search.
// Actual collection must also verify source shards/policies on disk; semantic
// manifest validation alone is not evidence that these games were played.
func NewAllocationValidation(ctx context.Context, x Experiment, source BuildSearchState, parent battlepolicy.Artifact, seed int64, groups int) (AllocationValidation, error) {
	v := AllocationValidation{Schema: allocationValidationSchema, Experiment: x, Source: source, Seed: seed, Groups: groups}
	var err error
	v.ExperimentDigest, err = Digest(x)
	if err != nil {
		return v, err
	}
	// Take ownership of every nested roster, pointer, source result and tensor.
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	if len(raw) > allocationValidationBytes {
		return v, fmt.Errorf("allocation validation exceeds size limit")
	}
	var owned AllocationValidation
	if err = json.Unmarshal(raw, &owned); err != nil {
		return owned, err
	}
	v = owned
	if err = v.validateSources(); err != nil {
		return v, err
	}
	if err = v.Experiment.validateInitialModel(&parent); err != nil {
		return v, err
	}
	v.OpponentRosters, err = v.generateOpponents(ctx)
	if err != nil {
		return v, err
	}
	return v, v.ValidateCandidate(parent)
}

func (v AllocationValidation) validateSources() error {
	if v.Schema != allocationValidationSchema || v.Groups < 1 || v.Groups > 512 {
		return fmt.Errorf("invalid allocation validation schema/group count")
	}
	x := v.Experiment
	if err := x.Validate(); err != nil {
		return err
	}
	id, err := Digest(x)
	if err != nil || id != v.ExperimentDigest || x.Pool == nil || x.InitialModel == "" {
		return fmt.Errorf("allocation validation requires its exact pooled parent experiment")
	}
	if err := validateBuildSearch(v.Source); err != nil {
		return err
	}
	if v.Source.Stage != "complete" {
		return fmt.Errorf("allocation validation requires a completed source search")
	}
	id, err = Digest(v.Source)
	if err != nil || id != x.Pool.SourceState {
		return fmt.Errorf("allocation validation source differs from frozen pool")
	}
	s, p := v.Source, x.Pool
	if s.Spec.Environment != p.Environment || s.Spec.Config != p.Config || s.Spec.Controller != p.SourcePolicy || !reflect.DeepEqual(s.Spec.RecordedSelectionArtifacts, p.RecordedSelectionArtifacts) {
		return fmt.Errorf("allocation validation pool source metadata mismatch")
	}
	var rosters []Roster
	groups := append([]string(nil), s.Spec.ExcludedPolicyTrainingGroups...)
	for _, e := range s.Validation {
		rosters = append(rosters, e.Roster)
	}
	sort.Slice(rosters, func(i, j int) bool { return rosters[i].ID() < rosters[j].ID() })
	for _, list := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		for _, e := range list {
			for _, g := range e.Games {
				groups = append(groups, ScenarioGroup(g.Scenario))
			}
		}
	}
	if !reflect.DeepEqual(rosters, p.Rosters) || !reflect.DeepEqual(sortedUnion(groups), p.SelectionGroups) {
		return fmt.Errorf("allocation validation pool dropped source exposure or finalists")
	}
	if v.Groups*len(s.Spec.Opponents)*16 > 200000 {
		return fmt.Errorf("allocation validation exceeds 200000 games for four arms")
	}
	return nil
}

func rosterSide(s battleenv.Scenario, side int) Roster {
	start, end := side*s.Mode, (side+1)*s.Mode
	r := Roster{Players: s.Builds[start:end]}
	if len(s.PetBuilds) > 0 {
		r.Pets = s.PetBuilds[start:end]
	}
	if len(s.Reserves) > 0 {
		r.Reserves = s.Reserves[start:end]
	}
	return r
}

func (v AllocationValidation) config() BuildSearchConfig {
	c := v.Source.Spec.Config
	c.Seed = v.Seed
	c.MaxTurns = v.Experiment.MaxTurns
	return c
}
func (v AllocationValidation) ownedRosters() []Roster {
	return []Roster{balancedRoster(v.Source.Spec.Config), buildFinalSelection(v.Source)}
}

func (v AllocationValidation) generateOpponents(ctx context.Context) ([]Roster, error) {
	c := v.config()
	seen, excluded := map[string]bool{}, map[string]bool{}
	for _, list := range [][]Roster{v.Source.Spec.Search, v.Source.Spec.Validation, v.Source.Spec.Test} {
		for _, r := range list {
			seen[r.ID()] = true
		}
	}
	for _, list := range [][]BuildEvaluation{v.Source.Search, v.Source.Validation, v.Source.Test} {
		for _, e := range list {
			seen[e.Roster.ID()] = true
		}
	}
	x := v.Experiment
	for _, g := range sortedUnion(x.InitialTrainingGroups, x.SelectionGroups) {
		excluded[g] = true
	}
	for _, f := range x.Families {
		excluded[f.Group] = true
		for side := 0; side < 2; side++ {
			seen[rosterSide(f.Scenario, side).ID()] = true
		}
	}
	owned := v.ownedRosters()
	rng := rand.New(rand.NewSource(gameSeed(v.Seed, 0, 161)))
	var result []Roster
	for attempt := 0; len(result) < v.Groups; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt >= v.Groups*1000+10000 {
			return nil, fmt.Errorf("not enough fresh allocation validation opponents")
		}
		r := randomRoster(c, rng)
		if seen[r.ID()] {
			continue
		}
		seen[r.ID()] = true
		overlap := false
		for _, own := range owned {
			if excluded[ScenarioGroup(buildScenario(c, own, r, len(result), 0, 3))] {
				overlap = true
			}
		}
		if !overlap {
			result = append(result, r)
		}
	}
	return result, nil
}

func (v AllocationValidation) Validate() error {
	if err := v.validateSources(); err != nil {
		return err
	}
	want, err := v.generateOpponents(context.Background())
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, v.OpponentRosters) {
		return fmt.Errorf("allocation validation opponent schedule changed")
	}
	return nil
}

// Schedule retains the candidate's allocation when swapping arena sides. Each
// opponent roster is one cluster of all rules, two seeds and both arena sides.
// Parent and child must consume this identical schedule without relabeling their
// model artifacts or Experiment's existing eight-game held-out families.
func (v AllocationValidation) Schedule() ([]AllocationValidationGame, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	c := v.config()
	var out []AllocationValidationGame
	for ai, own := range v.ownedRosters() {
		for _, opponent := range v.Source.Spec.Opponents {
			for i, r := range v.OpponentRosters {
				for repeat := 0; repeat < 4; repeat++ {
					out = append(out, AllocationValidationGame{Allocation: []string{"balanced", "selected"}[ai], Opponent: opponent, OpponentRoster: r.ID(), Scenario: buildScenario(c, own, r, i, repeat, 3), CandidateSide: repeat % 2})
				}
			}
		}
	}
	return out, nil
}

func (v AllocationValidation) ValidateCandidate(a battlepolicy.Artifact) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.CompatibleNativeEnvironment(v.Experiment.Environment) || !supports(a, v.Experiment.Mode) {
		return fmt.Errorf("allocation validation model environment/mode mismatch")
	}
	if err := v.Experiment.validateEvaluationCandidate(a, "validation"); err != nil {
		return err
	}
	// Supplementary validation never silently borrows the child's reserved tests.
	excluded := map[string]bool{}
	for _, id := range sortedUnion(a.DataGroups(), a.HeldoutGroups) {
		excluded[id] = true
	}
	c := v.config()
	for _, own := range v.ownedRosters() {
		for i, r := range v.OpponentRosters {
			if excluded[ScenarioGroup(buildScenario(c, own, r, i, 0, 3))] {
				return fmt.Errorf("allocation validation overlaps candidate training/selection/held-out exposure")
			}
		}
	}
	return nil
}

func SaveAllocationValidation(path string, v AllocationValidation) (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(raw) > allocationValidationBytes {
		return "", fmt.Errorf("allocation validation exceeds size limit")
	}
	id, err := Digest(v)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return id, writeObject(path, v)
}
func LoadAllocationValidation(path string) (AllocationValidation, error) {
	var v AllocationValidation
	if err := readObject(path, &v, allocationValidationBytes); err != nil {
		return v, err
	}
	return v, v.Validate()
}

// NewAllocationValidationFromSearch validates disk source evidence before a
// user-facing command publishes its immutable supplementary manifest.
func NewAllocationValidationFromSearch(ctx context.Context, x Experiment, parent battlepolicy.Artifact, sourceRoot string, seed int64, groups int) (AllocationValidation, error) {
	var empty AllocationValidation
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	s, err := LoadBuildSearch(sourceRoot)
	if err != nil {
		return empty, err
	}
	v, err := NewAllocationValidation(ctx, x, s, parent, seed, groups)
	if err != nil {
		return v, err
	}
	return v, verifyAllocationSource(ctx, sourceRoot, v)
}
