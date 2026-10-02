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

func TestRecordedBuildSearchTracksBothRolesAndVerifiesPinnedSources(t *testing.T) {
	a := recordedEvaluationFixture(t)
	b := a
	r := *a.Recorded
	r.Dataset = strings.Repeat("e", 64)
	b.Recorded = &r
	meta := a.Environment
	meta.Scenario = "controlled-battle-v8"
	c := DefaultBuildSearchConfig()
	c.InitialCandidates, c.SearchGroups, c.ValidationGroups, c.TestGroups = 5, 1, 1, 1
	aID, _ := Digest(a)
	bID, _ := Digest(b)
	for _, role := range []string{"controller", "opponent", "both"} {
		t.Run(role, func(t *testing.T) {
			own, opponent := Opponent{Name: "own", Rule: "basic"}, Opponent{Name: "other", Rule: "basic"}
			var want []string
			if role != "opponent" {
				own.Rule, own.Model = "", &a
				want = append(want, aID)
			}
			if role != "controller" {
				opponent.Rule, opponent.Model = "", &b
				want = append(want, bID)
			}
			x, _, _, err := newBuildSearchSpec(c, meta, own, []Opponent{opponent})
			if err != nil || !reflect.DeepEqual(x.RecordedSelectionArtifacts, sortedUnion(want)) || len(x.ExcludedPolicyTrainingGroups) != 0 {
				t.Fatal("recorded source lost or became synthetic group", err)
			}
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "search-policies"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, model := range []battlepolicy.Artifact{a, b} {
				id, _ := Digest(model)
				if err := writeObject(filepath.Join(root, "search-policies", id+".json"), model); err != nil {
					t.Fatal(err)
				}
			}
			pending, _ := initialRosters(c)
			s := BuildSearchState{Spec: x, Stage: "search", Pending: pending}
			if err := saveBuildSearch(root, s); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadBuildSearch(root)
			if err != nil || verifyBuildSearchData(root, loaded) != nil {
				t.Fatal("cannot verify pinned sources", err)
			}
			// Recomputed state hashes cannot legitimize omitted source metadata.
			loaded.Spec.RecordedSelectionArtifacts = nil
			if err := verifyBuildSearchData(root, loaded); err == nil {
				t.Fatal("source omission accepted")
			}
		})
	}
}

