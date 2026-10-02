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

func copyEvaluationEvidence(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0600)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationResumeRequiresExplicitExistingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, err := EvaluateRecorded(context.Background(), nil, battlepolicy.Artifact{}, nil, EvaluationConfig{}, nil, "", path, nil, EvaluationRecordingOptions{Resume: true})
	if err == nil || !strings.Contains(err.Error(), "existing frozen") {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "partial.json.data")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spec.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateRecorded(context.Background(), nil, battlepolicy.Artifact{}, nil, EvaluationConfig{}, nil, "", strings.TrimSuffix(root, ".data"), nil)
	if err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "spec.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "shards"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateRecorded(context.Background(), nil, battlepolicy.Artifact{}, nil, EvaluationConfig{}, nil, "", strings.TrimSuffix(root, ".data"), nil)
	if err == nil || !strings.Contains(err.Error(), "without frozen specification") {
		t.Fatal("orphan evidence silently reused", err)
	}
	lock, err := os.OpenFile(filepath.Join(root, ".evaluation.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockTraining(lock); err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateRecorded(context.Background(), nil, battlepolicy.Artifact{}, nil, EvaluationConfig{}, nil, "", strings.TrimSuffix(root, ".data"), nil, EvaluationRecordingOptions{Resume: true})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal("concurrent evaluation accepted", err)
	}
}

