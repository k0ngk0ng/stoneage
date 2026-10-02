package battletrain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestDemonstrationExportPinsSavedWeightsWithoutInventingNativeGroups(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	root := filepath.Join(t.TempDir(), "run")
	ctx := context.Background()
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	first, firstState, _, err := LoadDemonstrationCheckpoint(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	firstID, _ := Digest(first)
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: root, Resume: true, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	pointer, _ := os.ReadFile(filepath.Join(root, DemonstrationPointer))
	path, err := ExportDemonstrationCandidate(ctx, root, firstID, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(path)
	if err != nil || a.Recorded == nil {
		t.Fatal(err)
	}
	if a.Schema != 3 || a.Recorded.Checkpoint != firstID || a.Recorded.Dataset != first.Dataset.Digest || !reflect.DeepEqual(a.Recorded.Sources, first.Dataset.Sources) || !reflect.DeepEqual(a.Recorded.RosterGroups, first.Dataset.Groups) || !reflect.DeepEqual(a.Network, firstState.Model) || a.Recorded.Epochs != 1 || a.Environment.Scenario != "" || len(a.DataGroups()) != 0 {
		t.Fatal("export changed weights/source or invented held-out independence")
	}
	after, _ := os.ReadFile(filepath.Join(root, DemonstrationPointer))
	if string(pointer) != string(after) {
		t.Fatal("old checkpoint export rewound progress")
	}
	latest, err := ExportDemonstrationCandidate(ctx, root, "", "")
	if err != nil || latest == path {
		t.Fatal("latest export did not select new state", err)
	}
	oldBytes, _ := os.ReadFile(path)
	if _, err := ExportDemonstrationCandidate(ctx, root, "", path); err == nil {
		t.Fatal("export overwrote different candidate")
	}
	nowBytes, _ := os.ReadFile(path)
	if string(oldBytes) != string(nowBytes) {
		t.Fatal("old model changed")
	}
	meta := a.Environment
	meta.Scenario = "controlled-battle-v8"
	eval := DefaultEvaluationConfig()
	eval.MatchesPerOpponent = 8
	schedule, err := prepareEvaluation(ctx, meta, a, []Opponent{{Name: "basic", Rule: "basic"}}, eval, nil, "")
	artifactID, _ := Digest(a)
	if err != nil || !reflect.DeepEqual(schedule.Report.UnverifiedSourceArtifacts, []string{artifactID}) || schedule.Report.ExcludedTrainingGroups != 0 {
		t.Fatal("recorded evaluation lost unknown source overlap", err)
	}
	// Frozen evidence remains necessary for re-export; exporting only weights
	// must not make a broken training directory appear valid.
	if err := os.WriteFile(filepath.Join(root, "demonstrations", first.Dataset.Digest+".jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "corrupt-source-model.json")
	if _, err := ExportDemonstrationCandidate(ctx, root, firstID, output); err == nil {
		t.Fatal("missing evidence ignored")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("corrupt export published")
	}
}

func TestDemonstrationExportRejectsUntrainedAndCanceledState(t *testing.T) {
	dataset, config := demonstrationRunFixture(t)
	root := filepath.Join(t.TempDir(), "run")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 1, Progress: func(p DemonstrationProgress) error { cancel(); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, c := range []context.Context{context.Background(), ctx} {
		if _, err := ExportDemonstrationCandidate(c, root, "", ""); err == nil {
			t.Fatal("empty/canceled export accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "models")); !os.IsNotExist(err) {
		t.Fatal("rejected export created models")
	}
}
