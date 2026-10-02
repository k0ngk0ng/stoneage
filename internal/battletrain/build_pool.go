package battletrain

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// A pool freezes several complete team rosters, not a ranking assumed valid
// under a different combat policy. All source outcomes are reserved from the
// next independent evaluation, including those used for configuration choice.
type BuildPool struct {
	RecordedSelectionArtifacts []string            `json:"recorded_selection_artifacts,omitempty"`
	Schema                     string              `json:"schema"`
	Environment                battleenv.Metadata  `json:"environment"`
	Config                     BuildSearchConfig   `json:"search_config"`
	SourceState                string              `json:"source_state"`
	SourcePolicy               BuildPolicyIdentity `json:"source_policy"`
	Rosters                    []Roster            `json:"rosters"`
	SelectionGroups            []string            `json:"selection_groups"`
}

func sortedUnion(lists ...[]string) []string {
	seen := map[string]bool{}
	for _, list := range lists {
		for _, id := range list {
			seen[id] = true
		}
	}
	var out []string
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func validGroupList(list []string) bool {
	previous := ""
	for _, id := range list {
		if !digest(id) || id <= previous {
			return false
		}
		previous = id
	}
	return true
}

func (p BuildPool) Validate() error {
	if len(p.RecordedSelectionArtifacts) > 10000 || !validGroupList(p.RecordedSelectionArtifacts) {
		return fmt.Errorf("invalid build pool recorded selection sources")
	}
	if p.Schema != "commander-build-pool-v1" || !digest(p.SourceState) || !digest(p.SourcePolicy.Version) || len(p.Rosters) < 2 || len(p.Rosters) > 16 || len(p.SelectionGroups) == 0 || !validGroupList(p.SelectionGroups) {
		return fmt.Errorf("invalid frozen build pool")
	}
	if e := p.Environment.Validate(); e != nil {
		return e
	}
	if e := p.Config.Validate(); e != nil {
		return e
	}
	if p.SourcePolicy.Name == "" || (p.SourcePolicy.Rule == "") == (p.SourcePolicy.Artifact == "") || p.SourcePolicy.Artifact != "" && !digest(p.SourcePolicy.Artifact) {
		return fmt.Errorf("invalid pool source policy")
	}
	if p.SourcePolicy.Rule != "" {
		version, e := (Policy{Rule: p.SourcePolicy.Rule}).Version()
		if e != nil || version != p.SourcePolicy.Version {
			return fmt.Errorf("pool rule source mismatch")
		}
	}
	previous := ""
	for _, r := range p.Rosters {
		if e := r.validate(p.Config); e != nil {
			return e
		}
		id := r.ID()
		if id <= previous {
			return fmt.Errorf("build pool rosters must be unique and sorted")
		}
		previous = id
	}
	return nil
}

func ExportBuildPool(root, output string) (BuildPool, error) {
	var pool BuildPool
	s, e := LoadBuildSearch(root)
	if e != nil {
		return pool, e
	}
	if s.Stage != "complete" {
		return pool, fmt.Errorf("build search must complete before freezing a pool")
	}
	if e = verifyBuildSearchData(root, s); e != nil {
		return pool, e
	}
	pool = BuildPool{Schema: "commander-build-pool-v1", Environment: s.Spec.Environment, Config: s.Spec.Config, SourcePolicy: s.Spec.Controller}
	pool.RecordedSelectionArtifacts = append([]string(nil), s.Spec.RecordedSelectionArtifacts...)
	pool.SourceState, _ = Digest(s)
	for _, evaluation := range s.Validation {
		pool.Rosters = append(pool.Rosters, evaluation.Roster)
	}
	sort.Slice(pool.Rosters, func(i, j int) bool { return pool.Rosters[i].ID() < pool.Rosters[j].ID() })
	groups := append([]string(nil), s.Spec.ExcludedPolicyTrainingGroups...)
	for _, list := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		for _, evaluation := range list {
			for _, g := range evaluation.Games {
				groups = append(groups, ScenarioGroup(g.Scenario))
			}
		}
	}
	pool.SelectionGroups = sortedUnion(groups)
	if e = pool.Validate(); e != nil {
		return pool, e
	}
	if output == "" {
		output = filepath.Join(root, "build-pool.json")
	}
	if e = os.MkdirAll(filepath.Dir(output), 0700); e != nil {
		return pool, e
	}
	return pool, writeObject(output, pool)
}

func LoadBuildPool(path string) (BuildPool, error) {
	var pool BuildPool
	if e := readObject(path, &pool, 16<<20); e != nil {
		return pool, e
	}
	return pool, pool.Validate()
}

func (x Experiment) allowedTrainingGroups() map[string]bool {
	allowed := map[string]bool{}
	for _, id := range x.InitialTrainingGroups {
		allowed[id] = true
	}
	for _, f := range x.groups("train") {
		allowed[f.Group] = true
	}
	return allowed
}

