package battletrain

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Experiment freezes complete configuration families before training. Seeds,
// side swaps, both viewpoints and turns never define separate partitions.
// The manifest contains initial conditions, not hidden inputs to the policy.
type Experiment struct {
	// Nil preserves the original pool-only training contract.
	PoolTrainingGroups         *int                           `json:"pool_training_groups,omitempty"`
	RecordedSelectionArtifacts []string                       `json:"recorded_selection_artifacts,omitempty"`
	InitialRecorded            *battlepolicy.RecordedTraining `json:"initial_recorded_training,omitempty"`
	PetSkillMask               int                            `json:"pet_skill_mask,omitempty"` // Optional active set; zero preserves the original seven skills.
	Pairing                    string                         `json:"pairing,omitempty"`
	ReservePets                int                            `json:"reserve_pets,omitempty"`  // Extra pets per member, 0..2.
	HealingItems               int                            `json:"healing_items,omitempty"` // Number of single-use template 1234 items per character, 0..15.
	HealingMagic               int                            `json:"healing_magic,omitempty"`
	Schema                     string                         `json:"schema"`
	Environment                battleenv.Metadata             `json:"environment"`
	Seed                       int64                          `json:"seed"`
	Mode                       int                            `json:"mode"`
	Points                     int                            `json:"points"`
	PetPoints                  int                            `json:"pet_points"`
	Level                      int                            `json:"level"`
	MaxTurns                   int                            `json:"max_turns"`
	Families                   []ExperimentFamily             `json:"families"`
	Pool                       *BuildPool                     `json:"build_pool,omitempty"`
	InitialModel               string                         `json:"initial_model,omitempty"`
	InitialTrainingGroups      []string                       `json:"initial_training_groups,omitempty"`
	SelectionGroups            []string                       `json:"selection_groups,omitempty"`
}

type ExperimentFamily struct {
	Group    string             `json:"group"`
	Split    string             `json:"split"`
	Scenario battleenv.Scenario `json:"scenario"`
}

func NewExperiment(ctx context.Context, meta battleenv.Metadata, c EvaluationConfig, counts [3]int) (Experiment, error) {
	x := Experiment{Pairing: c.Pairing, Schema: experimentSchema(c.Pairing), Environment: meta, Seed: c.Seed, Mode: c.Mode, Points: c.Points, PetPoints: c.PetPoints, Level: c.Level, HealingMagic: c.HealingMagic, HealingItems: c.HealingItems, PetSkillMask: c.PetSkillMask, ReservePets: c.ReservePets, MaxTurns: c.MaxTurns}
	if e := x.validateSettings(); e != nil {
		return x, e
	}
	n := 0
	for _, count := range counts {
		if count < 1 || count > 4096 {
			return x, fmt.Errorf("each split requires 1..4096 configuration families")
		}
		n += count
	}
	if n > 4096 {
		return x, fmt.Errorf("experiment supports at most 4096 configuration families")
	}
	base := DefaultRunConfig()
	base.Pairing = c.Pairing
	base.Seed, base.Mode, base.Points, base.PetPoints, base.Level, base.MaxTurns = c.Seed, c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns
	base.HealingMagic = c.HealingMagic
	base.HealingItems = c.HealingItems
	base.ReservePets = c.ReservePets
	base.PetSkillMask = c.PetSkillMask
	seen := map[string]bool{}
	for i := uint64(0); len(x.Families) < n; i++ {
		if e := ctx.Err(); e != nil {
			return x, e
		}
		if i >= uint64(n*100+10000) {
			return x, fmt.Errorf("not enough distinct configurations for disjoint experiment splits")
		}
		s, group := scenarioFor(base, i*uint64(familyGames(c.Pairing)))
		if seen[group] {
			continue
		}
		seen[group] = true
		x.Families = append(x.Families, ExperimentFamily{Group: group, Scenario: s})
	}
	// Random partition after generation avoids assigning early generator
	// patterns preferentially to training. The ordered manifest is immutable.
	rng := rand.New(rand.NewSource(gameSeed(c.Seed, 0, 70)))
	rng.Shuffle(len(x.Families), func(i, j int) { x.Families[i], x.Families[j] = x.Families[j], x.Families[i] })
	for i := range x.Families {
		split := "train"
		if i >= counts[0]+counts[1] {
			split = "test"
		} else if i >= counts[0] {
			split = "validation"
		}
		x.Families[i].Split = split
	}
	return x, x.Validate()
}

