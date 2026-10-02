package battletrain

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func mixedExperimentFixture(t *testing.T) MixedExperiment {
	t.Helper()
	meta := experimentFixture(t).Environment
	var parts []MixedExperimentPart
	for i, mode := range []int{1, 5} {
		c := DefaultEvaluationConfig()
		c.Mode, c.Seed, c.ReservePets = mode, int64(715+mode), 2
		x, err := NewExperiment(context.Background(), meta, c, [3]int{5, 3, 2})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: x, FamiliesPerBatch: 2 - i})
	}
	x, err := NewMixedExperiment(testModeMixture(), parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func cloneMixedExperiment(t *testing.T, x MixedExperiment) MixedExperiment {
	t.Helper()
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var out MixedExperiment
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMixedExperimentPersistenceAndIsolation(t *testing.T) {
	x := mixedExperimentFixture(t)
	if !reflect.DeepEqual(x, mixedExperimentFixture(t)) {
		t.Fatal("mixed experiment constructor is not deterministic")
	}
	path := filepath.Join(t.TempDir(), "mixed.json")
	id, err := SaveMixedExperiment(path, x)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadMixedExperiment(path)
	if err != nil || !reflect.DeepEqual(x, loaded) {
		t.Fatal("mixed manifest changed after save/load", err)
	}
	want, _ := Digest(loaded)
	if id != want {
		t.Fatal("wrong mixed identity")
	}
	if _, err := LoadExperiment(path); err == nil {
		t.Fatal("single-mode loader accepted a mixed experiment")
	}
	changed := cloneMixedExperiment(t, x)
	changed.Mixture.Modes[0].Weight++
	if _, err := SaveMixedExperiment(path, changed); err == nil {
		t.Fatal("changed mixture overwrote a frozen manifest")
	}
	single := filepath.Join(t.TempDir(), "single.json")
	if _, err := SaveExperiment(single, x.Parts[0].Experiment); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMixedExperiment(single); err == nil {
		t.Fatal("mixed loader silently upgraded a single-mode experiment")
	}
	owned, err := NewMixedExperiment(x.Mixture, x.Parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.Mixture.Modes[0].Weight++
	x.Parts[0].Experiment.Families[0].Scenario.Builds[0][0]++
	x.Parts[1].Experiment.Families[0].Scenario.Reserves[0][0].Build[0]++
	if !reflect.DeepEqual(owned, loaded) {
		t.Fatal("constructed envelope aliases caller memory")
	}
	for _, raw := range []string{`{"unknown":1}`, string(mustJSON(t, owned)) + "\n{}"} {
		bad := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(bad, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMixedExperiment(bad); err == nil {
			t.Fatal("invalid mixed document accepted")
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMixedExperimentRejectsInvalidBindings(t *testing.T) {
	for _, kind := range []string{"schema", "missing", "duplicate", "wrong-digest", "wrong-mode", "environment", "zero-quota", "excess-quota", "changed-split", "legacy-pairing"} {
		t.Run(kind, func(t *testing.T) {
			x := mixedExperimentFixture(t)
			switch kind {
			case "schema":
				x.Schema = "commander-experiment-v2"
			case "missing":
				x.Parts = x.Parts[:1]
			case "duplicate":
				x.Parts[1] = x.Parts[0]
			case "wrong-digest":
				x.Parts[0].Digest = strings.Repeat("0", 64)
			case "wrong-mode":
				x.Mixture.Modes[1].Mode = 4
			case "environment":
				x.Parts[1].Experiment.Environment.Platform = "linux-amd64"
				x.Parts[1].Digest, _ = Digest(x.Parts[1].Experiment)
			case "zero-quota":
				x.Parts[0].FamiliesPerBatch = 0
			case "excess-quota":
				x.Parts[0].FamiliesPerBatch = 125
			case "changed-split":
				x.Parts[0].Experiment.Families[0].Split = "test"
			case "legacy-pairing":
				x.Parts[0].Experiment.Pairing = ""
				x.Parts[0].Experiment.Schema = experimentSchema("")
				x.Parts[0].Digest, _ = Digest(x.Parts[0].Experiment)
			}
			if x.Validate() == nil {
				t.Fatal("incompatible mixed experiment accepted")
			}
			if _, err := NewMixedSchedule(x, 42); err == nil {
				t.Fatal("invalid manifest reached sampler")
			}
		})
	}
}

func TestMixedExperimentSharedParentAndCrossPartSelection(t *testing.T) {
	parent := experimentCandidate(t, experimentFixture(t))
	parentID, _ := Digest(parent)
	var parts []MixedExperimentPart
	for _, mode := range []int{1, 5} {
		c := DefaultEvaluationConfig()
		c.Mode, c.Seed = mode, int64(918+mode)
		x, err := NewExperimentFromSources(context.Background(), parent.Environment, c, [3]int{3, 2, 2}, nil, &parent)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
	}
	x, err := NewMixedExperiment(testModeMixture(), parts, &parent)
	if err != nil {
		t.Fatal(err)
	}
	if x.ValidateParent(nil) == nil || !reflect.DeepEqual(parent.Modes, []int{1}) {
		t.Fatal("mixed initialization skipped parent verification or conferred mode5 support")
	}
	id, _ := Digest(parent)
	if parentID != id {
		t.Fatal("constructor modified parent artifact")
	}
	changedParent := parent
	changedParent.TrainingReport = strings.Repeat("f", 64)
	if x.ValidateParent(&changedParent) == nil {
		t.Fatal("another parent accepted")
	}
	bad := cloneMixedExperiment(t, x)
	bad.Parts[1].Experiment.InitialTrainingGroups = []string{strings.Repeat("a", 64)}
	bad.Parts[1].Digest, _ = Digest(bad.Parts[1].Experiment)
	if err := bad.Parts[1].Experiment.Validate(); err != nil {
		t.Fatal("test child must be individually valid", err)
	}
	if bad.Validate() == nil {
		t.Fatal("different parent ancestry accepted under a shared identity")
	}
	// Build-selection ancestry can refer to a family of another team size.
	// Each child individually validates, yet their composition must reject it.
	for _, split := range []string{"train", "validation", "test"} {
		pool := buildPoolFixture(t)
		c := DefaultEvaluationConfig()
		c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 20
		one, err := NewExperimentFromSources(context.Background(), pool.Environment, c, [3]int{3, 2, 2}, &pool, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Mode = 5
		five, err := NewExperiment(context.Background(), pool.Environment, c, [3]int{3, 2, 2})
		if err != nil {
			t.Fatal(err)
		}
		pool.SelectionGroups = sortedUnion(pool.SelectionGroups, []string{five.groups(split)[0].Group})
		one.Pool, one.SelectionGroups = &pool, pool.SelectionGroups
		if err := one.Validate(); err != nil {
			t.Fatal("test mode1 must remain valid", err)
		}
		if err := five.Validate(); err != nil {
			t.Fatal("test mode5 must remain valid", err)
		}
		if _, err := NewMixedExperiment(testModeMixture(), []MixedExperimentPart{{Experiment: one, FamiliesPerBatch: 1}, {Experiment: five, FamiliesPerBatch: 1}}, nil); err == nil {
			t.Fatalf("mode1 selection leaked into mode5 %s", split)
		}
	}
}

func TestMixedScheduleBalancedFamiliesResumeAndConcurrentReads(t *testing.T) {
	x := mixedExperimentFixture(t)
	schedule, err := NewMixedSchedule(x, 1207)
	if err != nil || schedule.BatchMatches() != 24 {
		t.Fatal("wrong total quota", err)
	}
	const games = 96
	var expected [games]MixedScheduledGame
	localCounts := map[int]uint64{}
	for i := range expected {
		g, err := schedule.Game(uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		wantMode := 1
		if i%24/8 == 1 {
			wantMode = 5
		}
		if g.Mode != wantMode || g.ModeGame != localCounts[g.Mode] || g.Index != uint64(i) || g.Batch != uint64(i/24) || ScenarioGroup(g.Scenario) != g.Group {
			t.Fatal("mode quota/local cursor/group mismatch", g)
		}
		localCounts[g.Mode]++
		expected[i] = g
		part := x.Parts[0]
		if g.Mode == 5 {
			part = x.Parts[1]
		}
		found := false
		for _, f := range part.Experiment.groups("train") {
			found = found || f.Group == g.Group
		}
		if !found || g.Experiment != part.Digest {
			t.Fatal("schedule escaped frozen mode training split")
		}
	}
	for start := 0; start < games; start += 8 {
		var family []EvaluationGame
		for _, g := range expected[start : start+8] {
			family = append(family, EvaluationGame{Group: g.Group, Scenario: g.Scenario, CandidateSide: int(g.ModeGame % 2), Opponent: "basic", OpponentPolicy: strings.Repeat("0", 64)})
		}
		if err := validateBalancedGames(family); err != nil {
			t.Fatal("mixed schedule broke roster/side/seed crossing", err)
		}
	}
	// Serialization and random access simulate resumption at arbitrary committed
	// prefixes. This is schedule recovery, not full optimizer/collector recovery.
	restored, err := NewMixedSchedule(cloneMixedExperiment(t, x), 1207)
	if err != nil {
		t.Fatal(err)
	}
	permutation := rand.New(rand.NewSource(44)).Perm(games)
	var wg sync.WaitGroup
	for _, index := range permutation {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := restored.Game(uint64(i))
			if err != nil || !reflect.DeepEqual(got, expected[i]) {
				t.Errorf("worker ordering changed index %d: %v", i, err)
			}
		}(index)
	}
	wg.Wait()
	x.Parts[0].Experiment.Families[0].Scenario.Builds[0][0]++
	got, _ := restored.Game(0)
	got.Scenario.Builds[0][0]++
	got.Scenario.PetBuilds[0][0]++
	got.Scenario.Reserves[0][0].Build[0]++
	unchanged, _ := restored.Game(0)
	if !reflect.DeepEqual(unchanged, expected[0]) {
		t.Fatal("caller scenario mutation corrupted immutable sampler")
	}
	for _, s := range []*MixedSchedule{nil, {}} {
		if _, err := s.Game(0); err == nil {
			t.Fatal("uninitialized sampler accepted")
		}
	}
	if _, err := restored.Game(uint64(math.MaxInt32) + 1); err == nil {
		t.Fatal("overflowing game counter accepted")
	}
}
