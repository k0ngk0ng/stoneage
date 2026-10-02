package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Roster contains only the proposed team's integer budgets. Opponent builds
// live in the experimental distribution and never enter scorer features.
type Roster struct {
	Players  []battleenv.Build        `json:"players"`
	Pets     []battleenv.Build        `json:"pets,omitempty"`
	Reserves [][]battleenv.ReservePet `json:"reserves,omitempty"`
}

func (r Roster) ID() string { id, _ := Digest(r); return id }

func (r Roster) validate(c BuildSearchConfig) error {
	if len(r.Players) != c.Mode || (c.PetPoints == 0 && len(r.Pets) != 0) || (c.PetPoints != 0 && len(r.Pets) != c.Mode) {
		return fmt.Errorf("build roster dimensions mismatch")
	}
	if c.ReservePets == 0 && len(r.Reserves) != 0 || c.ReservePets > 0 && len(r.Reserves) != c.Mode {
		return fmt.Errorf("reserve roster dimensions mismatch")
	}
	for _, row := range r.Reserves {
		if len(row) != c.ReservePets {
			return fmt.Errorf("reserve roster count mismatch")
		}
		for _, pet := range row {
			total := 0
			for _, n := range pet.Build {
				if n < 1 || n > 10000 {
					return fmt.Errorf("invalid reserve points")
				}
				total += n
			}
			allowed := c.PetSkillMask | 3
			if c.PetSkillMask == 0 {
				allowed = 127
			}
			if total != c.PetPoints || !battleenv.ValidPetSkillMask(pet.SkillMask) || pet.SkillMask & ^allowed != 0 {
				return fmt.Errorf("reserve budget or skills mismatch")
			}
		}
	}
	for j, list := range [][]battleenv.Build{r.Players, r.Pets} {
		budget := c.Points
		if j == 1 {
			budget = c.PetPoints
		}
		for _, b := range list {
			sum := 0
			for _, n := range b {
				if n < 1 || n > 10000 {
					return fmt.Errorf("build points must be integers in 1..10000")
				}
				sum += n
			}
			if sum != budget {
				return fmt.Errorf("each actor must retain its own exact budget")
			}
		}
	}
	return nil
}

func (r Roster) features(c BuildSearchConfig) []float32 {
	var features []float32
	for j, list := range [][]battleenv.Build{r.Players, r.Pets} {
		budget := c.Points
		if j == 1 {
			budget = c.PetPoints
		}
		for _, b := range list {
			for _, n := range b {
				features = append(features, float32(n)/float32(budget))
			}
		}
	}
	for _, row := range r.Reserves {
		for _, pet := range row {
			for _, n := range pet.Build {
				features = append(features, float32(n)/float32(c.PetPoints))
			}
			columns := 7
			if c.PetSkillMask != 0 {
				columns = 8
			}
			for bit := 0; bit < columns; bit++ {
				features = append(features, float32((pet.SkillMask>>uint(bit))&1))
			}
		}
	}
	return features
}

type BuildSearchConfig struct {
	PetSkillMask      int   `json:"pet_skill_mask,omitempty"` // Optional active set; zero preserves the original seven skills.
	ReservePets       int   `json:"reserve_pets,omitempty"`   // Extra pets per member, 0..2.
	HealingItems      int   `json:"healing_items,omitempty"`  // Number of single-use template 1234 items per character, 0..15.
	HealingMagic      int   `json:"healing_magic,omitempty"`
	Seed              int64 `json:"seed"`
	Mode              int   `json:"mode"`
	Points            int   `json:"points"`
	PetPoints         int   `json:"pet_points"`
	Level             int   `json:"level"`
	MaxTurns          int   `json:"max_turns"`
	InitialCandidates int   `json:"initial_candidates"`
	Generations       int   `json:"generations"`
	Proposals         int   `json:"proposals"`
	NativeCandidates  int   `json:"native_candidates"`
	Finalists         int   `json:"finalists"`
	FitEpochs         int   `json:"fit_epochs"`
	SearchGroups      int   `json:"search_groups"`
	ValidationGroups  int   `json:"validation_groups"`
	TestGroups        int   `json:"test_groups"`
}

