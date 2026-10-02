package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func mixedChampionFixture(t *testing.T, meta battleenv.Metadata, parent *battlepolicy.Artifact, seed int64, turns int) MixedExperiment {
	t.Helper()
	var parts []MixedExperimentPart
	for _, mode := range []int{1, 5} {
		c := DefaultEvaluationConfig()
		c.Mode = mode
		c.Seed = seed + int64(mode)
		c.MaxTurns = turns
		c.PetPoints = 0
		x, err := NewExperimentFromSources(context.Background(), meta, c, [3]int{2, 1, 32}, nil, parent)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
	}
	x, err := NewMixedExperiment(testModeMixture(), parts, parent)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestMixedChampionJointBudgetAndJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta := experimentFixture(t).Environment
	x := mixedChampionFixture(t, meta, nil, 7200, 3)
	gate := DefaultPromotionGate()
	gate.MinGroups = 20
	if err := InitMixedChampionRegistry(root, x, gate); err != nil {
		t.Fatal(err)
	}
	var parent *battlepolicy.Artifact
	reports := map[string]EvaluationReport{}
	verify := func(_ context.Context, path string) (EvaluationReport, error) {
		r, ok := reports[path]
		if !ok {
			return r, fmt.Errorf("missing synthetic report")
		}
		return r, nil
	}
	var passed []string
	for generation := 0; generation < 3; generation++ {
		if parent != nil {
			x = mixedChampionFixture(t, meta, parent, 7200+int64(generation*100), 3)
		}
		candidate := mixedCandidateFixture(t, x)
		cid, err := championPut(root, "models", candidate)
		if err != nil {
			t.Fatal(err)
		}
		s, err := loadChampionRegistry(ctx, root, verify)
		if err != nil {
			t.Fatal(err)
		}
		rid, _ := Digest(s.Registry)
		a := ChampionAttempt{Schema: "native-mixed-champion-attempt-v1", Mixed: &x, Registry: rid, Previous: s.Head, Champion: s.Champion, Candidate: cid, Number: s.Attempts + 1}
		if err := validateChampionAttempt(ctx, root, s, a, s.Head); err != nil {
			t.Fatal(err)
		}
		aid, err := championPut(root, "attempts", a)
		if err != nil {
			t.Fatal(err)
		}
		opponents, err := championOpponents(root, s.Champion)
		if err != nil {
			t.Fatal(err)
		}
		var all []EvaluationReport
		for _, p := range x.Parts {
			schedule, err := prepareEvaluationWithMixed(ctx, meta, candidate, opponents, EvaluationConfig{}, &p.Experiment, &x, "test")
			if err != nil {
				t.Fatal(err)
			}
			r := schedule.Report
			for oi, o := range opponents {
				for i, scene := range schedule.Suite {
					winner := i % 2
					if generation == 2 && p.Experiment.Mode == 5 {
						winner = 1 - winner
					}
					r.Games = append(r.Games, EvaluationGame{Opponent: o.Name, OpponentPolicy: schedule.Versions[oi], Scenario: scene, Group: ScenarioGroup(scene), CandidateSide: i % 2, Winner: winner, Terminated: true, Turns: 1})
				}
			}
			r.Comparisons = Summarize(r.Games, r.Config.Seed)
			if err := ValidateEvaluation(r); err != nil {
				t.Fatal(err)
			}
			var frozen EvaluationReport
			if err := json.Unmarshal(mustJSON(t, r), &frozen); err != nil {
				t.Fatal(err)
			}
			reports[championModeReportPath(root, aid, a, p.Experiment.Mode)] = frozen
			all = append(all, frozen)
		}
		assessment, err := assessChampionReports(all, a, gate)
		if err != nil || assessment.Passed != (generation < 2) {
			t.Fatal("pooled away failed mode", err, assessment.Passed)
		}
		if generation == 0 {
			if len(assessment.Bounds) != 10 || assessment.Alpha != .025 {
				t.Fatal("wrong joint budget")
			}
			want := 1 - math.Sqrt(math.Log(10/.025)/(2*32))
			for _, b := range assessment.Bounds {
				if b.Mode != 1 && b.Mode != 5 || b.Lower == nil || math.Abs(*b.Lower-want) > 1e-12 {
					t.Fatal("modes not covered by joint bound", b)
				}
			}
			if _, err := assessPromotion(all[0], gate, 1, false); err == nil {
				t.Fatal("one mode bypassed joint gate")
			}
			if _, err := assessChampionReports(all[:1], a, gate); err == nil {
				t.Fatal("omitted mode accepted")
			}
			if _, err := assessChampionReports([]EvaluationReport{all[1], all[0]}, a, gate); err == nil {
				t.Fatal("wrong mode order accepted")
			}
			cutoffs := append([]EvaluationReport(nil), all...)
			cutoffs[1].Games = append([]EvaluationGame(nil), all[1].Games...)
			cutoffs[1].Games[0].Truncated = true
			cutoffs[1].Games[0].Terminated = false
			cutoffs[1].Games[0].Winner = -1
			bad, err := assessChampionReports(cutoffs, a, gate)
			if err != nil || bad.Passed {
				t.Fatal("cutoff promoted", err)
			}
		}
		ridAll, _ := championReportsDigest(a, all)
		event := ChampionEvent{Schema: s.Registry.eventSchema(), Registry: rid, Previous: s.Head, Kind: "challenge", Attempt: aid, Report: ridAll, Assessment: &assessment, Champion: s.Champion}
		if assessment.Passed {
			event.Champion = cid
		}
		if _, err := championPut(root, "events", event); err != nil {
			t.Fatal(err)
		}
		before, err := loadChampionRegistry(ctx, root, verify)
		if err != nil || before.Head != s.Head {
			t.Fatal("orphan mixed event became current", err)
		}
		eid, err := commitChampionEvent(root, event)
		if err != nil {
			t.Fatal(err)
		}
		after, err := loadChampionRegistry(ctx, root, verify)
		if err != nil || after.Champion != event.Champion || after.Attempts != generation+1 || len(after.used) != (generation+1)*64 {
			t.Fatal("mixed transition lost history", err)
		}
		if assessment.Passed {
			passed = append(passed, eid)
		}
		parent = &candidate
	}
	before, err := loadChampionRegistry(ctx, root, verify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changeChampion(ctx, root, passed[0], "synthetic regression rollback", false, verify); err != nil {
		t.Fatal(err)
	}
	after, err := loadChampionRegistry(ctx, root, verify)
	if err != nil || after.Champion == before.Champion || after.Attempts != before.Attempts || !reflect.DeepEqual(after.used, before.used) {
		t.Fatal("mixed rollback reset history", err)
	}
	if _, err := changeChampion(ctx, root, before.Head, "failed candidate", false, verify); err == nil {
		t.Fatal("failed mixed candidate selected")
	}
	// A pending attempt is abandoned across all modes and keeps its exposures.
	x = mixedChampionFixture(t, meta, parent, 7900, 3)
	candidate := mixedCandidateFixture(t, x)
	cid, err := championPut(root, "models", candidate)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := Digest(after.Registry)
	a := ChampionAttempt{Schema: "native-mixed-champion-attempt-v1", Mixed: &x, Registry: rid, Previous: after.Head, Champion: after.Champion, Candidate: cid, Number: after.Attempts + 1}
	if err := validateChampionAttempt(ctx, root, after, a, after.Head); err != nil {
		t.Fatal(err)
	}
	aid, err := championPut(root, "attempts", a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "reservations"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeObject(championReservation(root, after.Head), struct {
		Attempt string `json:"attempt"`
	}{aid}); err != nil {
		t.Fatal(err)
	}
	if _, err := changeChampion(ctx, root, "", "synthetic abandon", true, verify); err != nil {
		t.Fatal(err)
	}
	final, err := loadChampionRegistry(ctx, root, verify)
	if err != nil || final.Attempts != 4 || len(final.used) != 256 || final.Champion != after.Champion {
		t.Fatal("abandon reset joint budget/exposures", err)
	}
	a.Number, a.Previous = 5, final.Head
	if err := validateChampionAttempt(ctx, root, final, a, final.Head); err == nil {
		t.Fatal("abandoned families reused")
	}
	if _, err := LoadChampionRegistry(ctx, root); err == nil {
		t.Fatal("synthetic reports passed real evidence verifier")
	}
}

func TestNativeMixedChampionPartialModeResume(t *testing.T) {
	raw, source, root := os.Getenv("STONEAGE_MIXED_CHAMPION_COMMAND"), os.Getenv("STONEAGE_MIXED_CHAMPION_PARENT"), os.Getenv("STONEAGE_MIXED_CHAMPION_ROOT")
	if raw == "" || source == "" || root == "" {
		t.Skip("explicit bounded mixed champion fixture required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("preserve previous fixture", err)
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	parent, err := battlepolicy.LoadArtifact(source)
	if err != nil {
		t.Fatal(err)
	}
	x := mixedChampionFixture(t, parent.Environment, &parent, 8500, 1)
	// Freeze the minimum 20 families per mode before starting any games.
	for i, p := range x.Parts {
		c := experimentEvaluationConfig(p.Experiment, "test")
		e, err := NewExperimentFromSources(ctx, parent.Environment, c, [3]int{2, 1, 20}, nil, &parent)
		if err != nil {
			t.Fatal(err)
		}
		x.Parts[i].Experiment = e
		x.Parts[i].Digest, _ = Digest(e)
	}
	if err := x.ValidateParent(&parent); err != nil {
		t.Fatal(err)
	}
	id, _ := Digest(x)
	cfg := mixedRunConfigFixture(t, x)
	cfg.Experiment = id
	cfg.Network = parent.Network.Config
	cfg.OpponentMix = OpponentMix{Rules: 100}
	cfg.RuleOpponents = []string{"basic"}
	train := filepath.Join(root, "training")
	if err := RunMixed(ctx, MixedRunOptions{Directory: train, Command: command, Config: &cfg, Experiment: &x, InitialModel: &parent, Batches: 1}); err != nil {
		t.Fatal(err)
	}
	model, err := ExportMixedCandidate(ctx, train, "", "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := battlepolicy.LoadArtifact(model)
	if err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "champions")
	gate := DefaultPromotionGate()
	gate.MinGroups = 20
	if err := InitMixedChampionRegistry(registry, x, gate); err != nil {
		t.Fatal(err)
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	stop := errors.New("stop after completed mode1 and first mode5 game")
	count := 0
	_, err = ChallengeMixedChampion(ctx, registry, engine, candidate, x, "", func(EvaluationGame) error {
		count++
		if count == 801 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal("partial mixed challenge did not stop", err)
	}
	s, err := LoadChampionRegistry(ctx, registry)
	if err != nil || s.Pending == "" || s.Champion != "" || len(s.Events) != 0 {
		t.Fatal("partial mode promoted or lost reservation", err)
	}
	first := filepath.Join(registry, "evaluations", s.Pending+"-mode-1.json")
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	count = 0
	event, err := ChallengeMixedChampion(ctx, registry, engine, candidate, x, "", func(EvaluationGame) error { count++; return nil })
	if err != nil || event.Assessment == nil || event.Assessment.Passed || count != 800 {
		t.Fatal("resume reran completed mode or promoted cutoffs", count, err)
	}
	after, err := os.ReadFile(first)
	if err != nil || string(before) != string(after) {
		t.Fatal("completed mode report changed", err)
	}
	s, err = LoadChampionRegistry(ctx, registry)
	if err != nil || s.Pending != "" || s.Champion != "" || s.Attempts != 1 || len(s.Events) != 1 || len(s.used) != 40 {
		t.Fatal("failed mixed challenge lost audit", err)
	}
	if _, err := ChallengeMixedChampion(ctx, registry, engine, candidate, x, "", nil); err == nil || !strings.Contains(err.Error(), "exposed") {
		t.Fatal("final families reused", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("16 training + 1601 challenge games; completed mode1 reused, both modes fail cutoff gate, exposures retained")
}