func (x Experiment) validateSources() error {
	if x.PoolTrainingGroups != nil && (x.Pool == nil || *x.PoolTrainingGroups < 1 || *x.PoolTrainingGroups > 136 || x.Pairing != BalancedPairing) {
		return fmt.Errorf("pool mix requires a frozen pool, balanced pairing and 1..136 pool groups")
	}
	if len(x.RecordedSelectionArtifacts) > 10000 || !validGroupList(x.RecordedSelectionArtifacts) || x.Pool == nil && x.InitialModel == "" && len(x.RecordedSelectionArtifacts) > 0 {
		return fmt.Errorf("invalid experiment recorded selection sources")
	}
	if !validGroupList(x.InitialTrainingGroups) || !validGroupList(x.SelectionGroups) || (x.InitialModel == "") != (len(x.InitialTrainingGroups) == 0 && x.InitialRecorded == nil) || x.InitialModel != "" && !digest(x.InitialModel) {
		return fmt.Errorf("invalid experiment model ancestry")
	}
	if x.InitialRecorded != nil {
		if err := x.InitialRecorded.Validate(); err != nil {
			return err
		}
	}
	if x.Pool != nil {
		if e := x.Pool.Validate(); e != nil {
			return e
		}
		p := x.Pool
		if p.Environment != x.Environment || p.Config.PetSkillMask != x.PetSkillMask || p.Config.ReservePets != x.ReservePets || p.Config.HealingItems != x.HealingItems || p.Config.HealingMagic != x.HealingMagic || p.Config.Mode != x.Mode || p.Config.Points != x.Points || p.Config.PetPoints != x.PetPoints || p.Config.Level != x.Level {
			return fmt.Errorf("build pool settings differ from experiment")
		}
		if !reflect.DeepEqual(sortedUnion(x.SelectionGroups, p.SelectionGroups), x.SelectionGroups) {
			return fmt.Errorf("experiment dropped build-selection provenance")
		}
		if !reflect.DeepEqual(sortedUnion(x.RecordedSelectionArtifacts, p.RecordedSelectionArtifacts), x.RecordedSelectionArtifacts) {
			return fmt.Errorf("experiment dropped recorded build-selection sources")
		}
	} else if x.InitialModel == "" && len(x.SelectionGroups) > 0 {
		return fmt.Errorf("selection provenance without a pool or parent model")
	}
	return nil
}

// NewExperimentFromSources supports the alternating cycle: keep a build pool
// fixed during combat learning, optionally initialize from a frozen commander,
// and hold out new configurations from both gradient and selection history.
func NewExperimentFromSources(ctx context.Context, meta battleenv.Metadata, c EvaluationConfig, counts [3]int, pool *BuildPool, parent *battlepolicy.Artifact) (Experiment, error) {
	return newExperimentFromSources(ctx, meta, c, counts, pool, parent, nil)
}

