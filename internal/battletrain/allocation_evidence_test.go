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

func TestAllocationEvidenceSpecificationBoundaries(t *testing.T) {
	v, a := allocationValidationFixture(t, 2)
	opponents := []Opponent{{Name: "basic", Rule: "basic"}, {Name: "sustain", Rule: "sustain"}}
	spec, schedules, err := prepareAllocationEvaluation(v, a, opponents)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Schema != allocationEvidenceSchema || !schedules[0].StrictPolicyStatistics || schedules[0].Report.Config.Pairing != "" || len(schedules[0].Suite) != 12 {
		t.Fatal("wrong evidence domain/schedule")
	}
	for _, bad := range [][]Opponent{nil, {{Name: "basic", Rule: "focus"}, opponents[1]}, {opponents[1], opponents[0]}} {
		if _, _, err = prepareAllocationEvaluation(v, a, bad); err == nil {
			t.Fatal("changed opponents accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = verifyAllocationSource(ctx, "unread", v); !errors.Is(err, context.Canceled) {
		t.Fatal("source preflight lost cancellation", err)
	}
	if _, err = RunAllocationValidation(ctx, nil, v, a, "unread", "", nil); err == nil {
		t.Fatal("missing engine accepted")
	}
}

// Real C battles test data integrity and recovery. Small networks and short
// evaluation cutoffs deliberately provide no claim of strategic strength.
func TestNativeAllocationEvidenceRecovery(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit isolated native environment required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mode := range []int{1, 5} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			base := t.TempDir()
			if keep := os.Getenv("STONEAGE_ALLOCATION_TEST_OUTPUT"); keep != "" {
				base, err = os.MkdirTemp(keep, fmt.Sprintf("mode%d-", mode))
				if err != nil {
					t.Fatal(err)
				}
			}
			c := DefaultRunConfig()
			c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns, c.BatchMatches = mode, 20, 0, 10, 3, 2
			c.Warmup = nil
			c.Network = testModel(t).Config
			c.PPO.Epochs = 1
			parentRoot := filepath.Join(base, "parent")
			if err = Run(ctx, RunOptions{Directory: parentRoot, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			path, err := ExportCandidate(parentRoot, "")
			if err != nil {
				t.Fatal(err)
			}
			parent, err := battlepolicy.LoadArtifact(path)
			if err != nil {
				t.Fatal(err)
			}
			searchConfig := DefaultBuildSearchConfig()
			searchConfig.Mode, searchConfig.Points, searchConfig.PetPoints, searchConfig.Level, searchConfig.MaxTurns = mode, 20, 0, 10, 300
			searchConfig.InitialCandidates, searchConfig.Generations, searchConfig.Proposals, searchConfig.NativeCandidates, searchConfig.Finalists, searchConfig.FitEpochs = 5, 1, 4, 1, 2, 1
			searchConfig.SearchGroups, searchConfig.ValidationGroups, searchConfig.TestGroups = 1, 1, 1
			sourceRoot := filepath.Join(base, "search")
			source, err := RunBuildSearch(ctx, BuildSearchOptions{Directory: sourceRoot, Engine: engine, Controller: Opponent{Name: "parent", Model: &parent}, Opponents: []Opponent{{Name: "basic", Rule: "basic"}}, Config: searchConfig})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := ExportBuildPool(sourceRoot, "")
			if err != nil {
				t.Fatal(err)
			}
			ec := DefaultEvaluationConfig()
			ec.Mode, ec.Points, ec.PetPoints, ec.Level, ec.MaxTurns = mode, 20, 0, 10, 3
			x, err := NewExperimentWithPoolMix(ctx, engine.Metadata(), ec, [3]int{2, 1, 1}, &pool, &parent, 1)
			if err != nil {
				t.Fatal(err)
			}
			v, err := NewAllocationValidation(ctx, x, source, parent, 991, 1)
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(base, "parent-evaluation.json")
			stop := errors.New("stop in second allocation")
			n := 0
			_, err = RunAllocationValidation(ctx, engine, v, parent, sourceRoot, output, func(_ string, _ EvaluationGame) error {
				n++
				if n == 5 {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) || n != 5 {
				t.Fatal("interruption lost", n, err)
			}
			if _, err = os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("partial report published", err)
			}
			// A bad second branch must not adopt the first branch's orphan commit.
			bad := filepath.Join(base, "bad-second.json")
			copyEvaluationEvidence(t, output+".data", bad+".data")
			missing := filepath.Join(bad+".data", "balanced", "commits", "000000003.json")
			if err = os.Remove(missing); err != nil {
				t.Fatal(err)
			}
			var commit evaluationCommit
			if err = readObject(filepath.Join(bad+".data", "selected", "commits", "000000000.json"), &commit, 4096); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(bad+".data", "selected", "shards", commit.Shard+".jsonl.gz"), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = RunAllocationValidation(ctx, engine, v, parent, sourceRoot, bad, func(string, EvaluationGame) error { calls++; return nil }, EvaluationRecordingOptions{Resume: true})
			if err == nil || calls != 0 {
				t.Fatal("corrupt second branch resumed", calls, err)
			}
			if _, err = os.Stat(missing); !os.IsNotExist(err) {
				t.Fatal("first branch adopted before all checks", err)
			}
			if _, err = RunAllocationValidation(ctx, engine, v, parent, sourceRoot, output, nil); err == nil {
				t.Fatal("implicit restart accepted")
			}
			changed := parent
			changed.TrainingReport = strings.Repeat("9", 64)
			if _, err = RunAllocationValidation(ctx, engine, v, changed, sourceRoot, output, nil, EvaluationRecordingOptions{Resume: true}); err == nil {
				t.Fatal("changed candidate accepted")
			}
			orphan := filepath.Join(output+".data", "balanced", "commits", "000000003.json")
			if err = os.Remove(orphan); err != nil {
				t.Fatal(err)
			}
			restored, newGames := 0, 0
			report, err := RunAllocationValidation(ctx, engine, v, parent, sourceRoot, output, func(string, EvaluationGame) error { newGames++; return nil }, EvaluationRecordingOptions{Resume: true, Restored: func(done, total int) error {
				restored = done
				if total != 8 {
					t.Fatal("wrong total", total)
				}
				return nil
			}})
			if err != nil || restored != 5 || newGames != 3 {
				t.Fatal("resume reexecuted committed games", restored, newGames, err)
			}
			if _, err = os.Stat(orphan); err != nil {
				t.Fatal("orphan not adopted", err)
			}
			verified, err := VerifyAllocationEvaluation(ctx, output, sourceRoot)
			if err != nil || !reflect.DeepEqual(report, verified) {
				t.Fatal("report cannot verify", err)
			}
			if report.Status != "incomplete-outcomes" {
				t.Fatal("short cutoff falsely complete", report.Status)
			}
			if _, err = VerifyEvaluation(ctx, output); err == nil {
				t.Fatal("supplementary report treated as ordinary evaluation")
			}
			// Verification checks/adopts no files, even with an unambiguous orphan.
			if err = os.Remove(orphan); err != nil {
				t.Fatal(err)
			}
			if _, err = VerifyAllocationEvaluation(ctx, output, sourceRoot); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatal("read-only verifier wrote adoption", err)
			}
			_, _, opponents, schedules, err := loadAllocationSpec(output + ".data")
			if err != nil {
				t.Fatal(err)
			}
			g := report.Results[0].Games[0]
			pair, _, err := LoadShard(filepath.Join(output+".data", "balanced", "shards"), g.Shard)
			if err != nil {
				t.Fatal(err)
			}
			pair[0].Steps[0].Value += 1
			err = verifyEvaluationGame(ctx, pair, parent, opponents, schedules[0], 0, g)
			if err == nil || !strings.Contains(err.Error(), "value/probabilities") {
				t.Fatal("stored value mismatch unchecked", err)
			}
			// Same candidate and frozen plan, uninterrupted: match IDs must not affect decisions/outcomes.
			continuous, err := RunAllocationValidation(ctx, engine, v, parent, sourceRoot, filepath.Join(base, "continuous.json"), nil)
			if err != nil {
				t.Fatal(err)
			}
			for ai := range report.Results {
				a, b := report.Results[ai], continuous.Results[ai]
				if !reflect.DeepEqual(a.Comparisons, b.Comparisons) {
					t.Fatal("resume changed outcomes")
				}
				for i := range a.Games {
					ga, gb := a.Games[i], b.Games[i]
					ga.Shard, gb.Shard = "", ""
					if !reflect.DeepEqual(ga, gb) {
						t.Fatal("resume changed scheduled game")
					}
				}
			}
			t.Logf("mode=%d 8 supplementary games:5 restored/3 new; both-branch preflight, orphan adoption, read-only verification, cutoff status and exact continuous outcomes checked", mode)
		})
	}
}

// Reuses the explicitly retained small native fixture; no source-search retry.
func TestNativeAllocationRetainedBoundaries(t *testing.T) {
	fixture, output, raw := os.Getenv("STONEAGE_ALLOCATION_RETAINED_FIXTURE"), os.Getenv("STONEAGE_ALLOCATION_BOUNDARY_OUTPUT"), os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if fixture == "" || output == "" || raw == "" {
		t.Skip("explicit retained fixture, output and native engine required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	sourceRoot := filepath.Join(fixture, "search")
	original := filepath.Join(fixture, "parent-evaluation.json")
	t.Run("global-prefix", func(t *testing.T) {
		spec, a, _, _, err := loadAllocationSpec(original + ".data")
		if err != nil {
			t.Fatal(err)
		}
		var report AllocationEvaluationReport
		if err = readObject(original, &report, 16<<20); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(output, "gap.json")
		copyEvaluationEvidence(t, original+".data", path+".data")
		g := report.Results[0].Games[3]
		for _, p := range []string{filepath.Join("balanced", "commits", "000000003.json"), filepath.Join("balanced", "shards", g.Shard+".manifest.json"), filepath.Join("balanced", "shards", g.Shard+".jsonl.gz")} {
			if err = os.Remove(filepath.Join(path+".data", p)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		reached := false
		_, err = RunAllocationValidation(ctx, engine, spec.Validation, a, sourceRoot, path, nil, EvaluationRecordingOptions{Resume: true, Restored: func(int, int) error { reached = true; return errors.New("preflight erroneously admitted gap") }})
		if err == nil || reached || !strings.Contains(err.Error(), "contiguous prefix") {
			t.Fatal("global allocation prefix not enforced", reached, err)
		}
	})
	t.Run("owned-model-snapshot", func(t *testing.T) {
		spec, a, _, _, err := loadAllocationSpec(original + ".data")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(output, "owned.json")
		n := 0
		_, err = RunAllocationValidation(ctx, engine, spec.Validation, a, sourceRoot, path, func(string, EvaluationGame) error {
			n++
			if n == 1 {
				for _, p := range a.Network.Parameters {
					if len(p.Values) > 0 {
						p.Values[0] += .125
						break
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = VerifyAllocationEvaluation(ctx, path, sourceRoot); err != nil {
			t.Fatal("caller mutation changed frozen evaluation", err)
		}
	})
}