func (x Experiment) validateSettings() error {
	if x.PetSkillMask != 0 && x.Environment.Scenario != "controlled-battle-v8" {
		return fmt.Errorf("active pet skill configuration requires controlled-battle-v8")
	}
	if e := validatePairing(x.Pairing); e != nil {
		return e
	}
	wantSchema := experimentSchema(x.Pairing)
	if x.PoolTrainingGroups != nil {
		wantSchema = "commander-pool-mix-experiment-v1"
	}
	if x.Schema != wantSchema {
		return fmt.Errorf("unsupported experiment schema")
	}
	if e := x.Environment.Validate(); e != nil {
		return e
	}
	c := DefaultRunConfig()
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = x.Mode, x.Points, x.PetPoints, x.Level, x.MaxTurns
	c.HealingMagic = x.HealingMagic
	c.HealingItems = x.HealingItems
	c.ReservePets = x.ReservePets
	c.PetSkillMask = x.PetSkillMask
	return c.Validate()
}

func (x Experiment) Validate() error {
	if e := x.validateSettings(); e != nil {
		return e
	}
	if e := x.validateSources(); e != nil {
		return e
	}
	if len(x.Families) < 3 || len(x.Families) > 4096 {
		return fmt.Errorf("invalid experiment size")
	}
	counts, seen := map[string]int{}, map[string]bool{}
	poolTrainingGroups := 0
	reserved, poolIDs := map[string]bool{}, map[string]bool{}
	for _, id := range sortedUnion(x.InitialTrainingGroups, x.SelectionGroups) {
		reserved[id] = true
	}
	if x.Pool != nil {
		for _, r := range x.Pool.Rosters {
			poolIDs[r.ID()] = true
		}
	}
	for _, f := range x.Families {
		if f.Split != "train" && f.Split != "validation" && f.Split != "test" {
			return fmt.Errorf("unknown experiment split %q", f.Split)
		}
		s := f.Scenario
		if e := s.ValidateEnvironment(x.Environment.Scenario); e != nil {
			return e
		}
		if f.Split != "train" && reserved[f.Group] {
			return fmt.Errorf("held-out experiment family overlaps earlier training/selection")
		}
		if x.Pool != nil && s.Mode == x.Mode {
			var inside [2]bool
			for side := 0; side < 2; side++ {
				r := Roster{Players: s.Builds[side*x.Mode : (side+1)*x.Mode]}
				if len(s.Reserves) > 0 {
					r.Reserves = s.Reserves[side*x.Mode : (side+1)*x.Mode]
				}
				if len(s.PetBuilds) > 0 {
					r.Pets = s.PetBuilds[side*x.Mode : (side+1)*x.Mode]
				}
				inside[side] = poolIDs[r.ID()]
			}
			if f.Split != "train" && (inside[0] || inside[1]) ||
				f.Split == "train" && (inside[0] != inside[1] || x.PoolTrainingGroups == nil && !inside[0]) {
				return fmt.Errorf("experiment family violates frozen-pool/held-out roster boundary")
			}
			if f.Split == "train" && inside[0] {
				poolTrainingGroups++
			} else if f.Split == "train" && x.PoolTrainingGroups != nil && reserved[f.Group] {
				return fmt.Errorf("generated pool-mix training family overlaps earlier training/selection")
			}
		}
		if !scenarioPetSkillsMatch(s, x.PetSkillMask) || f.Group != ScenarioGroup(s) || seen[f.Group] || reserveCount(s.Reserves) != x.ReservePets || s.HealingItems != x.HealingItems || s.HealingMagic != x.HealingMagic || s.Mode != x.Mode || s.Level != x.Level || s.MaxTurns != x.MaxTurns || (len(s.PetBuilds) == 0) != (x.PetPoints == 0) {
			return fmt.Errorf("duplicate, relabeled or incompatible experiment family")
		}
		for j, builds := range [][]battleenv.Build{s.Builds, s.PetBuilds} {
			want := x.Points
			if j == 1 {
				want = x.PetPoints
			}
			for _, b := range builds {
				if b[0]+b[1]+b[2]+b[3] != want {
					return fmt.Errorf("experiment allocation budget mismatch")
				}
			}
		}
		seen[f.Group] = true
		counts[f.Split]++
	}
	if x.PoolTrainingGroups != nil && (poolTrainingGroups != *x.PoolTrainingGroups || counts["train"] <= poolTrainingGroups) {
		return fmt.Errorf("pool mix must retain its declared pool groups and fresh training groups")
	}
	if counts["train"] == 0 || counts["validation"] == 0 || counts["test"] == 0 {
		return fmt.Errorf("experiment requires all three nonempty splits")
	}
	return nil
}