func DefaultBuildSearchConfig() BuildSearchConfig {
	return BuildSearchConfig{Seed: 1, Mode: 1, Points: 120, PetPoints: 120, Level: 35, MaxTurns: 200, InitialCandidates: 16, Generations: 3, Proposals: 128, NativeCandidates: 4, Finalists: 4, FitEpochs: 100, SearchGroups: 4, ValidationGroups: 20, TestGroups: 40}
}

func (c BuildSearchConfig) Validate() error {
	base := DefaultRunConfig()
	base.Mode, base.Points, base.PetPoints, base.Level, base.MaxTurns = c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns
	base.HealingMagic = c.HealingMagic
	base.HealingItems = c.HealingItems
	base.ReservePets = c.ReservePets
	base.PetSkillMask = c.PetSkillMask
	if e := base.Validate(); e != nil {
		return e
	}
	if c.Points == 4 && (c.PetPoints == 0 || c.PetPoints == 4) {
		return fmt.Errorf("no freely allocatable points to search")
	}
	if c.InitialCandidates < 5 || c.InitialCandidates > 256 || c.Generations < 1 || c.Generations > 20 || c.Proposals < c.NativeCandidates || c.Proposals > 4096 || c.NativeCandidates < 1 || c.NativeCandidates > 32 || c.Finalists < 2 || c.Finalists > 16 || c.Finalists > c.InitialCandidates || c.FitEpochs < 1 || c.FitEpochs > 1000 || c.SearchGroups < 1 || c.SearchGroups > 256 || c.ValidationGroups < 1 || c.ValidationGroups > 512 || c.TestGroups < 1 || c.TestGroups > 512 {
		return fmt.Errorf("invalid build search size/iteration settings")
	}
	return nil
}

func balancedRoster(c BuildSearchConfig) Roster {
	r := Roster{}
	for j, budget := range []int{c.Points, c.PetPoints} {
		if budget == 0 {
			continue
		}
		b := battleenv.Build{}
		for i := range b {
			b[i] = budget / 4
			if i < budget%4 {
				b[i]++
			}
		}
		for seat := 0; seat < c.Mode; seat++ {
			if j == 0 {
				r.Players = append(r.Players, b)
			} else {
				r.Pets = append(r.Pets, b)
			}
		}
	}
	if c.ReservePets > 0 {
		r.Reserves = make([][]battleenv.ReservePet, c.Mode)
		for i := range r.Reserves {
			for j := 0; j < c.ReservePets; j++ {
				mask := 3
				specialists := reserveSpecialists(c.PetSkillMask)
				if len(specialists) > 0 {
					mask |= specialists[j%len(specialists)]
				}
				r.Reserves[i] = append(r.Reserves[i], battleenv.ReservePet{Build: r.Pets[i], SkillMask: mask})
			}
		}
	}
	return r
}

func randomRoster(c BuildSearchConfig, rng *rand.Rand) Roster {
	r := Roster{}
	for seat := 0; seat < c.Mode; seat++ {
		r.Players = append(r.Players, allocation(c.Points, rng))
		if c.PetPoints > 0 {
			r.Pets = append(r.Pets, allocation(c.PetPoints, rng))
		}
	}
	r.Reserves = randomReserves(c.Mode, c.ReservePets, c.PetPoints, rng, c.PetSkillMask)
	return r
}

