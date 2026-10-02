package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestBuildScorerFitsActualRostersAcrossModesAndSkillContracts(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		for reserves := 0; reserves <= 2; reserves++ {
			for _, mask := range []int{0, 131, 223} {
				c := DefaultBuildSearchConfig()
				c.Mode, c.ReservePets, c.PetSkillMask = mode, reserves, mask
				c.InitialCandidates = 5
				rosters, err := initialRosters(c)
				if err != nil {
					t.Fatal(err)
				}
				features := [][]float32{rosters[0].features(c), rosters[1].features(c)}
				scorer, err := battlenet.FitBuildScorer(context.Background(), features, []float32{.25, .75}, 1, 23)
				if err != nil {
					t.Fatalf("mode=%d reserves=%d skills=%d: %v", mode, reserves, mask, err)
				}
				raw, err := json.Marshal(scorer)
				if err != nil {
					t.Fatal(err)
				}
				var restored battlenet.BuildScorer[float32]
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				for _, roster := range rosters {
					want, err := scorer.Predict(roster.features(c))
					got, restoreErr := restored.Predict(roster.features(c))
					if err != nil || restoreErr != nil || want != got {
						t.Fatal("fitted roster scorer cannot round-trip", err, restoreErr, want, got)
					}
				}
			}
		}
	}
}

func TestBuildIntegerBudgetsAndBlindFeatures(t *testing.T) {
	c := DefaultBuildSearchConfig()
	rng := rand.New(rand.NewSource(93))
	for mode := 1; mode <= 5; mode++ {
		c.Mode = mode
		for _, pets := range []int{0, 4, 121} {
			c.Points, c.PetPoints = 123, pets
			initial, e := initialRosters(c)
			if e != nil {
				t.Fatal(e)
			}
			seen := map[string]bool{}
			for _, r := range initial {
				if e = r.validate(c); e != nil || seen[r.ID()] {
					t.Fatal("invalid/duplicate initial build", e)
				}
				seen[r.ID()] = true
			}
			parent := initial[0]
			for i := 0; i < 500; i++ {
				r := mutateRoster(parent, c, rng)
				if e = r.validate(c); e != nil {
					t.Fatal(e)
				}
				parent = r
			}
			f := parent.features(c)
			c.Seed++
			c.SearchGroups++
			if !reflect.DeepEqual(f, parent.features(c)) {
				t.Fatal("search/opponent distribution entered own-build features")
			}
			for i := 0; i < len(f); i += 4 {
				sum := f[i] + f[i+1] + f[i+2] + f[i+3]
				if sum < .99999 || sum > 1.00001 {
					t.Fatal("features lost actor budget")
				}
			}
		}
	}
}