func TestNativeEvaluationResumeWindows(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit isolated native environment required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	c := DefaultRunConfig()
	c.Warmup = nil
	c.Network = testModel(t).Config
	c.BatchMatches, c.MaxTurns, c.PPO.Epochs = 2, 6, 1
	training := filepath.Join(root, "training")
	if err := Run(ctx, RunOptions{Directory: training, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	modelPath, err := ExportCandidate(training, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	}()
	ec := DefaultEvaluationConfig()
	ec.MatchesPerOpponent, ec.MaxTurns = 8, 6
	opponents := []Opponent{{Name: "basic", Rule: "basic"}, {Name: "self", Model: &a}}
	path := filepath.Join(root, "evaluation.json")
	stop := errors.New("test stop after committed game")
	newGames := 0
	partial, err := EvaluateRecorded(ctx, engine, a, opponents, ec, nil, "", path, func(EvaluationGame) error {
		newGames++
		if newGames == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || len(partial.Games) != 3 {
		t.Fatal("wrong first interruption", err, len(partial.Games))
	}
	schedule, err := prepareEvaluation(ctx, engine.Metadata(), a, opponents, ec, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"journal", "legacy", "orphan-commit-window", "missing-prefix", "duplicate", "bad-commit", "missing-manifest", "corrupt-shard", "wrong-action", "ambiguous-legacy", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "evidence")
			copyEvaluationEvidence(t, path+".data", dir)
			r := evaluationRecorder{root: dir, id: partial.Evidence, options: EvaluationRecordingOptions{Resume: true}}
			s := schedule
			callCtx := ctx
			removeGame := func(index int) {
				id := partial.Games[index].Shard
				for _, p := range []string{filepath.Join(dir, "shards", id+".manifest.json"), filepath.Join(dir, "shards", id+".jsonl.gz"), filepath.Join(dir, "commits", fmt.Sprintf("%09d.json", index))} {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
				}
			}
			switch name {
			case "legacy", "ambiguous-legacy":
				if err := os.RemoveAll(filepath.Join(dir, "commits")); err != nil {
					t.Fatal(err)
				}
				if name == "ambiguous-legacy" {
					s.Suite = append([]battleenv.Scenario(nil), s.Suite...)
					s.Suite[2] = s.Suite[0]
				}
			case "orphan-commit-window":
				if err := os.Remove(filepath.Join(dir, "commits", "000000002.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-prefix":
				removeGame(0)
			case "duplicate":
				// A second byte-identical shard referenced at another ordinal
				// must not be interpreted as another executed match.
				if err := r.commit(3, partial.Games[0].Shard); err != nil {
					t.Fatal(err)
				}
			case "bad-commit":
				b, _ := json.Marshal(evaluationCommit{"commander-evaluation-commit-v1", strings.Repeat("f", 64), 0, partial.Games[0].Shard})
				if err := os.WriteFile(filepath.Join(dir, "commits", "000000000.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-manifest":
				if err := os.Remove(filepath.Join(dir, "shards", partial.Games[0].Shard+".manifest.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt-shard":
				if err := os.WriteFile(filepath.Join(dir, "shards", partial.Games[0].Shard+".jsonl.gz"), []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-action":
				pair, _, err := LoadShard(filepath.Join(dir, "shards"), partial.Games[0].Shard)
				if err != nil {
					t.Fatal(err)
				}
				step := &pair[0].Steps[0]
				changed := false
				for i, slot := range step.Frame.Slots {
					if len(slot.Candidates) > 1 {
						step.Choices[i] = (step.Choices[i] + 1) % len(slot.Candidates)
						changed = true
						break
					}
				}
				if !changed {
					t.Fatal("fixture lacks alternative legal action")
				}
				removeGame(0)
				m, err := SaveShard(filepath.Join(dir, "shards"), pair)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.commit(0, m.Digest); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				c, stop := context.WithCancel(ctx)
				stop()
				callCtx = c
			}
			restored := false
			r.options.Restored = func(n, total int) error {
				restored = true
				if n != 3 || total != 16 {
					t.Fatal(n, total)
				}
				return nil
			}
			got, err := r.restore(callCtx, a, opponents, s)
			good := name == "journal" || name == "legacy" || name == "orphan-commit-window"
			if !good {
				if err == nil || restored {
					t.Fatal("unsafe resume passed", name, err)
				}
				if name == "wrong-action" && !strings.Contains(err.Error(), "action differs") {
					t.Fatal("not rejected by policy replay", err)
				}
				if name == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
				return
			}
			if err != nil || !restored || !reflect.DeepEqual(got, partial.Games) {
				t.Fatal("prefix changed", err)
			}
			commits, err := os.ReadDir(filepath.Join(dir, "commits"))
			if err != nil || len(commits) != 3 {
				t.Fatal("adoption not committed", err)
			}
		})
	}
	// Freeze mismatch must fail before progress or any new native match.
	changed := ec
	changed.Seed++
	if _, err := EvaluateRecorded(ctx, engine, a, opponents, changed, nil, "", path, func(EvaluationGame) error { t.Fatal("collected with changed specification"); return nil }, EvaluationRecordingOptions{Resume: true}); err == nil {
		t.Fatal("changed spec accepted")
	}
	// A repeated interruption keeps the original first three shards.
	second, err := EvaluateRecorded(ctx, engine, a, opponents, ec, nil, "", path, func(EvaluationGame) error { return stop }, EvaluationRecordingOptions{Resume: true})
	if !errors.Is(err, stop) || len(second.Games) != 4 || !reflect.DeepEqual(second.Games[:3], partial.Games) {
		t.Fatal("second interruption lost prefix", err)
	}
	remaining := 0
	final, err := EvaluateRecorded(ctx, engine, a, opponents, ec, nil, "", path, func(EvaluationGame) error { remaining++; return nil }, EvaluationRecordingOptions{Resume: true})
	if err != nil || remaining != 12 || len(final.Games) != 16 || !reflect.DeepEqual(final.Games[:4], second.Games) {
		t.Fatal("resume repeated completed matches", err, remaining)
	}
	verified, err := VerifyEvaluation(ctx, path)
	if err != nil || !reflect.DeepEqual(final, verified) {
		t.Fatal("resumed report failed independent verification", err)
	}
	// Report publication may fail after the last commit. Restoring only the
	// completed evidence must publish it with zero additional games.
	completePath := filepath.Join(root, "unpublished.json")
	copyEvaluationEvidence(t, path+".data", completePath+".data")
	got, err := EvaluateRecorded(ctx, engine, a, opponents, ec, nil, "", completePath, func(EvaluationGame) error { t.Fatal("complete evidence re-executed"); return nil }, EvaluationRecordingOptions{Resume: true})
	if err != nil || !reflect.DeepEqual(got, final) {
		t.Fatal("complete evidence failed to publish", err)
	}
	// The committed scope remains exactly16 shards despite all retries.
	shards, err := filepath.Glob(filepath.Join(path+".data", "shards", "*.manifest.json"))
	if err != nil || len(shards) != 16 {
		t.Fatal("extra native data generated", err, len(shards))
	}
	t.Log("2 training +16 evaluation games; repeated interruption, legacy/orphan recovery, rejection cases and full raw-policy verification; functional evidence only")
}
