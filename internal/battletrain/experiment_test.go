package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func experimentFixture(t *testing.T) Experiment {
	t.Helper()
	x, e := NewExperiment(context.Background(), battleenv.Metadata{Rules: strings.Repeat("0", 64), Platform: "linux-arm64", Scenario: "controlled-battle-v8"}, DefaultEvaluationConfig(), [3]int{5, 3, 2})
	if e != nil {
		t.Fatal(e)
	}
	return x
}

func TestExperimentSplitsAndSchedule(t *testing.T) {
	x := experimentFixture(t)
	if !reflect.DeepEqual(x, experimentFixture(t)) {
		t.Fatal("manifest generation isn't deterministic")
	}
	byGroup := map[string]string{}
	for _, f := range x.Families {
		byGroup[f.Group] = f.Split
	}
	for _, split := range []string{"validation", "test"} {
		suite, e := x.evaluationSuite(split)
		if e != nil || len(suite) != len(x.groups(split))*8 {
			t.Fatal(e, len(suite))
		}
		for i, s := range suite {
			if byGroup[ScenarioGroup(s)] != split {
				t.Fatal("swap/repeat crossed a split")
			}
			if i%8 == 0 && (s.Seed != suite[i+3].Seed || s.Seed == suite[i+4].Seed || s.Builds[0] != suite[i+1].Builds[x.Mode]) {
				t.Fatal("paired scenario schedule changed")
			}
		}
	}
	for seed := int64(1); seed < 5; seed++ {
		for game := uint64(0); game < 100; game++ {
			s, group := x.trainingScenario(seed, game)
			if group != ScenarioGroup(s) || byGroup[group] != "train" {
				t.Fatal("training escaped its declared split")
			}
		}
	}
	a, _ := x.trainingScenario(1, 0)
	b, _ := x.trainingScenario(1, 40)
	if a.Seed == b.Seed || ScenarioGroup(a) != ScenarioGroup(b) {
		t.Fatal("new pass must retain families with new random seeds")
	}
	bad := x
	bad.Families = append([]ExperimentFamily(nil), x.Families...)
	bad.Families[5] = bad.Families[0]
	bad.Families[5].Split = "validation"
	bad.Families[5].Scenario.Seed++
	if bad.Validate() == nil {
		t.Fatal("reseeded duplicate family accepted in a different split")
	}
	bad = x
	bad.Points++
	if bad.Validate() == nil {
		t.Fatal("budget metadata mismatch accepted")
	}
	if _, e := x.evaluationSuite("train"); e == nil {
		t.Fatal("training set accepted as independent evaluation")
	}
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints = 4, 0
	if _, e := NewExperiment(context.Background(), x.Environment, c, [3]int{1, 1, 1}); e == nil {
		t.Fatal("impossible disjoint minimum-budget experiment accepted")
	}
}

func experimentCandidate(t *testing.T, x Experiment) battlepolicy.Artifact {
	t.Helper()
	m := testModel(t)
	id, _ := Digest(x)
	weights, _ := ModelDigest(m)
	return battlepolicy.Artifact{Schema: 2, Architecture: "commander-policy-v2", Features: battlepolicy.FeatureVersion, Actions: battlepolicy.ActionVersion, Status: "candidate", Environment: x.Environment, Modes: []int{x.Mode}, WeightsDigest: weights, Experiment: id, HeldoutGroups: x.heldoutGroups(), TrainingReport: strings.Repeat("1", 64), TrainingShards: []string{strings.Repeat("2", 64)}, TrainingGroups: []string{x.groups("train")[0].Group}, Network: m}
}

func TestExperimentPersistenceAndFinalSelection(t *testing.T) {
	x := experimentFixture(t)
	path := filepath.Join(t.TempDir(), "experiment.json")
	if _, e := SaveExperiment(path, x); e != nil {
		t.Fatal(e)
	}
	got, e := LoadExperiment(path)
	if e != nil || !reflect.DeepEqual(x, got) {
		t.Fatal("manifest changed on load", e)
	}
	changed := x
	changed.Seed++
	if _, e = SaveExperiment(path, changed); e == nil {
		t.Fatal("immutable manifest overwritten")
	}
	a := experimentCandidate(t, x)
	opponents := []Opponent{{Name: "basic", Rule: "basic"}, {Name: "focus", Rule: "focus"}}
	selection := path + ".test-selection.json"
	if e = FreezeFinalSelection(selection, x, a, opponents); e != nil {
		t.Fatal(e)
	}
	opponents[0], opponents[1] = opponents[1], opponents[0]
	if e = FreezeFinalSelection(selection, x, a, opponents); e != nil {
		t.Fatal("same selection couldn't retry", e)
	}
	other := a
	other.TrainingReport = strings.Repeat("3", 64)
	if e = FreezeFinalSelection(selection, x, other, opponents); e == nil {
		t.Fatal("new candidate reused final test after selection")
	}
	if e = FreezeFinalSelection(selection, x, a, opponents[:1]); e == nil {
		t.Fatal("opponent set changed after final selection")
	}
	other.TrainingGroups = []string{x.groups("test")[0].Group}
	if e = FreezeFinalSelection(selection+"-bad", x, other, opponents); e == nil {
		t.Fatal("held-out training contamination accepted")
	}
	if _, e = os.Stat(selection + "-bad"); !os.IsNotExist(e) {
		t.Fatal("invalid selection left a test lock")
	}
}

