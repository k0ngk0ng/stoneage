package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func buildPoolFixture(t *testing.T) BuildPool {
	t.Helper()
	c := DefaultBuildSearchConfig()
	c.Points, c.PetPoints, c.Level = 20, 0, 10
	rosters, e := initialRosters(c)
	if e != nil {
		t.Fatal(e)
	}
	rosters = []Roster{rosters[0], rosters[2]}
	sort.Slice(rosters, func(i, j int) bool { return rosters[i].ID() < rosters[j].ID() })
	version, _ := (Policy{Rule: "basic"}).Version()
	p := BuildPool{Schema: "commander-build-pool-v1", Environment: experimentFixture(t).Environment, Config: c, SourceState: strings.Repeat("4", 64), SourcePolicy: BuildPolicyIdentity{Name: "basic", Rule: "basic", Version: version}, Rosters: rosters, SelectionGroups: []string{strings.Repeat("5", 64)}}
	if e = p.Validate(); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestBuildPoolFrozenRosterAndAncestryBoundaries(t *testing.T) {
	p := buildPoolFixture(t)
	a := experimentCandidate(t, experimentFixture(t))
	a.SelectionGroups = []string{strings.Repeat("6", 64)}
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 20
	x, e := NewExperimentFromSources(context.Background(), p.Environment, c, [3]int{3, 2, 2}, &p, &a)
	if e != nil {
		t.Fatal(e)
	}
	if len(x.InitialTrainingGroups) != len(a.TrainingGroups) || len(x.SelectionGroups) != 2+len(a.HeldoutGroups) {
		t.Fatal("ancestral data omitted")
	}
	if e = x.validateInitialModel(&a); e != nil {
		t.Fatal(e)
	}
	for _, f := range x.Families {
		for side := 0; side < 2; side++ {
			r := Roster{Players: f.Scenario.Builds[side : side+1]}
			inside := false
			for _, known := range p.Rosters {
				inside = inside || known.ID() == r.ID()
			}
			if inside != (f.Split == "train") {
				t.Fatal("frozen roster leaked into heldout set")
			}
		}
	}
	child := a
	child.Parent = x.InitialModel
	child.SelectionGroups = x.SelectionGroups
	child.HeldoutGroups = x.heldoutGroups()
	child.TrainingGroups = sortedUnion(a.TrainingGroups, []string{x.groups("train")[0].Group})
	if e = x.validateCandidateProvenance(child); e != nil {
		t.Fatal(e)
	}
	child.SelectionGroups = nil
	if x.validateCandidateProvenance(child) == nil {
		t.Fatal("dropped selection provenance accepted")
	}
	child.SelectionGroups = x.SelectionGroups
	child.TrainingGroups = []string{x.groups("train")[0].Group}
	if x.validateCandidateProvenance(child) == nil {
		t.Fatal("dropped parent training accepted")
	}
	bad := x
	bad.SelectionGroups = nil
	if bad.Validate() == nil {
		t.Fatal("pool source data forgotten")
	}
	bad = x
	bad.InitialTrainingGroups = sortedUnion(x.InitialTrainingGroups, []string{x.groups("validation")[0].Group})
	if bad.Validate() == nil {
		t.Fatal("ancestral training reused in validation")
	}
	if _, e = NewExperimentFromSources(context.Background(), p.Environment, c, [3]int{4, 1, 1}, &p, &a); e == nil {
		t.Fatal("invented extra frozen roster pair")
	}
	a.TrainingReport = strings.Repeat("f", 64)
	if x.validateInitialModel(&a) == nil {
		t.Fatal("changed initial model accepted")
	}
	legacy := a
	legacy.HeldoutGroups = nil
	if e := legacy.Validate(); e != nil {
		t.Fatal("old inference artifacts must remain readable", e)
	}
	if _, e := NewExperimentFromSources(context.Background(), p.Environment, c, [3]int{3, 1, 1}, &p, &legacy); e == nil {
		t.Fatal("fine-tuning accepted an experiment-bound parent without held-out ancestry")
	}
	badArtifact := a
	badArtifact.HeldoutGroups = append([]string(nil), a.TrainingGroups...)
	if badArtifact.Validate() == nil {
		t.Fatal("model declared training groups as held-out evidence")
	}
	badArtifact.HeldoutGroups = append([]string(nil), a.SelectionGroups...)
	if badArtifact.Validate() == nil {
		t.Fatal("model declared selection groups as held-out evidence")
	}
}

func TestNativeBuildPoolFineTuneAndResume(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	c := DefaultBuildSearchConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 300
	c.InitialCandidates, c.Generations, c.Proposals, c.NativeCandidates, c.Finalists, c.FitEpochs = 5, 1, 8, 1, 2, 1
	c.SearchGroups, c.ValidationGroups, c.TestGroups = 1, 1, 1
	searchRoot := t.TempDir()
	_, e = RunBuildSearch(ctx, BuildSearchOptions{Directory: searchRoot, Engine: engine, Controller: Opponent{Name: "own", Rule: "basic"}, Opponents: []Opponent{{Name: "basic", Rule: "basic"}}, Config: c})
	if e != nil {
		t.Fatal(e)
	}
	pool, e := ExportBuildPool(searchRoot, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(pool.Rosters) != 2 || len(pool.SelectionGroups) == 0 {
		t.Fatal("pool lacks alternatives/source groups")
	}
	restored, e := LoadBuildPool(filepath.Join(searchRoot, "build-pool.json"))
	if e != nil || !reflect.DeepEqual(pool, restored) {
		t.Fatal("pool changed", e)
	}
	parentConfig := DefaultRunConfig()
	parentConfig.Network = testModel(t).Config
	parentConfig.Warmup = nil
	parentConfig.Points, parentConfig.PetPoints, parentConfig.Level, parentConfig.MaxTurns, parentConfig.BatchMatches = 20, 0, 10, 3, 2
	parentConfig.PPO.Epochs = 1
	parentRoot := t.TempDir()
	if e = Run(ctx, RunOptions{Directory: parentRoot, Command: command, Config: &parentConfig, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	path, e := ExportCandidate(parentRoot, "")
	if e != nil {
		t.Fatal(e)
	}
	parent, e := battlepolicy.LoadArtifact(path)
	if e != nil {
		t.Fatal(e)
	}
	parentBytes, _ := json.Marshal(parent)
	ec := DefaultEvaluationConfig()
	ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = 20, 0, 10, 3
	x, e := NewExperimentFromSources(ctx, engine.Metadata(), ec, [3]int{3, 1, 1}, &pool, &parent)
	if e != nil {
		t.Fatal(e)
	}
	childConfig := parentConfig
	childConfig.Experiment, _ = Digest(x)
	childConfig.InitialModel = x.InitialModel
	one, two := t.TempDir(), t.TempDir()
	stop := errors.New("stop after first committed fine-tuning game")
	e = Run(ctx, RunOptions{Directory: one, Command: command, Config: &childConfig, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "ready" {
			cp, learning, e := LoadCheckpoint(one)
			if e != nil {
				t.Fatal(e)
			}
			weights, _ := ModelDigest(learning.Model)
			if weights != parent.WeightsDigest || learning.Optimizer.Step != 0 || !reflect.DeepEqual(cp.TrainingGroups, parent.TrainingGroups) {
				t.Fatal("fine-tune didn't start from parent weights with fresh optimizer/provenance")
			}
		}
		if p.Event == "collected" {
			return stop
		}
		return nil
	}})
	if !errors.Is(e, stop) {
		t.Fatal("fine-tune interruption failed", e)
	}
	if e = Run(ctx, RunOptions{Directory: one, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	if e = Run(ctx, RunOptions{Directory: two, Command: command, Config: &childConfig, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	cp, a, e := LoadCheckpoint(one)
	if e != nil {
		t.Fatal(e)
	}
	_, b, e := LoadCheckpoint(two)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("fine-tune resume changed weights/moments", e)
	}
	unchanged, _ := json.Marshal(parent)
	if string(unchanged) != string(parentBytes) {
		t.Fatal("fine-tuning mutated parent artifact")
	}
	if !reflect.DeepEqual(cp.SelectionGroups, x.SelectionGroups) {
		t.Fatal("checkpoint dropped build-selection provenance")
	}
	path, e = ExportCandidate(one, "")
	if e != nil {
		t.Fatal(e)
	}
	child, e := battlepolicy.LoadArtifact(path)
	if e != nil {
		t.Fatal(e)
	}
	if child.Parent != x.InitialModel || len(child.TrainingShards) != len(parent.TrainingShards)+2 {
		t.Fatal("child export dropped parent/shard lineage")
	}
	if e = x.validateCandidateProvenance(child); e != nil {
		t.Fatal(e)
	}
	for _, split := range []string{"validation", "test"} {
		report, e := EvaluateExperiment(ctx, engine, child, []Opponent{{Name: "basic", Rule: "basic"}}, x, split, nil)
		if e != nil || len(report.Games) != 8 {
			t.Fatal("descendant evaluation failed", split, e)
		}
		if e = SaveEvaluation(filepath.Join(one, split+".json"), report); e != nil {
			t.Fatal(e)
		}
	}
	// A second generation must retain the entire earlier selection history.
	x2, e := NewExperimentFromSources(ctx, engine.Metadata(), ec, [3]int{3, 1, 1}, &pool, &child)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x2.InitialTrainingGroups, child.TrainingGroups) || !reflect.DeepEqual(x2.SelectionGroups, sortedUnion(child.SelectionGroups, child.HeldoutGroups)) {
		t.Fatal("second generation lost ancestry")
	}
	oldHeldout := map[string]bool{}
	for _, id := range child.HeldoutGroups {
		oldHeldout[id] = true
	}
	for _, f := range x2.Families {
		if f.Split != "train" && oldHeldout[f.Group] {
			t.Fatal("second iteration reused first iteration's evaluation")
		}
	}
	// Pool training from scratch and parent-only fine-tuning are both useful;
	// neither should accidentally require the other source.
	for _, tc := range []struct {
		name   string
		pool   *BuildPool
		parent *battlepolicy.Artifact
	}{
		{"pool_from_scratch", &pool, nil},
		{"parent_without_pool", nil, &child},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, e := NewExperimentFromSources(ctx, engine.Metadata(), ec, [3]int{3, 1, 1}, tc.pool, tc.parent)
			if e != nil {
				t.Fatal(e)
			}
			config := parentConfig
			config.Experiment, _ = Digest(x)
			config.InitialModel = x.InitialModel
			root := t.TempDir()
			if e = Run(ctx, RunOptions{Directory: root, Command: command, Config: &config, Experiment: &x, InitialModel: tc.parent, Batches: 1, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			path, e := ExportCandidate(root, "")
			if e != nil {
				t.Fatal(e)
			}
			a, e := battlepolicy.LoadArtifact(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = x.validateCandidateProvenance(a); e != nil {
				t.Fatal(e)
			}
			if _, e = EvaluateExperiment(ctx, engine, a, []Opponent{{Name: "basic", Rule: "basic"}}, x, "validation", nil); e != nil {
				t.Fatal(e)
			}
		})
	}
	if e = os.WriteFile(filepath.Join(one, "initial-models", x.InitialModel+".json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadCheckpoint(one); e == nil {
		t.Fatal("resume accepted corrupt parent model")
	}
	t.Logf("pool roster pairs=%d; inherited training=%d selection=%d; fine-tune resume exact, old weights immutable, heldout evaluation passed", len(x.groups("train")), len(parent.TrainingGroups), len(child.SelectionGroups))
}

func TestPoolMixPrototypeLegacyIdentity(t *testing.T) {
	p := buildPoolFixture(t)
	a := experimentCandidate(t, experimentFixture(t))
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 20
	x, e := NewExperimentFromSources(context.Background(), p.Environment, c, [3]int{3, 2, 2}, &p, &a)
	if e != nil {
		t.Fatal(e)
	}
	id, e := Digest(x)
	if e != nil {
		t.Fatal(e)
	}
	t.Log("legacy_identity=" + id)
}

func TestPoolMixPrototypeBoundaries(t *testing.T) {
	p := buildPoolFixture(t)
	a := experimentCandidate(t, experimentFixture(t))
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 20
	x, e := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, &p, &a, 2)
	if e != nil {
		t.Fatal(e)
	}
	if x.Schema != "commander-pool-mix-experiment-v1" || x.PoolTrainingGroups == nil || *x.PoolTrainingGroups != 2 {
		t.Fatal("missing explicit mixed contract")
	}
	poolCount, freshCount := 0, 0
	poolIDs := map[string]bool{}
	for _, r := range p.Rosters {
		poolIDs[r.ID()] = true
	}
	for _, f := range x.Families {
		left := Roster{Players: f.Scenario.Builds[:1]}
		right := Roster{Players: f.Scenario.Builds[1:]}
		l, r := poolIDs[left.ID()], poolIDs[right.ID()]
		if f.Split == "train" {
			if l && r {
				poolCount++
			} else if !l && !r {
				freshCount++
			} else {
				t.Fatal("one-sided mixed roster")
			}
		} else if l || r {
			t.Fatal("pool entered heldout")
		}
	}
	if poolCount != 2 || freshCount != 4 {
		t.Fatal("wrong training mixture", poolCount, freshCount)
	}
	copyX := func() Experiment {
		b, _ := json.Marshal(x)
		var y Experiment
		if e := json.Unmarshal(b, &y); e != nil {
			t.Fatal(e)
		}
		return y
	}
	for _, kind := range []string{"removed-contract", "changed-count", "missing-pool", "old-schema", "heldout-overlap", "training-one-sided", "generated-training-overlap"} {
		t.Run(kind, func(t *testing.T) {
			y := copyX()
			switch kind {
			case "removed-contract":
				y.PoolTrainingGroups = nil
			case "changed-count":
				*y.PoolTrainingGroups = 3
			case "missing-pool":
				y.Pool = nil
			case "old-schema":
				y.Schema = experimentSchema(y.Pairing)
			case "heldout-overlap":
				y.SelectionGroups = sortedUnion(y.SelectionGroups, []string{y.groups("validation")[0].Group})
			case "generated-training-overlap":
				for _, f := range y.Families {
					if f.Split == "train" && !poolIDs[Roster{Players: f.Scenario.Builds[:1]}.ID()] {
						y.SelectionGroups = sortedUnion(y.SelectionGroups, []string{f.Group})
						break
					}
				}
			case "training-one-sided":
				for i, f := range y.Families {
					if f.Split == "train" && !poolIDs[Roster{Players: f.Scenario.Builds[:1]}.ID()] {
						copy(y.Families[i].Scenario.Builds[:1], p.Rosters[0].Players)
						y.Families[i].Group = ScenarioGroup(y.Families[i].Scenario)
						break
					}
				}
			}
			if y.Validate() == nil {
				t.Fatal("invalid mixture accepted")
			}
		})
	}
	for _, n := range []int{0, 4, 6} {
		if _, e := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, &p, &a, n); e == nil {
			t.Fatal("invalid requested count", n)
		}
	}
	if _, e := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, nil, &a, 2); e == nil {
		t.Fatal("missing pool accepted")
	}
	again, e := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, &p, &a, 2)
	if e != nil || !reflect.DeepEqual(x, again) {
		t.Fatal("nondeterministic manifest", e)
	}
}

func TestPoolMixPrototypeAllModesAndRoundTrip(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			p := buildPoolFixture(t)
			p.Config.Mode = mode
			rs, e := initialRosters(p.Config)
			if e != nil {
				t.Fatal(e)
			}
			p.Rosters = []Roster{rs[0], rs[2]}
			sort.Slice(p.Rosters, func(i, j int) bool { return p.Rosters[i].ID() < p.Rosters[j].ID() })
			a := experimentCandidate(t, experimentFixture(t))
			c := DefaultEvaluationConfig()
			c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = mode, 20, 0, 10, 20
			x, e := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, &p, &a, 3)
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(t.TempDir(), "experiment.json")
			id, e := SaveExperiment(path, x)
			if e != nil {
				t.Fatal(e)
			}
			loaded, e := LoadExperiment(path)
			if e != nil || !reflect.DeepEqual(x, loaded) {
				t.Fatal("manifest round trip", e)
			}
			again, _ := Digest(loaded)
			if id != again {
				t.Fatal("identity changed")
			}
			counts := map[string]int{}
			for _, f := range x.Families {
				counts[f.Split]++
				if f.Scenario.Mode != mode {
					t.Fatal("mode mismatch")
				}
			}
			if counts["train"] != 6 || counts["validation"] != 2 || counts["test"] != 2 {
				t.Fatal("counts")
			}
		})
	}
}

