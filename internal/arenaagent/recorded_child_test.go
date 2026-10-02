package arenaagent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// The small numerical fixture checks schema dispatch, not training quality.
func TestNativeRecordedChildUsesSameCommanderInference(t *testing.T) {
	base, _ := neuralFixtureModel(t, 2)
	a := *base.neural
	h := strings.Repeat("0", 64)
	a.Schema, a.Parent, a.Experiment = 4, h, h
	a.Recorded = &battlepolicy.RecordedTraining{Schema: "commander-recorded-training-v1", Checkpoint: h, Dataset: h, Sources: []string{h}, GroupKind: "recorded-roster-v1", RosterGroups: []string{h}, Epochs: 1}
	path := filepath.Join(t.TempDir(), "child.json")
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	child, err := NewLearned(path, 2)
	if err != nil {
		t.Fatal("schema 4 not accepted by online client", err)
	}
	team, history := neuralFixtureTeam(t, 2, 0)
	want, err := base.Decide(context.Background(), team, history)
	if err != nil {
		t.Fatal(err)
	}
	got, err := child.Decide(context.Background(), team, history)
	if err != nil || !reflect.DeepEqual(got.Plan, want.Plan) || got.Version == want.Version {
		t.Fatal("source metadata changed inference or lost policy identity", err)
	}
	if localModel(&Hybrid{Local: child}) != child {
		t.Fatal("hybrid lost recorded child model")
	}
}

func TestRecordedBuildSelectionUsesSameCommanderInference(t *testing.T) {
	base, _ := neuralFixtureModel(t, 2)
	for _, direct := range []bool{false, true} {
		a := *base.neural
		h := strings.Repeat("0", 64)
		a.Schema, a.Experiment = 5, h
		a.RecordedSelectionArtifacts = []string{h}
		if direct {
			a.Parent = h
			a.Recorded = &battlepolicy.RecordedTraining{Schema: "commander-recorded-training-v1", Checkpoint: h, Dataset: h, Sources: []string{h}, GroupKind: "recorded-roster-v1", RosterGroups: []string{h}, Epochs: 1}
		}
		path := filepath.Join(t.TempDir(), "selected.json")
		if err := os.WriteFile(path, enc(a), 0600); err != nil {
			t.Fatal(err)
		}
		selected, err := NewLearned(path, 2)
		if err != nil {
			t.Fatal("schema 5 rejected by local client", err)
		}
		team, history := neuralFixtureTeam(t, 2, 0)
		want, err := base.Decide(context.Background(), team, history)
		if err != nil {
			t.Fatal(err)
		}
		got, err := selected.Decide(context.Background(), team, history)
		if err != nil || !reflect.DeepEqual(got.Plan, want.Plan) || got.Version == want.Version {
			t.Fatal("selection metadata changed inference or lost identity", err)
		}
		if localModel(&Hybrid{Local: selected}) != selected {
			t.Fatal("hybrid lost selected model")
		}
	}
}