func (x Experiment) groups(split string) []ExperimentFamily {
	var out []ExperimentFamily
	for _, f := range x.Families {
		if f.Split == split {
			out = append(out, f)
		}
	}
	return out
}

func (x Experiment) matches(c RunConfig) bool {
	return x.Pairing == c.Pairing && x.PetSkillMask == c.PetSkillMask && x.ReservePets == c.ReservePets && x.HealingItems == c.HealingItems && x.HealingMagic == c.HealingMagic && x.Mode == c.Mode && x.Points == c.Points && x.PetPoints == c.PetPoints && x.Level == c.Level && x.MaxTurns == c.MaxTurns
}

func swapScenario(s *battleenv.Scenario) {
	s.Builds = append([]battleenv.Build(nil), s.Builds...)
	s.PetBuilds = append([]battleenv.Build(nil), s.PetBuilds...)
	s.Reserves = battleenv.CloneReserves(s.Reserves)
	s.PetSkillMasks = append([]int(nil), s.PetSkillMasks...)
	for i := 0; i < s.Mode; i++ {
		if len(s.PetSkillMasks) > 0 {
			s.PetSkillMasks[i], s.PetSkillMasks[i+s.Mode] = s.PetSkillMasks[i+s.Mode], s.PetSkillMasks[i]
		}
		if len(s.Reserves) > 0 {
			s.Reserves[i], s.Reserves[i+s.Mode] = s.Reserves[i+s.Mode], s.Reserves[i]
		}
		s.Builds[i], s.Builds[i+s.Mode] = s.Builds[i+s.Mode], s.Builds[i]
		if len(s.PetBuilds) != 0 {
			s.PetBuilds[i], s.PetBuilds[i+s.Mode] = s.PetBuilds[i+s.Mode], s.PetBuilds[i]
		}
	}
}

func (x Experiment) trainingScenario(seed int64, game uint64) (battleenv.Scenario, string) {
	families := x.groups("train") // validated before use; deterministic on resume.
	size := uint64(familyGames(x.Pairing))
	f := families[(game/size)%uint64(len(families))]
	s := f.Scenario
	s.Seed = 1 + int(gameSeed(seed, game/(size/2), 72)%2147483647)
	if pairingSwap(x.Pairing, game) {
		swapScenario(&s)
	}
	return s, f.Group
}

func (x Experiment) evaluationSuite(split string) ([]battleenv.Scenario, error) {
	if split != "validation" && split != "test" {
		return nil, fmt.Errorf("evaluation split must be validation or test")
	}
	var suite []battleenv.Scenario
	size := familyGames(x.Pairing)
	for i, f := range x.groups(split) {
		for k := 0; k < size; k++ {
			s := f.Scenario
			s.Seed = 1 + int(gameSeed(x.Seed, uint64(i*2+k/(size/2)), 73)%2147483647)
			if pairingSwap(x.Pairing, uint64(k)) {
				swapScenario(&s)
			}
			suite = append(suite, s)
		}
	}
	return suite, nil
}