func TestBuildSearchPartitionsAndIntegrity(t *testing.T) {
	c := DefaultBuildSearchConfig()
	c.InitialCandidates, c.SearchGroups, c.ValidationGroups, c.TestGroups = 5, 2, 2, 2
	meta := battleenv.Metadata{Rules: strings.Repeat("0", 64), Platform: "linux-arm64", Scenario: "controlled-battle-v8"}
	x, _, _, e := newBuildSearchSpec(c, meta, Opponent{Name: "own", Rule: "basic"}, []Opponent{{Name: "basic", Rule: "basic"}})
	if e != nil {
		t.Fatal(e)
	}
	initial, _ := initialRosters(c)
	s := BuildSearchState{Spec: x, Stage: "search", Pending: initial}
	if e = validateBuildSearch(s); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = saveBuildSearch(dir, s); e != nil {
		t.Fatal(e)
	}
	restored, e := LoadBuildSearch(dir)
	if e != nil || !reflect.DeepEqual(s, restored) {
		t.Fatal("search state changed", e)
	}
	bad := s
	bad.Spec.Validation = append([]Roster(nil), x.Validation...)
	bad.Spec.Validation[0] = x.Search[0]
	if validateBuildSearch(bad) == nil {
		t.Fatal("heldout opponent configuration reused")
	}
	bad = s
	bad.Pending = append([]Roster(nil), initial...)
	bad.Pending[0] = initial[1]
	if validateBuildSearch(bad) == nil {
		t.Fatal("pending plan altered")
	}
	if _, e = SaveBuildSearchReport(dir, s); e == nil {
		t.Fatal("unfinished search reported advice")
	}
	// The pointer and state remain intact; a changed referenced spec must
	// still be rejected by its own checksum, not trusted by its filename.
	stateID, _ := Digest(s)
	var disk buildSearchDisk
	if e = readObject(filepath.Join(dir, "search-states", stateID+".json"), &disk, 1<<20); e != nil {
		t.Fatal(e)
	}
	changed := s.Spec
	changed.Config.Seed++
	raw, _ := json.Marshal(changed)
	if e = os.WriteFile(filepath.Join(dir, "search-objects", disk.Spec+".json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadBuildSearch(dir); e == nil {
		t.Fatal("corrupt referenced search object accepted")
	}
	if e = os.WriteFile(filepath.Join(dir, "search-latest.json"), []byte(`{"state":"../outside"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadBuildSearch(dir); e == nil {
		t.Fatal("invalid search pointer accepted")
	}
}

func TestNativeBuildSearchResumeAndRecheck(t *testing.T) {
	testNativeBuildSearchResume(t, false)
}

func TestNativeGuardianBuildSearchResumeAndRecheck(t *testing.T) {
	testNativeBuildSearchResume(t, true)
}

func testNativeBuildSearchResume(t *testing.T, guardian bool) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if guardian {
		raw = os.Getenv("STONEAGE_BATTLE_GUARDIAN_ENV_COMMAND")
	}
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	start := func() *battleenv.Native {
		engine, e := battleenv.Start(ctx, command, io.Discard)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { engine.Close() })
		return engine
	}
	c := DefaultBuildSearchConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 300
	if guardian {
		c.PetPoints, c.ReservePets, c.PetSkillMask, c.MaxTurns = 20, 1, 131, 1000
	}
	c.InitialCandidates, c.Generations, c.Proposals, c.NativeCandidates, c.Finalists, c.FitEpochs = 5, 1, 8, 1, 2, 2
	c.SearchGroups, c.ValidationGroups, c.TestGroups = 1, 1, 1
	one, two := t.TempDir(), t.TempDir()
	base := BuildSearchOptions{Directory: one, Engine: start(), Controller: Opponent{Name: "own", Rule: "basic"}, Opponents: []Opponent{{Name: "basic", Rule: "basic"}}, Config: c}
	stop := errors.New("intentional committed-game interruption")
	base.Progress = func(stage string, games int) error { return stop }
	if _, e := RunBuildSearch(ctx, base); !errors.Is(e, stop) {
		t.Fatal("initial interruption not observed", e)
	}
	s, e := LoadBuildSearch(one)
	if e != nil || len(s.Search) != 1 || len(s.Search[0].Games) != 1 {
		t.Fatal("first game not committed", e)
	}
	base.Engine.Close()
	base.Engine, base.Resume = start(), true
	base.Progress = func(stage string, games int) error {
		if stage == "test" {
			return stop
		}
		return nil
	}
	if _, e = RunBuildSearch(ctx, base); !errors.Is(e, stop) {
		t.Fatal("test-stage interruption not observed", e)
	}
	s, e = LoadBuildSearch(one)
	if e != nil || s.Selected == "" || len(s.Test) != 1 || len(s.Test[0].Games) != 1 {
		t.Fatal("final choice was not frozen before testing", e)
	}
	chosen := s.Selected
	base.Engine.Close()
	base.Engine, base.Progress = start(), nil
	a, e := RunBuildSearch(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	if a.Selected != chosen || a.Stage != "complete" {
		t.Fatal("final test changed chosen build")
	}
	if _, e = SaveBuildSearchReport(one, a); e != nil {
		t.Fatal(e)
	}
	base.Engine.Close()
	base.Engine, base.Directory, base.Resume = start(), two, false
	b, e := RunBuildSearch(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	if a.Selected != b.Selected || !reflect.DeepEqual(a.Scorer, b.Scorer) {
		t.Fatal("resume changed build selection/scorer")
	}
	// Worker-specific trajectory identities differ; numerical match outcomes
	// and complete paired scenario schedules must still agree.
	normalize := func(s BuildSearchState) BuildSearchState {
		for _, list := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
			for i := range list {
				for j := range list[i].Games {
					list[i].Games[j].Shard = ""
				}
			}
		}
		return s
	}
	copyState := func(s BuildSearchState) BuildSearchState {
		raw, _ := json.Marshal(s)
		var out BuildSearchState
		json.Unmarshal(raw, &out)
		return out
	}
	if !reflect.DeepEqual(normalize(copyState(a)), normalize(copyState(b))) {
		t.Fatal("resumed outcomes/schedule differ")
	}
	bad := copyState(a)
	bad.Test[0].Games[0].Winner = 1 - bad.Test[0].Games[0].CandidateSide
	// Regardless of whether the chosen winner happens to equal the old one,
	// an unrelated but valid-looking shard may never substitute for this game.
	bad.Test[0].Games[0].Shard = bad.Search[0].Games[0].Shard
	if e = verifyBuildSearchData(one, bad); e == nil {
		t.Fatal("unrelated native trajectory accepted")
	}
	if _, e = SaveBuildSearchReport(one, bad); e == nil {
		t.Fatal("unrelated native evidence accepted")
	}
	bad = copyState(a)
	bad.Selected = strings.Repeat("f", 64)
	if validateBuildSearch(bad) == nil {
		t.Fatal("post-test choice accepted")
	}
	base.Engine.Close()
	base.Engine, base.Directory, base.Resume = start(), one, true
	base.Config.Points++
	if _, e = RunBuildSearch(ctx, base); e == nil {
		t.Fatal("resume changed integer budget")
	}
	t.Logf("native build search: %d initial/refined builds, %d finalists, frozen final selection, resumed scorer/outcomes identical", len(a.Search), len(a.Validation))
}

func TestNativeBuildSearchCutoffsAreNotScores(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	c := DefaultBuildSearchConfig()
	c.PetPoints, c.MaxTurns = 0, 1
	c.InitialCandidates, c.Generations, c.Proposals, c.NativeCandidates, c.Finalists, c.FitEpochs = 5, 1, 8, 1, 2, 1
	c.SearchGroups, c.ValidationGroups, c.TestGroups = 1, 1, 1
	dir := t.TempDir()
	s, e := RunBuildSearch(ctx, BuildSearchOptions{Directory: dir, Engine: engine, Controller: Opponent{Name: "own", Rule: "basic"}, Opponents: []Opponent{{Name: "basic", Rule: "basic"}}, Config: c})
	if e == nil || !strings.Contains(e.Error(), "collection cutoffs") {
		t.Fatal("cutoff wasn't explicit", e)
	}
	if len(s.Search) != 1 || len(s.Validation) != 0 || s.Scorer != nil || s.Selected != "" {
		t.Fatal("cutoff used for build fitting/selection")
	}
	_, truncated := buildScore(s.Search[0])
	if truncated == 0 {
		t.Fatal("no native cutoff observed")
	}
	for _, g := range s.Search[0].Games {
		if g.Truncated && (g.Terminated || g.Winner != -1) {
			t.Fatal("cutoff converted to a result")
		}
	}
	if _, e = LoadBuildSearch(dir); e != nil {
		t.Fatal("cutoff evidence lost", e)
	}
	if _, e = SaveBuildSearchReport(dir, s); e == nil {
		t.Fatal("cutoff generated a recommendation")
	}
}