func initialRosters(c BuildSearchConfig) ([]Roster, error) {
	rng := rand.New(rand.NewSource(gameSeed(c.Seed, 0, 100)))
	out := []Roster{balancedRoster(c)}
	seen := map[string]bool{out[0].ID(): true}
	for attribute := 0; attribute < 4; attribute++ {
		r := balancedRoster(c)
		for _, list := range [][]battleenv.Build{r.Players, r.Pets} {
			for seat, b := range list {
				budget := b[0] + b[1] + b[2] + b[3]
				list[seat] = battleenv.Build{1, 1, 1, 1}
				list[seat][attribute] += budget - 4
			}
		}
		for _, row := range r.Reserves {
			for i := range row {
				row[i].Build = battleenv.Build{1, 1, 1, 1}
				row[i].Build[attribute] += c.PetPoints - 4
			}
		}
		if !seen[r.ID()] {
			seen[r.ID()] = true
			out = append(out, r)
		}
	}
	for attempt := 0; len(out) < c.InitialCandidates; attempt++ {
		if attempt > c.InitialCandidates*1000 {
			return nil, fmt.Errorf("not enough distinct legal initial rosters")
		}
		r := randomRoster(c, rng)
		if !seen[r.ID()] {
			seen[r.ID()] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// Local mutations move points between two attributes of ONE actor. No
// rounding, budget repair or team-level redistribution can occur.
func mutateRoster(parent Roster, c BuildSearchConfig, rng *rand.Rand) Roster {
	r := Roster{Players: append([]battleenv.Build(nil), parent.Players...), Pets: append([]battleenv.Build(nil), parent.Pets...), Reserves: battleenv.CloneReserves(parent.Reserves)}
	for changes := 1 + rng.Intn(3); changes > 0; changes-- {
		list, budget := r.Players, c.Points
		if len(r.Pets) > 0 && rng.Intn(2) == 0 {
			list, budget = r.Pets, c.PetPoints
		}
		b := &list[rng.Intn(len(list))]
		if c.ReservePets > 0 && rng.Intn(2) == 0 {
			pet := &r.Reserves[rng.Intn(c.Mode)][rng.Intn(c.ReservePets)]
			b, budget = &pet.Build, c.PetPoints
			if rng.Intn(3) == 0 {
				pet.SkillMask = randomReserveSkills(rng, c.PetSkillMask)
			}
		}
		from, to := rng.Intn(4), rng.Intn(4)
		if from == to || b[from] <= 1 {
			continue
		}
		limit := min(b[from]-1, max(1, budget/4))
		delta := 1 + rng.Intn(limit)
		b[from] -= delta
		b[to] += delta
	}
	return r
}

type BuildPolicyIdentity struct {
	Name     string `json:"name"`
	Rule     string `json:"rule,omitempty"`
	Version  string `json:"version"`
	Artifact string `json:"artifact,omitempty"`
}

type BuildSearchSpec struct {
	RecordedSelectionArtifacts   []string              `json:"recorded_selection_artifacts,omitempty"`
	Schema                       string                `json:"schema"`
	Config                       BuildSearchConfig     `json:"config"`
	Environment                  battleenv.Metadata    `json:"environment"`
	Controller                   BuildPolicyIdentity   `json:"controller"`
	Opponents                    []BuildPolicyIdentity `json:"opponents"`
	Search                       []Roster              `json:"search_opponent_rosters"`
	Validation                   []Roster              `json:"validation_opponent_rosters"`
	Test                         []Roster              `json:"test_opponent_rosters"`
	ExcludedPolicyTrainingGroups []string              `json:"excluded_policy_training_groups,omitempty"`
}

func buildPolicy(o Opponent, meta battleenv.Metadata, mode int) (Policy, BuildPolicyIdentity, error) {
	id := BuildPolicyIdentity{Name: o.Name, Rule: o.Rule}
	if o.Name == "" || (o.Rule == "") == (o.Model == nil) {
		return Policy{}, id, fmt.Errorf("build search policy needs a name and exactly one rule/model")
	}
	p := Policy{Rule: o.Rule}
	if o.Model != nil {
		if e := o.Model.Validate(); e != nil {
			return p, id, e
		}
		if !o.Model.CompatibleNativeEnvironment(meta) || !supports(*o.Model, mode) {
			return p, id, fmt.Errorf("build search policy rules/platform/mode mismatch")
		}
		p = Policy{Model: o.Model.Network, Greedy: true}
		id.Artifact, _ = Digest(*o.Model)
	}
	var e error
	id.Version, e = p.Version()
	return p, id, e
}

func newBuildSearchSpec(c BuildSearchConfig, meta battleenv.Metadata, controller Opponent, opponents []Opponent) (BuildSearchSpec, Policy, []Policy, error) {
	x := BuildSearchSpec{Schema: "build-search-v1", Config: c, Environment: meta}
	var p Policy
	if e := c.Validate(); e != nil {
		return x, p, nil, e
	}
	if e := meta.Validate(); e != nil {
		return x, p, nil, e
	}
	if c.PetSkillMask != 0 && meta.Scenario != "controlled-battle-v8" {
		return x, p, nil, fmt.Errorf("active pet skill configuration requires controlled-battle-v8")
	}
	if len(opponents) < 1 || len(opponents) > 16 {
		return x, p, nil, fmt.Errorf("build search requires 1..16 frozen opponents")
	}
	games := (c.InitialCandidates+c.Generations*c.NativeCandidates)*c.SearchGroups*4 + c.Finalists*c.ValidationGroups*4 + 2*c.TestGroups*4
	if games*len(opponents) > 200000 {
		return x, p, nil, fmt.Errorf("build search exceeds 200000 native games; reduce candidates/groups")
	}
	var e error
	p, x.Controller, e = buildPolicy(controller, meta, c.Mode)
	if e != nil {
		return x, p, nil, e
	}
	var policies []Policy
	names := map[string]bool{}
	for _, o := range opponents {
		q, id, e := buildPolicy(o, meta, c.Mode)
		if e != nil {
			return x, p, nil, e
		}
		if names[id.Name] {
			return x, p, nil, fmt.Errorf("duplicate build-search opponent")
		}
		names[id.Name] = true
		policies, x.Opponents = append(policies, q), append(x.Opponents, id)
	}
	excluded := map[string]bool{}
	for _, o := range append([]Opponent{controller}, opponents...) {
		if o.Model != nil {
			x.RecordedSelectionArtifacts = sortedUnion(x.RecordedSelectionArtifacts, o.Model.RecordedSelectionArtifacts)
			if o.Model.Recorded != nil {
				id, _ := Digest(*o.Model)
				x.RecordedSelectionArtifacts = sortedUnion(x.RecordedSelectionArtifacts, []string{id})
			}
			for _, group := range o.Model.DataGroups() {
				excluded[group] = true
			}
			for _, group := range o.Model.HeldoutGroups {
				excluded[group] = true
			}
		}
	}
	for group := range excluded {
		x.ExcludedPolicyTrainingGroups = append(x.ExcludedPolicyTrainingGroups, group)
	}
	sort.Strings(x.ExcludedPolicyTrainingGroups)
	rng := rand.New(rand.NewSource(gameSeed(c.Seed, 0, 101)))
	seen := map[string]bool{}
	for split, n := range []int{c.SearchGroups, c.ValidationGroups, c.TestGroups} {
		var rosters []Roster
		for attempt := 0; len(rosters) < n; attempt++ {
			if attempt > n*1000+10000 {
				return x, p, nil, fmt.Errorf("not enough unseen opponent rosters for build-search splits")
			}
			r := randomRoster(c, rng)
			if seen[r.ID()] {
				continue
			}
			seen[r.ID()] = true
			rosters = append(rosters, r)
		}
		switch split {
		case 0:
			x.Search = rosters
		case 1:
			x.Validation = rosters
		case 2:
			x.Test = rosters
		}
	}
	return x, p, policies, nil
}

type BuildGame struct {
	EvaluationGame
	Shard string `json:"shard"`
}
type BuildEvaluation struct {
	Roster Roster      `json:"roster"`
	Games  []BuildGame `json:"games"`
}
type BuildSearchState struct {
	Spec                  BuildSearchSpec                 `json:"spec"`
	Stage                 string                          `json:"stage"`
	Generation            int                             `json:"generation"`
	Pending               []Roster                        `json:"pending"`
	Search                []BuildEvaluation               `json:"search"`
	Validation            []BuildEvaluation               `json:"validation"`
	Test                  []BuildEvaluation               `json:"test"`
	Scorer                *battlenet.BuildScorer[float32] `json:"scorer,omitempty"`
	ScorerTrainingRosters []string                        `json:"scorer_training_rosters,omitempty"`
	Selected              string                          `json:"selected,omitempty"`
}

func buildScenario(c BuildSearchConfig, own, enemy Roster, group, repeat, split int) battleenv.Scenario {
	s := battleenv.Scenario{Seed: 1 + int(gameSeed(c.Seed, uint64(group*2+repeat/2), uint64(110+split))%2147483647), Mode: c.Mode, Level: c.Level, HealingMagic: c.HealingMagic, HealingItems: c.HealingItems, MaxTurns: c.MaxTurns}
	s.Builds = append(append([]battleenv.Build(nil), own.Players...), enemy.Players...)
	s.PetBuilds = append(append([]battleenv.Build(nil), own.Pets...), enemy.Pets...)
	s.Reserves = battleenv.CloneReserves(append(append([][]battleenv.ReservePet(nil), own.Reserves...), enemy.Reserves...))
	applyPetSkills(&s, c.PetSkillMask)
	if repeat%2 == 1 {
		swapScenario(&s)
	}
	return s
}

func buildScore(e BuildEvaluation) (float64, int) {
	score, truncated := 0., 0
	for _, g := range e.Games {
		if g.Truncated || !g.Terminated {
			truncated++
		} else if g.Winner < 0 {
			score += .5
		} else if g.Winner == g.CandidateSide {
			score++
		}
	}
	if len(e.Games) == truncated {
		return 0, truncated
	}
	return score / float64(len(e.Games)-truncated), truncated
}

func rankedBuilds(evaluations []BuildEvaluation) []BuildEvaluation {
	list := append([]BuildEvaluation(nil), evaluations...)
	sort.Slice(list, func(i, j int) bool {
		a, _ := buildScore(list[i])
		b, _ := buildScore(list[j])
		if a == b {
			return list[i].Roster.ID() < list[j].Roster.ID()
		}
		return a > b
	})
	return list
}

func proposeBuilds(ctx context.Context, state *BuildSearchState) ([]Roster, error) {
	c := state.Spec.Config
	var features [][]float32
	var scores []float32
	seen := map[string]bool{}
	for _, e := range state.Search {
		score, truncated := buildScore(e)
		if truncated != 0 {
			return nil, fmt.Errorf("build search contains collection cutoffs; use a new experiment with a larger --max-turns")
		}
		features, scores = append(features, e.Roster.features(c)), append(scores, float32(score))
		seen[e.Roster.ID()] = true
	}
	scorer, e := battlenet.FitBuildScorer(ctx, features, scores, c.FitEpochs, gameSeed(c.Seed, uint64(state.Generation), 120))
	if e != nil {
		return nil, e
	}
	ranked := rankedBuilds(state.Search)
	rng := rand.New(rand.NewSource(gameSeed(c.Seed, uint64(state.Generation), 121)))
	type proposal struct {
		roster Roster
		score  float32
	}
	var proposals []proposal
	for attempt := 0; len(proposals) < c.Proposals && attempt < c.Proposals*1000; attempt++ {
		r := randomRoster(c, rng)
		if rng.Intn(5) != 0 {
			r = mutateRoster(ranked[rng.Intn(max(1, len(ranked)/4))].Roster, c, rng)
		}
		if seen[r.ID()] {
			continue
		}
		seen[r.ID()] = true
		if e = r.validate(c); e != nil {
			return nil, e
		}
		score, e := scorer.Predict(r.features(c))
		if e != nil {
			return nil, e
		}
		proposals = append(proposals, proposal{r, score})
	}
	if len(proposals) < c.NativeCandidates {
		return nil, fmt.Errorf("legal unseen build space exhausted; reduce search dimensions in a new experiment")
	}
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].score == proposals[j].score {
			return proposals[i].roster.ID() < proposals[j].roster.ID()
		}
		return proposals[i].score > proposals[j].score
	})
	var out []Roster
	for i := 0; i < c.NativeCandidates; i++ {
		index := i
		if c.NativeCandidates > 1 && i == c.NativeCandidates-1 {
			index += rng.Intn(len(proposals) - i) // one exploration slot.
		}
		out = append(out, proposals[index].roster)
	}
	state.Scorer = scorer
	state.ScorerTrainingRosters = nil
	for _, result := range state.Search {
		state.ScorerTrainingRosters = append(state.ScorerTrainingRosters, result.Roster.ID())
	}
	return out, nil
}