func SaveExperiment(path string, x Experiment) (string, error) {
	if e := x.Validate(); e != nil {
		return "", e
	}
	id, e := Digest(x)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	return id, writeObject(path, x)
}

func LoadExperiment(path string) (Experiment, error) {
	var x Experiment
	if e := readObject(path, &x, 16<<20); e != nil {
		return x, e
	}
	return x, x.Validate()
}

func loadRunExperiment(root string, c RunConfig) (*Experiment, error) {
	if c.Experiment == "" {
		return nil, nil
	}
	x, e := LoadExperiment(filepath.Join(root, "experiments", c.Experiment+".json"))
	if e != nil {
		return nil, e
	}
	id, _ := Digest(x)
	if id != c.Experiment || !x.matches(c) {
		return nil, fmt.Errorf("experiment checksum/settings differ from checkpoint")
	}
	return &x, nil
}

// FreezeFinalSelection records the chosen model/opponents BEFORE test results
// are observed. Same-selection retries are allowed after interruption; another
// candidate or opponent list cannot overwrite the experiment's final choice.
// This is an accidental-reuse guard, not protection against manual deletion.
func FreezeFinalSelection(path string, x Experiment, candidate battlepolicy.Artifact, opponents []Opponent) error {
	if e := x.Validate(); e != nil {
		return e
	}
	if e := candidate.Validate(); e != nil {
		return e
	}
	id, _ := Digest(x)
	if candidate.Experiment != id || candidate.Environment != x.Environment || !supports(candidate, x.Mode) || len(opponents) == 0 || len(opponents) > 32 {
		return fmt.Errorf("final selection requires compatible experiment candidate and opponents")
	}
	type opponentID struct{ Name, Policy string }
	selection := struct {
		Schema     string       `json:"schema"`
		Experiment string       `json:"experiment"`
		Candidate  string       `json:"candidate_artifact"`
		Opponents  []opponentID `json:"opponents"`
	}{Schema: "commander-final-selection-v1", Experiment: id}
	selection.Candidate, _ = Digest(candidate)
	if e := x.validateCandidateProvenance(candidate); e != nil {
		return e
	}
	heldout := map[string]bool{}
	for _, f := range x.Families {
		if f.Split != "train" {
			heldout[f.Group] = true
		}
	}
	names := map[string]bool{}
	for _, o := range opponents {
		if o.Name == "" || names[o.Name] || (o.Rule == "") == (o.Model == nil) {
			return fmt.Errorf("final selection requires unique opponents with exactly one rule/model")
		}
		names[o.Name] = true
		p := Policy{Rule: o.Rule}
		if o.Model != nil {
			if e := o.Model.Validate(); e != nil {
				return e
			}
			if !o.Model.CompatibleNativeEnvironment(x.Environment) || !supports(*o.Model, x.Mode) {
				return fmt.Errorf("final-test opponent incompatible")
			}
			for _, group := range o.Model.DataGroups() {
				if heldout[group] {
					return fmt.Errorf("opponent learned from a held-out experiment group")
				}
			}
			if o.Model.Experiment != candidate.Experiment {
				for _, group := range o.Model.HeldoutGroups {
					if heldout[group] {
						return fmt.Errorf("opponent previously evaluated on this held-out group")
					}
				}
			}
			p = Policy{Model: o.Model.Network, Greedy: true}
		}
		version, e := p.Version()
		if e != nil {
			return e
		}
		selection.Opponents = append(selection.Opponents, opponentID{o.Name, version})
	}
	sort.Slice(selection.Opponents, func(i, j int) bool { return selection.Opponents[i].Name < selection.Opponents[j].Name })
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	if e := writeObject(path, selection); e != nil {
		return fmt.Errorf("final-test selection is fixed for this experiment: %w", e)
	}
	return nil
}