func TestRecordedPoolSelectionSurvivesScratchAndParentTraining(t *testing.T) {
	recorded := recordedEvaluationFixture(t)
	pool := buildPoolFixture(t)
	pool.Environment = recorded.Environment
	pool.Environment.Scenario = "controlled-battle-v8"
	id, _ := Digest(recorded)
	pool.RecordedSelectionArtifacts = []string{id}
	native := experimentCandidate(t, experimentFixture(t))
	native.Environment = pool.Environment
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints, c.Level = pool.Config.Points, pool.Config.PetPoints, pool.Config.Level
	for _, parent := range []*battlepolicy.Artifact{nil, &native, &recorded} {
		x, err := NewExperimentFromSources(context.Background(), pool.Environment, c, [3]int{2, 1, 1}, &pool, parent)
		if err != nil || x.validateInitialModel(parent) != nil {
			t.Fatal("cannot use recorded selection pool", err)
		}
		child := experimentCandidate(t, x)
		child.Schema, child.Recorded, child.Parent = 5, x.InitialRecorded, x.InitialModel
		child.RecordedSelectionArtifacts, child.SelectionGroups = x.RecordedSelectionArtifacts, x.SelectionGroups
		child.TrainingGroups = sortedUnion(child.TrainingGroups, x.InitialTrainingGroups)
		if err := child.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := x.validateCandidateProvenance(child); err != nil {
			t.Fatal(err)
		}
		if (child.Recorded == nil) != (parent == nil || parent.Recorded == nil) {
			t.Fatal("selection influence was mislabeled as gradient ancestry")
		}
		s, err := prepareEvaluation(context.Background(), pool.Environment, child, []Opponent{{Name: "basic", Rule: "basic"}}, c, &x, "validation")
		childID, _ := Digest(child)
		if err != nil || !reflect.DeepEqual(s.Report.UnverifiedSourceArtifacts, []string{childID}) {
			t.Fatal("selection uncertainty missing from evaluation", err)
		}
		if _, err := assessPromotion(s.Report, DefaultPromotionGate(), 1, false); err == nil || !strings.Contains(err.Error(), "source separation") {
			t.Fatal("uncertain selected model promoted", err)
		}
		report := s.Report
		for i, scenario := range s.Suite {
			report.Games = append(report.Games, EvaluationGame{Opponent: "basic", OpponentPolicy: s.Versions[0], Group: ScenarioGroup(scenario), Scenario: scenario, CandidateSide: i % 2, Turns: 1, Winner: 0, Terminated: true})
		}
		report.Comparisons = Summarize(report.Games, report.Config.Seed)
		if err := ValidateEvaluation(report); err != nil {
			t.Fatal(err)
		}
		report.UnverifiedSourceArtifacts = nil
		if ValidateEvaluation(report) == nil {
			t.Fatal("self-contained report omitted selection uncertainty")
		}
		if parent == &native {
			baseline, err := prepareEvaluation(context.Background(), pool.Environment, native, []Opponent{{Name: "basic", Rule: "basic"}}, c, &x, "validation")
			if err != nil || len(baseline.Report.UnverifiedSourceArtifacts) != 0 {
				t.Fatal("untrained native parent incorrectly acquired future pool influence", err)
			}
			opponentCase, err := prepareEvaluation(context.Background(), pool.Environment, native, []Opponent{{Name: "selected", Model: &child}}, c, &x, "validation")
			if err != nil || !reflect.DeepEqual(opponentCase.Report.UnverifiedSourceArtifacts, []string{childID}) {
				t.Fatal("selected opponent uncertainty lost", err)
			}
		}
		next, err := NewExperimentFromSources(context.Background(), pool.Environment, c, [3]int{2, 1, 1}, nil, &child)
		if err != nil || next.validateInitialModel(&child) != nil || !reflect.DeepEqual(next.RecordedSelectionArtifacts, []string{id}) {
			t.Fatal("next generation lost selection source", err)
		}
		searchConfig := pool.Config
		searchConfig.InitialCandidates, searchConfig.SearchGroups, searchConfig.ValidationGroups, searchConfig.TestGroups = 5, 1, 1, 1
		search, _, _, err := newBuildSearchSpec(searchConfig, pool.Environment, Opponent{Name: "own", Rule: "basic"}, []Opponent{{Name: "selected", Model: &child}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{id}
		if child.Recorded != nil {
			want = sortedUnion(want, []string{childID})
		}
		if !reflect.DeepEqual(search.RecordedSelectionArtifacts, want) {
			t.Fatal("repeated search dropped inherited recorded selection")
		}
		bad := child
		bad.RecordedSelectionArtifacts = nil
		if bad.Validate() == nil || x.validateCandidateProvenance(bad) == nil {
			t.Fatal("selection source omission accepted")
		}
		bad = child
		bad.Schema = 2
		if bad.Validate() == nil {
			t.Fatal("legacy schema accepted new selection influence")
		}
		badExperiment := x
		badExperiment.RecordedSelectionArtifacts = nil
		if badExperiment.Validate() == nil || badExperiment.validateInitialModel(parent) == nil {
			t.Fatal("experiment laundered pool source")
		}
	}
}

// Requires an actual exported recorded model and an isolated existing engine.
// The search uses that model as an opponent; the new policy starts from random
// weights, so the only possible recorded influence is through roster selection.
func TestNativeRecordedBuildPoolFromScratch(t *testing.T) {
	raw, modelPath := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND"), os.Getenv("STONEAGE_RECORDED_MODEL")
	if raw == "" || modelPath == "" {
		t.Skip("explicit native engine and recorded model required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	model, err := battlepolicy.LoadArtifact(modelPath)
	if err != nil || model.Recorded == nil {
		t.Fatal("recorded source model required", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	root := os.Getenv("STONEAGE_RECORDED_SELECTION_OUTPUT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("evidence output must be new", err)
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	c := DefaultBuildSearchConfig()
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = model.Modes[0], 20, 0, 10, 300
	c.InitialCandidates, c.Generations, c.Proposals, c.NativeCandidates, c.Finalists, c.FitEpochs = 5, 1, 8, 1, 2, 1
	c.SearchGroups, c.ValidationGroups, c.TestGroups = 1, 1, 1
	searchRoot := filepath.Join(root, "search")
	options := BuildSearchOptions{Directory: searchRoot, Engine: engine, Controller: Opponent{Name: "basic", Rule: "basic"}, Opponents: []Opponent{{Name: "recorded", Model: &model}}, Config: c}
	stop := errors.New("interrupt after committed game")
	options.Progress = func(stage string, games int) error {
		if games == 1 {
			return stop
		}
		return nil
	}
	if _, err := RunBuildSearch(ctx, options); !errors.Is(err, stop) {
		t.Fatal("search did not interrupt after a committed game", err)
	}
	options.Resume, options.Progress = true, func(stage string, games int) error {
		if games%4 == 0 {
			t.Logf("search stage=%s games=%d", stage, games)
		}
		return nil
	}
	state, err := RunBuildSearch(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveBuildSearchReport(searchRoot, state); err != nil {
		t.Fatal(err)
	}
	pool, err := ExportBuildPool(searchRoot, "")
	modelID, _ := Digest(model)
	if err != nil || !reflect.DeepEqual(pool.RecordedSelectionArtifacts, []string{modelID}) {
		t.Fatal("pool lost actual recorded opponent", err)
	}
	ec := DefaultEvaluationConfig()
	ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = c.Mode, c.Points, c.PetPoints, c.Level, 3
	x, err := NewExperimentFromSources(ctx, engine.Metadata(), ec, [3]int{2, 1, 1}, &pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeObject(filepath.Join(root, "experiment.json"), x); err != nil {
		t.Fatal(err)
	}
	config := DefaultRunConfig()
	config.Network, config.Warmup = testModel(t).Config, nil
	config.Mode, config.Points, config.PetPoints, config.Level, config.MaxTurns = ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns
	config.BatchMatches, config.PPO.Epochs = 2, 1
	config.Experiment, _ = Digest(x)
	resumed, continuous := filepath.Join(root, "resumed"), filepath.Join(root, "continuous")
	err = Run(ctx, RunOptions{Directory: resumed, Command: command, Config: &config, Experiment: &x, Batches: 1, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.Event == "collected" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal("PPO interruption missing", err)
	}
	if err := Run(ctx, RunOptions{Directory: resumed, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, RunOptions{Directory: continuous, Command: command, Config: &config, Experiment: &x, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	_, a, err := LoadCheckpoint(resumed)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := LoadCheckpoint(continuous)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("PPO resume diverged", err)
	}
	path, err := ExportCandidate(resumed, filepath.Join(root, "child.json"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := battlepolicy.LoadArtifact(path)
	if err != nil || child.Schema != 5 || child.Recorded != nil || child.Parent != "" || !reflect.DeepEqual(child.RecordedSelectionArtifacts, []string{modelID}) {
		t.Fatal("scratch child lost indirect source or invented a gradient parent", err)
	}
	reportPath := filepath.Join(root, "validation.json")
	report, err := EvaluateRecorded(ctx, engine, child, []Opponent{{Name: "basic", Rule: "basic"}}, ec, &x, "validation", reportPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyEvaluation(ctx, reportPath)
	if err != nil || !reflect.DeepEqual(report, verified) || len(report.UnverifiedSourceArtifacts) != 1 {
		t.Fatal("source evidence cannot verify", err)
	}
	next, err := NewExperimentFromSources(ctx, engine.Metadata(), ec, [3]int{2, 1, 1}, nil, &child)
	if err != nil || next.validateInitialModel(&child) != nil || !reflect.DeepEqual(next.RecordedSelectionArtifacts, pool.RecordedSelectionArtifacts) {
		t.Fatal("descendant lost source", err)
	}
	id, _ := Digest(a)
	t.Logf("actual recorded opponent -> search/pool -> scratch PPO/resume -> schema5 -> verified evaluation; learning=%s; evidence=%s; no strength claim", id, root)
}