func saveBuildSearch(root string, state BuildSearchState) error {
	return saveBuildSearchCached(root, state, map[string]bool{})
}

func saveBuildSearchCached(root string, state BuildSearchState, cache map[string]bool) error {
	id, e := Digest(state)
	if e != nil {
		return e
	}
	dir := filepath.Join(root, "search-states")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	disk, e := persistBuildSearch(root, state, cache)
	if e != nil {
		return e
	}
	// The address identifies the expanded semantic state; each referenced
	// object's own checksum is also verified when loading.
	if e = writeObject(filepath.Join(dir, id+".json"), disk); e != nil {
		return e
	}
	f, e := os.CreateTemp(root, ".search-latest-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = json.NewEncoder(f).Encode(struct {
		State string `json:"state"`
	}{id}); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), filepath.Join(root, "search-latest.json")); e != nil {
		return e
	}
	d, e := os.Open(root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func LoadBuildSearch(root string) (BuildSearchState, error) {
	var pointer struct {
		State string `json:"state"`
	}
	var s BuildSearchState
	if e := readObject(filepath.Join(root, "search-latest.json"), &pointer, 1024); e != nil {
		return s, e
	}
	if !digest(pointer.State) {
		return s, fmt.Errorf("invalid build search pointer")
	}
	var disk buildSearchDisk
	if e := readObject(filepath.Join(root, "search-states", pointer.State+".json"), &disk, 1<<20); e != nil {
		return s, e
	}
	var e error
	s, e = expandBuildSearch(root, disk)
	if e != nil {
		return s, e
	}
	id, _ := Digest(s)
	if id != pointer.State {
		return s, fmt.Errorf("build search state checksum mismatch")
	}
	return s, validateBuildSearch(s)
}

func LoadBuildSearchPolicy(root string, id BuildPolicyIdentity) (Opponent, error) {
	o := Opponent{Name: id.Name, Rule: id.Rule}
	if id.Artifact != "" {
		if !digest(id.Artifact) {
			return o, fmt.Errorf("invalid pinned build policy identity")
		}
		a, e := battlepolicy.LoadArtifact(filepath.Join(root, "search-policies", id.Artifact+".json"))
		if e != nil {
			return o, e
		}
		actual, _ := Digest(a)
		if actual != id.Artifact {
			return o, fmt.Errorf("pinned build policy checksum mismatch")
		}
		o.Model = &a
	}
	return o, nil
}

type BuildSearchOptions struct {
	Directory  string
	Engine     *battleenv.Native
	Controller Opponent
	Opponents  []Opponent
	Config     BuildSearchConfig
	Resume     bool
	Progress   func(stage string, completeGames int) error
}

// RunBuildSearch fixes combat policies throughout search -> validation ->
// final test. Every native game saves both complete trajectories; an unfinished
// game is discarded, while completed games and the selected finalist resume.
func RunBuildSearch(ctx context.Context, o BuildSearchOptions) (BuildSearchState, error) {
	var state BuildSearchState
	if o.Engine == nil || o.Directory == "" {
		return state, fmt.Errorf("build search needs an engine and data directory")
	}
	x, own, opponents, e := newBuildSearchSpec(o.Config, o.Engine.Metadata(), o.Controller, o.Opponents)
	if e != nil {
		return state, e
	}
	if e = os.MkdirAll(o.Directory, 0700); e != nil {
		return state, e
	}
	lock, e := os.OpenFile(filepath.Join(o.Directory, ".build-search.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return state, e
	}
	defer lock.Close()
	if e = lockTraining(lock); e != nil {
		return state, fmt.Errorf("build search directory in use: %w", e)
	}
	objectCache := map[string]bool{}
	if o.Resume {
		state, e = LoadBuildSearch(o.Directory)
		if e != nil {
			return state, e
		}
		if !reflect.DeepEqual(state.Spec, x) {
			return state, fmt.Errorf("build search config, policy or environment changed; cannot resume")
		}
		if e = verifyBuildSearchData(o.Directory, state); e != nil {
			return state, e
		}
	} else {
		if _, e = os.Stat(filepath.Join(o.Directory, "search-latest.json")); e == nil || !os.IsNotExist(e) {
			return state, fmt.Errorf("build search already exists; use --resume")
		}
		initial, e := initialRosters(o.Config)
		if e != nil {
			return state, e
		}
		state = BuildSearchState{Spec: x, Stage: "search", Pending: initial}
		if e = os.MkdirAll(filepath.Join(o.Directory, "search-policies"), 0700); e != nil {
			return state, e
		}
		for _, policy := range append([]Opponent{o.Controller}, o.Opponents...) {
			if policy.Model != nil {
				id, _ := Digest(*policy.Model)
				if e = writeObject(filepath.Join(o.Directory, "search-policies", id+".json"), policy.Model); e != nil {
					return state, e
				}
			}
		}
		if e = saveBuildSearchCached(o.Directory, state, objectCache); e != nil {
			return state, e
		}
	}
	save := func() error { return saveBuildSearchCached(o.Directory, state, objectCache) }
	emit := func() error {
		if o.Progress == nil {
			return nil
		}
		n := 0
		for _, list := range [][]BuildEvaluation{state.Search, state.Validation, state.Test} {
			for _, evaluation := range list {
				n += len(evaluation.Games)
			}
		}
		return o.Progress(state.Stage, n)
	}
	for state.Stage != "complete" {
		if e = ctx.Err(); e != nil {
			return state, e
		}
		var results *[]BuildEvaluation
		var enemies []Roster
		split := 0
		switch state.Stage {
		case "search":
			results, enemies = &state.Search, x.Search
		case "validation":
			results, enemies, split = &state.Validation, x.Validation, 1
		case "test":
			results, enemies, split = &state.Test, x.Test, 2
		default:
			return state, fmt.Errorf("unknown build search stage")
		}
		for _, roster := range state.Pending {
			if e = roster.validate(x.Config); e != nil {
				return state, e
			}
			index := -1
			for i, r := range *results {
				if r.Roster.ID() == roster.ID() {
					index = i
				}
			}
			if index < 0 {
				*results = append(*results, BuildEvaluation{Roster: roster})
				index = len(*results) - 1
			}
			result := &(*results)[index]
			for game := len(result.Games); game < len(enemies)*len(opponents)*4; game++ {
				oi, group, repeat := game/(len(enemies)*4), (game/4)%len(enemies), game%4
				s := buildScenario(x.Config, roster, enemies[group], group, repeat, split)
				if split > 0 {
					id := ScenarioGroup(s)
					i := sort.SearchStrings(x.ExcludedPolicyTrainingGroups, id)
					if i < len(x.ExcludedPolicyTrainingGroups) && x.ExcludedPolicyTrainingGroups[i] == id {
						return state, fmt.Errorf("held-out build scenario overlaps frozen combat-policy training; no independent recommendation")
					}
				}
				side := repeat % 2
				policies := [2]Policy{}
				policies[side], policies[1-side] = own, opponents[oi]
				tr, e := CollectPolicies(ctx, o.Engine, s, policies, [2]*rand.Rand{}, "", x.Environment.Rules)
				if e != nil {
					return state, e
				}
				shard, e := SaveShard(filepath.Join(o.Directory, "shards"), tr[:])
				if e != nil {
					return state, e
				}
				t := tr[side]
				result.Games = append(result.Games, BuildGame{EvaluationGame: EvaluationGame{Opponent: x.Opponents[oi].Name, OpponentPolicy: x.Opponents[oi].Version, Group: enemies[group].ID(), Scenario: s, CandidateSide: side, Turns: len(t.Steps), Winner: t.Winner, Terminated: t.Terminated, Truncated: t.Truncated}, Shard: shard.Digest})
				if e = save(); e != nil {
					return state, e
				}
				if e = emit(); e != nil {
					return state, e
				}
			}
			if _, truncated := buildScore(*result); truncated != 0 {
				return state, fmt.Errorf("build comparison has %d collection cutoffs; saved for inspection, no recommendation; start a new search with larger --max-turns", truncated)
			}
		}
		switch state.Stage {
		case "search":
			if state.Generation < x.Config.Generations {
				state.Pending, e = proposeBuilds(ctx, &state)
				if e != nil {
					return state, e
				}
				state.Generation++
			} else {
				state.Stage = "validation"
				state.Pending = buildFinalists(state)
			}
		case "validation":
			selected := buildFinalSelection(state)
			state.Selected = selected.ID() // fixed before observing any final-test outcome.
			state.Stage = "test"
			state.Pending = []Roster{selected}
			baseline := balancedRoster(x.Config)
			if baseline.ID() != state.Selected {
				state.Pending = append(state.Pending, baseline)
			}
		case "test":
			state.Stage, state.Pending = "complete", nil
		}
		if e = save(); e != nil {
			return state, e
		}
	}
	return state, emit()
}

type BuildSearchReport struct {
	Schema                string                          `json:"schema"`
	Status                string                          `json:"status"`
	Spec                  BuildSearchSpec                 `json:"spec"`
	Selected              Roster                          `json:"selected"`
	Finalists             []BuildEvaluation               `json:"finalists"`
	FinalTest             []BuildEvaluation               `json:"final_test"`
	DeltaFromBalanced     float64                         `json:"delta_from_balanced"`
	PairedCI95            *[2]float64                     `json:"paired_delta_ci95,omitempty"`
	Scorer                *battlenet.BuildScorer[float32] `json:"scorer"`
	ScorerTrainingRosters []string                        `json:"scorer_training_rosters"`
	SearchState           string                          `json:"search_state"`
	Scores                []BuildScoreSummary             `json:"scores"`
}

type BuildScoreSummary struct {
	Stage          string       `json:"stage"`
	Roster         string       `json:"roster"`
	CompletedScore float64      `json:"completed_score"`
	Comparisons    []Comparison `json:"comparisons"`
}

func SaveBuildSearchReport(root string, state BuildSearchState) (string, error) {
	if e := validateBuildSearch(state); e != nil {
		return "", e
	}
	stored, e := LoadBuildSearch(root)
	if e != nil {
		return "", e
	}
	actual, _ := Digest(stored)
	want, _ := Digest(state)
	if actual != want {
		return "", fmt.Errorf("report must reference the committed build search state")
	}
	if e := verifyBuildSearchData(root, state); e != nil {
		return "", e
	}
	if state.Stage != "complete" || state.Selected == "" || state.Scorer == nil || len(state.Test) < 1 {
		return "", fmt.Errorf("build search has not completed native rechecks")
	}
	if e := state.Scorer.Validate(); e != nil {
		return "", e
	}
	r := BuildSearchReport{Schema: "build-search-report-v1", Status: "candidate-advice", Spec: state.Spec, Finalists: state.Validation, FinalTest: state.Test, Scorer: state.Scorer}
	r.ScorerTrainingRosters = state.ScorerTrainingRosters
	for i, list := range [][]BuildEvaluation{state.Validation, state.Test} {
		for _, evaluation := range list {
			var games []EvaluationGame
			for _, game := range evaluation.Games {
				games = append(games, game.EvaluationGame)
			}
			score, _ := buildScore(evaluation)
			r.Scores = append(r.Scores, BuildScoreSummary{Stage: []string{"validation", "test"}[i], Roster: evaluation.Roster.ID(), CompletedScore: score, Comparisons: Summarize(games, state.Spec.Config.Seed)})
		}
	}
	r.SearchState, _ = Digest(state)
	var selected, baseline *BuildEvaluation
	for i := range state.Test {
		if state.Test[i].Roster.ID() == state.Selected {
			selected = &state.Test[i]
		}
		if state.Test[i].Roster.ID() == balancedRoster(state.Spec.Config).ID() {
			baseline = &state.Test[i]
		}
	}
	if selected == nil || baseline == nil {
		return "", fmt.Errorf("selected/balanced native test missing")
	}
	r.Selected = selected.Roster
	a, ta := buildScore(*selected)
	b, tb := buildScore(*baseline)
	if ta != 0 || tb != 0 {
		return "", fmt.Errorf("cutoff games cannot certify a build comparison")
	}
	r.DeltaFromBalanced = a - b
	if len(state.Spec.Test) >= 20 {
		// Pair differences on each opponent roster, then resample whole groups
		// with all policies/seeds/sides kept together. No iid-game interval.
		differences := map[string]float64{}
		counts := map[string]int{}
		for i, g := range selected.Games {
			h := baseline.Games[i]
			if g.Group != h.Group || g.OpponentPolicy != h.OpponentPolicy || g.CandidateSide != h.CandidateSide || g.Scenario.Seed != h.Scenario.Seed {
				return "", fmt.Errorf("unpaired final build comparison")
			}
			score := func(g BuildGame) float64 {
				if g.Winner < 0 {
					return .5
				}
				if g.Winner == g.CandidateSide {
					return 1
				}
				return 0
			}
			differences[g.Group] += score(g) - score(h)
			counts[g.Group]++
		}
		var keys []string
		for key := range differences {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		rng := rand.New(rand.NewSource(gameSeed(state.Spec.Config.Seed, 0, 130)))
		values := make([]float64, 2000)
		for i := range values {
			for range keys {
				key := keys[rng.Intn(len(keys))]
				values[i] += differences[key] / float64(counts[key]) / float64(len(keys))
			}
		}
		sort.Float64s(values)
		r.PairedCI95 = &[2]float64{values[int(math.Floor(.025*float64(len(values)-1)))], values[int(math.Ceil(.975*float64(len(values)-1)))]}
	}
	path := filepath.Join(root, "build-report.json")
	return path, writeObject(path, r)
}
