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
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Journal transition tests deliberately substitute only the raw-evidence
// reader. These synthetic all-win reports test promotion/rollback persistence;
// they are not native strength evidence. The public API always uses the real
// verifier, tested separately with actual failed native challenges below.
func TestChampionPromotionAndRollbackJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta := experimentFixture(t).Environment
	c := DefaultEvaluationConfig()
	x, e := NewExperiment(ctx, meta, c, [3]int{2, 1, 32})
	if e != nil {
		t.Fatal(e)
	}
	gate := DefaultPromotionGate()
	gate.MinGroups = 20
	if e := InitChampionRegistry(root, x, gate); e != nil {
		t.Fatal(e)
	}
	reports := map[string]EvaluationReport{}
	verify := func(_ context.Context, path string) (EvaluationReport, error) {
		r, ok := reports[path]
		if !ok {
			return r, fmt.Errorf("missing synthetic test report")
		}
		return r, nil
	}
	var promoted []string
	var parent *battlepolicy.Artifact
	for generation := 0; generation < 3; generation++ {
		if parent != nil {
			c.Seed++
			x, e = NewExperimentFromSources(ctx, meta, c, [3]int{2, 1, 32}, nil, parent)
			if e != nil {
				t.Fatal(e)
			}
		}
		a := experimentCandidate(t, x)
		a.Parent, a.SelectionGroups = x.InitialModel, x.SelectionGroups
		a.TrainingGroups = sortedUnion(a.TrainingGroups, x.InitialTrainingGroups)
		id, e := championPut(root, "models", a)
		if e != nil {
			t.Fatal(e)
		}
		s, e := loadChampionRegistry(ctx, root, verify)
		if e != nil {
			t.Fatal(e)
		}
		rid, _ := Digest(s.Registry)
		attempt := ChampionAttempt{Schema: "native-champion-attempt-v1", Registry: rid, Previous: s.Head, Champion: s.Champion, Candidate: id, Experiment: x, Number: s.Attempts + 1}
		if e := validateChampionAttempt(ctx, root, s, attempt, s.Head); e != nil {
			t.Fatal(e)
		}
		attemptID, e := championPut(root, "attempts", attempt)
		if e != nil {
			t.Fatal(e)
		}
		opponents, e := championOpponents(root, s.Champion)
		if e != nil {
			t.Fatal(e)
		}
		schedule, e := prepareEvaluation(ctx, meta, a, opponents, experimentEvaluationConfig(x, "test"), &x, "test")
		if e != nil {
			t.Fatal(e)
		}
		r := schedule.Report
		for oi, o := range opponents {
			for i, scenario := range schedule.Suite {
				winner := i % 2
				if generation == 2 {
					winner = 1 - winner
				}
				r.Games = append(r.Games, EvaluationGame{Opponent: o.Name, OpponentPolicy: schedule.Versions[oi], Group: ScenarioGroup(scenario), Scenario: scenario, CandidateSide: i % 2, Winner: winner, Terminated: true, Turns: 1})
			}
		}
		r.Comparisons = Summarize(r.Games, r.Config.Seed)
		if e := ValidateEvaluation(r); e != nil {
			t.Fatal(e)
		}
		assessment, e := assessPromotion(r, gate, attempt.Number, s.Champion != "")
		if e != nil || assessment.Passed != (generation < 2) {
			t.Fatal("unexpected synthetic gate result", e)
		}
		// Freeze the test reader's value as a real immutable file would. The
		// next generation reassigns x, which the prepared report points at.
		encoded, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		var frozen EvaluationReport
		if e := json.Unmarshal(encoded, &frozen); e != nil {
			t.Fatal(e)
		}
		reports[championReportPath(root, attemptID)] = frozen
		reportID, _ := Digest(r)
		event := ChampionEvent{Schema: "native-champion-event-v1", Registry: rid, Previous: s.Head, Kind: "challenge", Attempt: attemptID, Report: reportID, Assessment: &assessment, Champion: s.Champion}
		if assessment.Passed {
			event.Champion = id
		}
		// An immutable orphan event from a crash before pointer replacement is
		// ignored. Only the synced atomic pointer commits a change.
		if _, e := championPut(root, "events", event); e != nil {
			t.Fatal(e)
		}
		before, e := loadChampionRegistry(ctx, root, verify)
		if e != nil || before.Head != s.Head || before.Champion != s.Champion {
			t.Fatal("orphan event became live", e)
		}
		eventID, e := commitChampionEvent(root, event)
		if e != nil {
			t.Fatal(e)
		}
		after, e := loadChampionRegistry(ctx, root, verify)
		if e != nil || after.Champion != event.Champion || after.Attempts != generation+1 {
			t.Fatal("journal transition failed", e)
		}
		if assessment.Passed {
			promoted = append(promoted, eventID)
		}
		parent = &a
	}
	before, e := loadChampionRegistry(ctx, root, verify)
	if e != nil {
		t.Fatal(e)
	}
	rollback, e := changeChampion(ctx, root, promoted[0], "synthetic execution regression", false, verify)
	if e != nil || rollback.Champion == before.Champion {
		t.Fatal("eligible previous model was not restored", e)
	}
	after, e := loadChampionRegistry(ctx, root, verify)
	if e != nil || after.Champion != rollback.Champion || after.Attempts != before.Attempts || len(after.used) != len(before.used) {
		t.Fatal("rollback reset exposure or spending", e)
	}
	if _, e := changeChampion(ctx, root, before.Head, "failed candidate", false, verify); e == nil {
		t.Fatal("rollback selected a failed challenge")
	}
	if _, e := changeChampion(ctx, root, promoted[0], "already selected", false, verify); e == nil {
		t.Fatal("no-op rollback accepted")
	}
	if _, e := LoadChampionRegistry(ctx, root); e == nil {
		t.Fatal("public registry accepted synthetic reports without raw evidence")
	}
}