// NewExperimentWithPoolMix freezes pool-pair and independently generated
// training families together. It is a family count, not a gradient weight.
// New manifests interleave the two populations at family boundaries. Loaded
// manifests retain their original order; their schedule must never be rewritten.
// Validation/test rosters remain outside the pool and every source exposure.
func NewExperimentWithPoolMix(ctx context.Context, meta battleenv.Metadata, c EvaluationConfig, counts [3]int, pool *BuildPool, parent *battlepolicy.Artifact, poolGroups int) (Experiment, error) {
	if pool == nil || poolGroups < 1 || poolGroups >= counts[0] || c.Pairing != BalancedPairing {
		return Experiment{}, fmt.Errorf("pool mix requires 0 < pool groups < total train groups and balanced pairing")
	}
	return newExperimentFromSources(ctx, meta, c, counts, pool, parent, &poolGroups)
}
func newExperimentFromSources(ctx context.Context, meta battleenv.Metadata, c EvaluationConfig, counts [3]int, pool *BuildPool, parent *battlepolicy.Artifact, poolGroups *int) (Experiment, error) {
	x := Experiment{Pairing: c.Pairing, Schema: experimentSchema(c.Pairing), Environment: meta, Seed: c.Seed, Mode: c.Mode, Points: c.Points, PetPoints: c.PetPoints, Level: c.Level, HealingMagic: c.HealingMagic, HealingItems: c.HealingItems, PetSkillMask: c.PetSkillMask, ReservePets: c.ReservePets, MaxTurns: c.MaxTurns, Pool: pool}
	if poolGroups != nil {
		n := *poolGroups
		x.PoolTrainingGroups = &n
		x.Schema = "commander-pool-mix-experiment-v1"
	}
	if e := x.validateSettings(); e != nil {
		return x, e
	}
	if pool != nil {
		x.SelectionGroups = append([]string(nil), pool.SelectionGroups...)
		x.RecordedSelectionArtifacts = append([]string(nil), pool.RecordedSelectionArtifacts...)
	}
	if parent != nil {
		if e := parent.Validate(); e != nil {
			return x, e
		}
		if parent.Experiment != "" && len(parent.HeldoutGroups) == 0 {
			return x, fmt.Errorf("parent predates held-out ancestry metadata; re-export it from its original training checkpoint")
		}
		if !parent.CompatibleNativeEnvironment(meta) {
			return x, fmt.Errorf("initial commander incompatible with experiment")
		}
		x.InitialModel, _ = Digest(*parent)
		x.InitialRecorded = parent.Recorded
		x.RecordedSelectionArtifacts = sortedUnion(x.RecordedSelectionArtifacts, parent.RecordedSelectionArtifacts)
		x.InitialTrainingGroups = append([]string(nil), parent.TrainingGroups...)
		x.SelectionGroups = sortedUnion(x.SelectionGroups, parent.SelectionGroups, parent.HeldoutGroups)
	}
	if e := x.validateSources(); e != nil {
		return x, e
	}
	if pool == nil && parent == nil {
		return NewExperiment(ctx, meta, c, counts)
	}
	n := 0
	for _, count := range counts {
		if count < 1 || count > 4096 {
			return x, fmt.Errorf("each experiment split requires 1..4096 groups")
		}
		n += count
	}
	if n > 4096 {
		return x, fmt.Errorf("experiment supports at most 4096 groups")
	}
	reserved, seen := map[string]bool{}, map[string]bool{}
	for _, id := range sortedUnion(x.InitialTrainingGroups, x.SelectionGroups) {
		reserved[id] = true
	}
	base := DefaultRunConfig()
	base.Pairing = c.Pairing
	base.Seed, base.Mode, base.Points, base.PetPoints, base.Level, base.MaxTurns = c.Seed, c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns
	base.HealingMagic = c.HealingMagic
	base.HealingItems = c.HealingItems
	base.ReservePets = c.ReservePets
	base.PetSkillMask = c.PetSkillMask
	poolIDs := map[string]bool{}
	if pool != nil {
		poolCount := counts[0]
		if poolGroups != nil {
			poolCount = *poolGroups
		}
		var families []ExperimentFamily
		for i, left := range pool.Rosters {
			poolIDs[left.ID()] = true
			for j := i; j < len(pool.Rosters); j++ {
				s := buildScenario(pool.Config, left, pool.Rosters[j], len(families), 0, 0)
				s.Seed = 1 + int(gameSeed(c.Seed, uint64(len(families)), 140)%2147483647)
				s.MaxTurns = c.MaxTurns
				families = append(families, ExperimentFamily{Group: ScenarioGroup(s), Split: "train", Scenario: s})
			}
		}
		if poolCount > len(families) {
			return x, fmt.Errorf("frozen pool has only %d distinct roster pairs; reduce --train-groups", len(families))
		}
		rng := rand.New(rand.NewSource(gameSeed(c.Seed, 0, 141)))
		rng.Shuffle(len(families), func(i, j int) { families[i], families[j] = families[j], families[i] })
		for _, f := range families[:poolCount] {
			x.Families = append(x.Families, f)
			seen[f.Group] = true
		}
	}
	for i := uint64(0); len(x.Families) < n; i++ {
		if e := ctx.Err(); e != nil {
			return x, e
		}
		if i >= uint64(n*100+10000) {
			return x, fmt.Errorf("not enough fresh experiment families outside ancestral/selection data")
		}
		s, group := scenarioFor(base, i*uint64(familyGames(c.Pairing)))
		if seen[group] || reserved[group] {
			continue
		}
		if pool != nil {
			known := false
			for side := 0; side < 2; side++ {
				r := Roster{Players: s.Builds[side*c.Mode : (side+1)*c.Mode]}
				if len(s.Reserves) > 0 {
					r.Reserves = s.Reserves[side*c.Mode : (side+1)*c.Mode]
				}
				if len(s.PetBuilds) > 0 {
					r.Pets = s.PetBuilds[side*c.Mode : (side+1)*c.Mode]
				}
				known = known || poolIDs[r.ID()]
			}
			if known {
				continue
			}
		}
		seen[group] = true
		split := "train"
		if len(x.Families) >= counts[0]+counts[1] {
			split = "test"
		} else if len(x.Families) >= counts[0] {
			split = "validation"
		}
		x.Families = append(x.Families, ExperimentFamily{Group: group, Split: split, Scenario: s})
	}
	if poolGroups != nil {
		// Generation above keeps pool families first so the RNG, fresh-family
		// identities, and held-out sets are independent of this ordering step.
		train := interleaveTrainingFamilies(x.Families[:*poolGroups], x.Families[*poolGroups:counts[0]])
		copy(x.Families[:counts[0]], train)
	}
	return x, x.Validate()
}

// interleaveTrainingFamilies retains each population's relative order and
// apportions pool families evenly: every prefix of k families contains
// floor(k*pool/total) pool families. Each family remains a complete paired
// block, so this does not split seed/side/roster pairs or rebalance gradients.
func interleaveTrainingFamilies(pool, fresh []ExperimentFamily) []ExperimentFamily {
	n := len(pool) + len(fresh)
	out := make([]ExperimentFamily, 0, n)
	i, j := 0, 0
	for k := 1; k <= n; k++ {
		if k*len(pool)/n > i {
			out = append(out, pool[i])
			i++
		} else {
			out = append(out, fresh[j])
			j++
		}
	}
	return out
}

func (x Experiment) heldoutGroups() []string {
	var ids []string
	for _, f := range x.Families {
		if f.Split != "train" {
			ids = append(ids, f.Group)
		}
	}
	return sortedUnion(ids)
}