// This test verifies the training pipeline, not playing strength: battles are
// deliberately capped at three turns and use a small network.
func TestNativePoolMixTrainingResumeAndExport(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			base := t.TempDir()
			if keep := os.Getenv("STONEAGE_POOL_MIX_TEST_OUTPUT"); keep != "" {
				var e error
				base, e = os.MkdirTemp(keep, fmt.Sprintf("mode%d-", mode))
				if e != nil {
					t.Fatal(e)
				}
			}
			config := DefaultRunConfig()
			config.Mode = mode
			config.Network = testModel(t).Config
			config.Warmup = nil
			config.Points, config.PetPoints, config.Level, config.MaxTurns, config.BatchMatches = 20, 20, 10, 3, 2
			config.ReservePets, config.HealingItems, config.HealingMagic = 1, 1, 20
			config.PPO.Epochs = 1
			parentRoot := filepath.Join(base, "parent")
			if e := Run(ctx, RunOptions{Directory: parentRoot, Command: command, Config: &config, Batches: 1, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			path, e := ExportCandidate(parentRoot, "")
			if e != nil {
				t.Fatal(e)
			}
			parent, e := battlepolicy.LoadArtifact(path)
			if e != nil {
				t.Fatal(e)
			}
			parentBytes, _ := json.Marshal(parent)
			pool := buildPoolFixture(t)
			pool.Environment = engine.Metadata()
			pool.Config.Mode = mode
			pool.Config.PetPoints = 20
			pool.Config.ReservePets, pool.Config.HealingItems, pool.Config.HealingMagic = 1, 1, 20
			rosters, e := initialRosters(pool.Config)
			if e != nil {
				t.Fatal(e)
			}
			pool.Rosters = []Roster{rosters[0], rosters[2]}
			sort.Slice(pool.Rosters, func(i, j int) bool { return pool.Rosters[i].ID() < pool.Rosters[j].ID() })
			ec := DefaultEvaluationConfig()
			ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = mode, 20, 20, 10, 3
			ec.ReservePets, ec.HealingItems, ec.HealingMagic = 1, 1, 20
			x, e := NewExperimentWithPoolMix(ctx, engine.Metadata(), ec, [3]int{2, 1, 1}, &pool, &parent, 1)
			if e != nil {
				t.Fatal(e)
			}
			id, e := SaveExperiment(filepath.Join(base, "experiment.json"), x)
			if e != nil {
				t.Fatal(e)
			}
			config.Experiment, config.InitialModel, config.BatchMatches = id, x.InitialModel, 16
			one, two := filepath.Join(base, "resumed"), filepath.Join(base, "continuous")
			stop := errors.New("stop after first training family committed")
			collected := 0
			e = Run(ctx, RunOptions{Directory: one, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
				if p.Event == "collected" {
					collected++
					if collected == 8 {
						return stop
					}
				}
				return nil
			}})
			if !errors.Is(e, stop) {
				t.Fatal("interruption not reached", collected, e)
			}
			if e = Run(ctx, RunOptions{Directory: one, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			if e = Run(ctx, RunOptions{Directory: two, Command: command, Config: &config, Experiment: &x, InitialModel: &parent, Batches: 1, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			cp, a, e := LoadCheckpoint(one)
			if e != nil {
				t.Fatal(e)
			}
			cp2, b, e := LoadCheckpoint(two)
			if e != nil || !reflect.DeepEqual(a, b) {
				t.Fatal("resumed model/optimizer differ", e)
			}
			if !reflect.DeepEqual(cp.TrainingGroups, cp2.TrainingGroups) || !reflect.DeepEqual(cp.SelectionGroups, x.SelectionGroups) {
				t.Fatal("resume lineage mismatch")
			}
			trained := map[string]bool{}
			for _, g := range cp.TrainingGroups {
				trained[g] = true
			}
			for _, f := range x.groups("train") {
				if !trained[f.Group] {
					t.Fatal("did not collect both pool and fresh families")
				}
			}
			after, _ := json.Marshal(parent)
			if string(after) != string(parentBytes) {
				t.Fatal("parent mutated")
			}
			path, e = ExportCandidate(one, "")
			if e != nil {
				t.Fatal(e)
			}
			child, e := battlepolicy.LoadArtifact(path)
			if e != nil {
				t.Fatal(e)
			}
			if child.Parent != x.InitialModel || len(child.TrainingShards) != len(parent.TrainingShards)+16 {
				t.Fatal("child lineage lost")
			}
			if e = x.validateCandidateProvenance(child); e != nil {
				t.Fatal(e)
			}
			for _, split := range []string{"validation", "test"} {
				report, e := EvaluateExperiment(ctx, engine, child, []Opponent{{Name: "basic", Rule: "basic"}}, x, split, nil)
				if e != nil || len(report.Games) != 8 {
					t.Fatal("mixed child evaluation", split, e)
				}
				if e = SaveEvaluation(filepath.Join(base, split+".json"), report); e != nil {
					t.Fatal(e)
				}
			}
			t.Logf("mode=%d pool/fresh training covered; resumed weights/Adam exact; parent immutable; child provenance and both heldout splits verified", mode)
		})
	}
}