func TestNativeChampionFailureResumeAndExposure(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	ec := DefaultEvaluationConfig()
	ec.MaxTurns = 1 // Forces unresolved games; a successful gate would be a bug.
	x, e := NewExperiment(ctx, engine.Metadata(), ec, [3]int{2, 1, 20})
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultRunConfig()
	c.Warmup, c.Network, c.MaxTurns, c.BatchMatches = nil, testModel(t).Config, 1, 2
	c.Experiment, _ = Digest(x)
	c.PPO.Epochs = 1
	training, root := t.TempDir(), t.TempDir()
	if e := Run(ctx, RunOptions{Directory: training, Command: command, Config: &c, Experiment: &x, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	modelPath, e := ExportCandidate(training, "")
	if e != nil {
		t.Fatal(e)
	}
	a, e := battlepolicy.LoadArtifact(modelPath)
	if e != nil {
		t.Fatal(e)
	}
	gate := DefaultPromotionGate()
	gate.MinGroups = 20
	if e := InitChampionRegistry(root, x, gate); e != nil {
		t.Fatal(e)
	}
	changed := gate
	changed.RuleScore = .55
	if e := InitChampionRegistry(root, x, changed); e == nil {
		t.Fatal("gate was changed after registry creation")
	}
	stop := errors.New("interrupt after real native game")
	selection := filepath.Join(root, "shared-selection.json")
	_, e = ChallengeChampion(ctx, root, engine, a, x, selection, func(EvaluationGame) error { return stop })
	if !errors.Is(e, stop) {
		t.Fatal("interruption not propagated", e)
	}
	s, e := LoadChampionRegistry(ctx, root)
	if e != nil || s.Pending == "" || s.Attempts != 0 || s.Champion != "" {
		t.Fatal("pending state lost", s, e)
	}
	pending := s.Pending
	lock, e := championLock(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := ChallengeChampion(ctx, root, engine, a, x, selection, nil); e == nil {
		t.Fatal("concurrent challenge accepted")
	}
	lock.Close()
	if _, e := ChangeChampion(ctx, root, strings.Repeat("0", 64), "rollback", false); e == nil {
		t.Fatal("rollback erased pending exposure")
	}
	event, e := ChallengeChampion(ctx, root, engine, a, x, selection, nil)
	if e != nil {
		t.Fatal(e)
	}
	if event.Attempt != pending || event.Assessment == nil || event.Assessment.Passed || event.Champion != "" {
		t.Fatal("incomplete matches promoted", event)
	}
	s, e = LoadChampionRegistry(ctx, root)
	if e != nil || s.Attempts != 1 || s.Pending != "" || s.Champion != "" || len(s.Events) != 1 || len(s.EventIDs) != 1 {
		t.Fatal("failed evaluation not committed", e)
	}
	if !reflect.DeepEqual(s.Events[0], event) {
		t.Fatal("event changed after reload")
	}
	if _, e := ChallengeChampion(ctx, root, engine, a, x, selection, nil); e == nil || !strings.Contains(e.Error(), "already exposed") {
		t.Fatal("test families reused after failure", e)
	}
	if _, e := ChangeChampion(ctx, root, s.Head, "try failed event", false); e == nil {
		t.Fatal("failed candidate eligible for rollback")
	}
	// Even a rehashed journal event cannot turn a failed raw report into a win.
	forged := event
	decision := *event.Assessment
	decision.Passed = true
	forged.Assessment = &decision
	forged.Champion, _ = Digest(a)
	if _, e := commitChampionEvent(root, forged); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadChampionRegistry(ctx, root); e == nil || !strings.Contains(e.Error(), "assessment differs") {
		t.Fatal("forged promotion passed", e)
	}
	if _, e := commitChampionEvent(root, event); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadChampionRegistry(ctx, root); e != nil {
		t.Fatal(e)
	}
	// A separate registry fixture exercises explicit abandonment. This is
	// lifecycle verification, not new independent strength evidence.
	abandoned := t.TempDir()
	if e := InitChampionRegistry(abandoned, x, gate); e != nil {
		t.Fatal(e)
	}
	if _, e := ChallengeChampion(ctx, abandoned, engine, a, x, "", func(EvaluationGame) error { return stop }); !errors.Is(e, stop) {
		t.Fatal(e)
	}
	if _, e := ChangeChampion(ctx, abandoned, "", "fixture interruption", true); e != nil {
		t.Fatal(e)
	}
	ab, e := LoadChampionRegistry(ctx, abandoned)
	if e != nil || ab.Attempts != 1 || ab.Pending != "" || ab.Champion != "" || ab.Events[0].Kind != "abandon" {
		t.Fatal("abandon lost exposure", e)
	}
	if _, e := ChallengeChampion(ctx, abandoned, engine, a, x, "", nil); e == nil || !strings.Contains(e.Error(), "already exposed") {
		t.Fatal("abandon allowed test reuse", e)
	}
	t.Logf("400 real native final-test games failed safely; verified unchanged champion, resume, concurrency, rehashed-forgery rejection and abandoned-test exposure")
}
