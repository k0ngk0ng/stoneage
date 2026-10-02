package battletrain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Reuses a previously completed native fixture without starting an engine or
// spending another match. All export mutations go into a private fixture copy.
func TestMixedExportRetainedNativeFixture(t *testing.T) {
	source := os.Getenv("STONEAGE_MIXED_EXPORT_FIXTURE")
	if source == "" {
		t.Skip("explicit retained mixed native fixture required")
	}
	root := filepath.Join(t.TempDir(), "copy")
	copyMixedRunFixture(t, source, root)
	verifyMixedExport(t, root)
}

func verifyMixedExport(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	loaded, err := LoadMixedCheckpoint(ctx, root)
	if err != nil || len(loaded.Receipts) != 2 {
		t.Fatal("requires two committed mixed batches", err)
	}
	before, err := os.ReadFile(filepath.Join(root, MixedPointer))
	if err != nil {
		t.Fatal(err)
	}
	path, err := ExportMixedCandidate(ctx, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Schema != 6 || a.Mixed == nil || a.Mixed.Batches != 2 || a.Mixed.Updates != loaded.Learning.Optimizer.Step || !reflect.DeepEqual(a.Network, loaded.Learning.Model) || !reflect.DeepEqual(a.TrainingGroups, loaded.TrainingGroups) || !reflect.DeepEqual(a.Modes, []int{1, 5}) {
		t.Fatal("export changed weights, provenance or shared update accounting")
	}
	var report MixedExportReport
	if err := readObject(filepath.Join(root, "reports", a.TrainingReport+".json"), &report, mixedExperimentBytes+(8<<20)); err != nil {
		t.Fatal(err)
	}
	id, _ := Digest(report)
	if id != a.TrainingReport || report.Schema != "commander-mixed-export-v1" || !reflect.DeepEqual(report.Checkpoint, loaded.Checkpoint) || !reflect.DeepEqual(report.Experiment, loaded.Experiment) || !reflect.DeepEqual(report.Training, *a.Mixed) {
		t.Fatal("export did not bind its frozen experiment and receipt chain")
	}
	for _, kind := range []string{"old-schema", "missing-training", "untrained", "mode", "mode-experiment", "weight", "quota", "training", "heldout", "selection", "recorded-selection", "parent"} {
		t.Run(kind, func(t *testing.T) {
			var bad battlepolicy.Artifact
			if err := json.Unmarshal(mustJSON(t, a), &bad); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "old-schema":
				bad.Schema = 2
			case "missing-training":
				bad.Mixed = nil
			case "untrained":
				bad.Mixed.Updates = 0
			case "mode":
				bad.Modes[1], bad.Mixed.Modes[1].Mode = 4, 4
			case "mode-experiment":
				bad.Mixed.Modes[1].Experiment = strings.Repeat("b", 64)
			case "weight":
				bad.Mixed.Modes[1].Weight++
			case "quota":
				bad.Mixed.Modes[1].FamiliesPerBatch++
			case "training":
				bad.TrainingGroups = sortedUnion(bad.TrainingGroups, []string{strings.Repeat("b", 64)})
			case "heldout":
				bad.HeldoutGroups = nil
			case "selection":
				bad.SelectionGroups = []string{strings.Repeat("b", 64)}
			case "recorded-selection":
				bad.RecordedSelectionArtifacts = []string{strings.Repeat("b", 64)}
			case "parent":
				bad.Parent = strings.Repeat("b", 64)
			}
			if loaded.Experiment.ValidateCandidate(bad) == nil {
				t.Fatal("false mixed candidate provenance accepted")
			}
		})
	}
	// Selecting a completed historical update must not rewind current training.
	files, err := os.ReadDir(filepath.Join(root, "mixed-checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	foundInitial, foundPrior := false, false
	for _, file := range files {
		var c MixedCheckpoint
		if err := readObject(filepath.Join(root, "mixed-checkpoints", file.Name()), &c, 4<<20); err != nil {
			t.Fatal(err)
		}
		checkpoint := strings.TrimSuffix(file.Name(), ".json")
		if c.NextGame == 0 {
			foundInitial = true
			if _, err := ExportMixedCandidate(ctx, root, checkpoint, ""); err == nil || !strings.Contains(err.Error(), "accepted shared update") {
				t.Fatal("untrained checkpoint exported", err)
			}
		}
		if len(c.Reports) == 1 && len(c.Pending) == 0 {
			foundPrior = true
			previous, err := ExportMixedCandidate(ctx, root, checkpoint, "")
			if err != nil || previous == path {
				t.Fatal("historical export not distinct", err)
			}
			b, err := battlepolicy.LoadArtifact(previous)
			if err != nil || b.Mixed.Checkpoint != checkpoint || b.Mixed.Batches != 1 || reflect.DeepEqual(b.Network, a.Network) {
				t.Fatal("historical export lost actual update", err)
			}
			if again, err := ExportMixedCandidate(ctx, root, checkpoint, previous); err != nil || again != previous {
				t.Fatal("idempotent export failed", err)
			}
		}
	}
	after, err := os.ReadFile(filepath.Join(root, MixedPointer))
	if err != nil || string(before) != string(after) || !foundInitial || !foundPrior {
		t.Fatal("export changed latest pointer or historical fixtures were missing", err)
	}
	if _, err := LoadMixedCheckpointID(ctx, root, "../outside"); err == nil {
		t.Fatal("non-digest checkpoint accepted")
	}
	for _, p := range loaded.Experiment.Parts {
		if p.Experiment.validateEvaluationCandidate(a, "validation") == nil {
			t.Fatal("single experiment silently accepted composite candidate")
		}
	}
	t.Logf("mixed export %s; modes=%v; updates=%d; historical export preserved latest pointer", filepath.Base(path), a.Modes, a.Mixed.Updates)
}
