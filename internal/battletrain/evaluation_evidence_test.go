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

func TestNativeEvaluationEvidence(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	c := DefaultRunConfig()
	c.Warmup = nil
	c.Network = testModel(t).Config
	c.BatchMatches, c.MaxTurns, c.PPO.Epochs = 2, 6, 1
	if e := Run(ctx, RunOptions{Directory: filepath.Join(root, "training"), Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	modelPath, e := ExportCandidate(filepath.Join(root, "training"), "")
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := battlepolicy.LoadArtifact(modelPath)
	if e != nil {
		t.Fatal(e)
	}
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	ec := DefaultEvaluationConfig()
	ec.MatchesPerOpponent, ec.MaxTurns = 8, 6
	opponents := []Opponent{{Name: "sustain", Rule: "sustain"}, {Name: "frozen-model", Model: &candidate}}
	path := filepath.Join(root, "evaluation.json")
	stop := errors.New("intentional interruption")
	_, e = EvaluateRecorded(ctx, engine, candidate, opponents, ec, nil, "", path, func(EvaluationGame) error { return stop })
	if !errors.Is(e, stop) {
		t.Fatal("interruption lost", e)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("partial report published", e)
	}
	if _, e := EvaluateRecorded(ctx, engine, candidate, []Opponent{{Name: "basic", Rule: "basic"}}, ec, nil, "", path, nil, EvaluationRecordingOptions{Resume: true}); e == nil {
		t.Fatal("frozen opponent set changed on retry")
	}
	if _, err := EvaluateRecorded(ctx, engine, candidate, opponents, ec, nil, "", path, nil); err == nil {
		t.Fatal("implicit restart accepted")
	}
	var first evaluationCommit
	if err := readObject(filepath.Join(path+".data", "commits", "000000000.json"), &first, 4096); err != nil {
		t.Fatal(err)
	}
	newlyCollected, restored := 0, 0
	report, e := EvaluateRecorded(ctx, engine, candidate, opponents, ec, nil, "", path, func(EvaluationGame) error { newlyCollected++; return nil }, EvaluationRecordingOptions{Resume: true, Restored: func(n, total int) error {
		restored = n
		if total != 16 {
			t.Fatal("wrong total", total)
		}
		return nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = EvaluateRecorded(ctx, engine, candidate, opponents, ec, nil, "", path, nil); e == nil {
		t.Fatal("existing report overwritten")
	}
	verified, e := VerifyEvaluation(ctx, path)
	if e != nil || !reflect.DeepEqual(report, verified) {
		t.Fatal("recording did not verify", e)
	}
	if restored != 1 || newlyCollected != 15 || report.Games[0].Shard != first.Shard {
		t.Fatal("completed game re-executed", restored, newlyCollected)
	}
	if len(report.Games) != 16 || report.Evidence == "" {
		t.Fatal("incomplete evidence")
	}
	// Evaluate the unchanged parent on a new child's frozen validation split.
	// Verify through the same persisted evidence path as ordinary candidates.
	x, e := NewExperimentFromSources(ctx, candidate.Environment, ec, [3]int{2, 1, 1}, nil, &candidate)
	if e != nil {
		t.Fatal(e)
	}
	parentPath := filepath.Join(root, "parent-validation.json")
	parentReport, e := EvaluateRecorded(ctx, engine, candidate, opponents, ec, &x, "validation", parentPath, nil)
	if e != nil {
		t.Fatal("native parent baseline failed", e)
	}
	parentVerified, e := VerifyEvaluation(ctx, parentPath)
	if e != nil || !reflect.DeepEqual(parentReport, parentVerified) || parentReport.CandidateArtifact != x.InitialModel || len(parentReport.Games) != 16 {
		t.Fatal("parent evidence failed to retain artifact and frozen suite", e)
	}
	// The output remains independently inspectable after original model files
	// disappear; only the copies under this temporary test tree are removed.
	if e := os.Remove(modelPath); e != nil {
		t.Fatal(e)
	}
	if _, e := VerifyEvaluation(ctx, path); e != nil {
		t.Fatal("verification depends on source model", e)
	}
	write := func(v any) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	// A internally consistent forged summary must still fail against raw data.
	forged := report
	forged.Games = append([]EvaluationGame(nil), report.Games...)
	if forged.Games[0].Turns > 1 {
		forged.Games[0].Turns--
	} else {
		forged.Games[0].Turns++
	}
	forged.Comparisons = Summarize(forged.Games, forged.Config.Seed)
	write(forged)
	if _, e := VerifyEvaluation(ctx, path); e == nil || !strings.Contains(e.Error(), "raw trajectories") {
		t.Fatal("forged statistics accepted", e)
	}
	write(report)
	shard := filepath.Join(path+".data", "shards", report.Games[0].Shard+".jsonl.gz")
	b, e := os.ReadFile(shard)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(shard, append(b, byte(0)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := VerifyEvaluation(ctx, path); e == nil {
		t.Fatal("corrupt raw evidence accepted")
	}
	if e = os.WriteFile(shard, b, 0600); e != nil {
		t.Fatal(e)
	}
	// Replace valid actions, then rehash the entire shard and report. Observation
	// validity alone is not proof that the frozen model chose these actions.
	tr, _, e := LoadShard(filepath.Join(path+".data", "shards"), report.Games[0].Shard)
	if e != nil {
		t.Fatal(e)
	}
	changed := false
	for si := range tr[0].Steps {
		step := &tr[0].Steps[si]
		for ai, actor := range step.Frame.Slots {
			for ci, action := range actor.Candidates {
				if action.Supported && ci != step.Choices[ai] {
					step.Choices[ai] = ci
					changed = true
					break
				}
			}
			if changed {
				break
			}
		}
		if changed {
			break
		}
	}
	if !changed {
		t.Fatal("fixture has no alternative legal action")
	}
	manifest, e := SaveShard(filepath.Join(path+".data", "shards"), tr)
	if e != nil {
		t.Fatal(e)
	}
	forged = report
	forged.Games = append([]EvaluationGame(nil), report.Games...)
	forged.Games[0].Shard = manifest.Digest
	write(forged)
	if _, e := VerifyEvaluation(ctx, path); e == nil || !strings.Contains(e.Error(), "frozen policy") {
		t.Fatal("substituted behavior accepted", e)
	}
	write(report)
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, e := VerifyEvaluation(cancelled, path); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel ignored", e)
	}
	// Older summary-only files are still structurally valid, but cannot grant
	// themselves evidence coverage by copying a model ID or aggregate score.
	legacy := report
	legacy.Evidence = ""
	legacy.Games = append([]EvaluationGame(nil), report.Games...)
	for i := range legacy.Games {
		legacy.Games[i].Shard = ""
	}
	if e := ValidateEvaluation(legacy); e != nil {
		t.Fatal(e)
	}
	write(legacy)
	if _, e := VerifyEvaluation(ctx, path); e == nil || !strings.Contains(e.Error(), "no complete evaluation evidence") {
		t.Fatal("legacy treated as recorded", e)
	}
	write(report)
	if _, e := VerifyEvaluation(ctx, path); e != nil {
		t.Fatal(e)
	}
	t.Logf("verified %d paired games, frozen rule/model opponents, interrupted retry and evidence tamper rejection", len(report.Games))
}