func TestNativeExperimentResumeAndHeldoutEvaluation(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	ec := DefaultEvaluationConfig()
	ec.MaxTurns = 3
	ec.HealingMagic = 20
	ec.HealingItems = 2
	ec.ReservePets = 2
	x, e := NewExperiment(ctx, engine.Metadata(), ec, [3]int{2, 1, 1})
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultRunConfig()
	c.HealingMagic = ec.HealingMagic
	c.HealingItems = ec.HealingItems
	c.ReservePets = ec.ReservePets
	c.Experiment, _ = Digest(x)
	c.Network, c.MaxTurns, c.BatchMatches = testModel(t).Config, 3, 2
	c.PPO.Epochs = 1
	c.Warmup.Matches, c.Warmup.Epochs = 4, 1
	c.Warmup.Teachers = []string{"sustain"}
	c.RuleOpponents = []string{"sustain"}
	one, two := t.TempDir(), t.TempDir()
	stop := errors.New("stop after committed warmup game")
	e = Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Experiment: &x, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "warmup_collected" {
			return stop
		}
		return nil
	}})
	if !errors.Is(e, stop) {
		t.Fatal("warmup interruption failed", e)
	}
	if e = Run(ctx, RunOptions{Directory: one, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	if e = Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Experiment: &x, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	ca, sa, e := LoadCheckpoint(one)
	if e != nil {
		t.Fatal(e)
	}
	cb, sb, e := LoadCheckpoint(two)
	if ca.Config.ReservePets != 2 || cb.Config.ReservePets != 2 || ca.Config.HealingMagic != 20 || cb.Config.HealingMagic != 20 || ca.Config.HealingItems != 2 || cb.Config.HealingItems != 2 {
		t.Fatal("resume dropped frozen healing loadout")
	}
	if !reflect.DeepEqual(ca.Config.RuleOpponents, []string{"sustain"}) || !reflect.DeepEqual(ca.Config.Warmup.Teachers, []string{"sustain"}) {
		t.Fatal("resume changed the teacher/opponent pool")
	}
	// Each native worker has its own match/event identity, so independently
	// collected shards are intentionally distinct. Learned numerical state,
	// scenario provenance and progress must nevertheless resume exactly.
	if e != nil || !reflect.DeepEqual(sa, sb) || ca.NextGame != cb.NextGame || ca.CompletedBatches != cb.CompletedBatches || !reflect.DeepEqual(ca.Config, cb.Config) || !reflect.DeepEqual(ca.TrainingGroups, cb.TrainingGroups) {
		t.Fatalf("experiment resume changed state: err=%v weights/optimizer equal=%v counts=%d/%d", e, reflect.DeepEqual(sa, sb), ca.NextGame, cb.NextGame)
	}
	allowed := map[string]bool{}
	for _, f := range x.groups("train") {
		allowed[f.Group] = true
	}
	for _, group := range ca.TrainingGroups {
		if !allowed[group] {
			t.Fatal("warmup/PPO leaked into held-out split")
		}
	}
	path, e := ExportCandidate(one, "")
	if e != nil {
		t.Fatal(e)
	}
	a, e := battlepolicy.LoadArtifact(path)
	if e != nil || a.Experiment != c.Experiment {
		t.Fatal("export lost experiment provenance", e)
	}
	for _, split := range []string{"validation", "test"} {
		reportPath := filepath.Join(one, split+".json")
		r, e := EvaluateRecorded(ctx, engine, a, []Opponent{{Name: "basic", Rule: "basic"}}, ec, &x, split, reportPath, nil)
		if e != nil || len(r.Games) != 8 {
			t.Fatal(split, e)
		}
		if r.Config.ReservePets != 2 || r.Config.HealingMagic != 20 || r.Config.HealingItems != 2 {
			t.Fatal("evaluation dropped healing loadout")
		}
		for _, g := range r.Games {
			if reserveCount(g.Scenario.Reserves) != 2 || g.Scenario.HealingMagic != 20 || g.Scenario.HealingItems != 2 {
				t.Fatal("evaluation played a different loadout")
			}
			if allowed[g.Group] {
				t.Fatal("evaluation reused training family")
			}
		}
		if _, e = VerifyEvaluation(ctx, reportPath); e != nil {
			t.Fatal(e)
		}
		r.Games[1] = r.Games[0]
		r.Comparisons = Summarize(r.Games, r.Config.Seed)
		if e = SaveEvaluation(filepath.Join(one, "bad-"+split+".json"), r); e == nil {
			t.Fatal("duplicate game passed frozen suite validation")
		}
	}
	bad := a
	bad.TrainingGroups = []string{x.groups("validation")[0].Group}
	if _, e = EvaluateExperiment(ctx, engine, bad, []Opponent{{Name: "basic", Rule: "basic"}}, x, "validation", nil); e == nil {
		t.Fatal("held-out contamination accepted")
	}
	if e = os.WriteFile(filepath.Join(one, "experiments", c.Experiment+".json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadCheckpoint(one); e == nil {
		t.Fatal("resume accepted replaced experiment")
	}
	t.Log("frozen experiment: warmup/PPO resume, train-only provenance, validation/test suites and corruption rejection passed")
}
