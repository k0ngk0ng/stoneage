package battletrain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeclaredParentValidationWithoutRelabelingOrTestAccess(t *testing.T) {
	parent := experimentCandidate(t, experimentFixture(t))
	before, _ := json.Marshal(parent)
	c := DefaultEvaluationConfig()
	c.Seed = 881
	x, err := NewExperimentFromSources(context.Background(), parent.Environment, c, [3]int{2, 1, 1}, nil, &parent)
	if err != nil {
		t.Fatal(err)
	}
	opponents := []Opponent{{Name: "basic", Rule: "basic"}}
	s, err := prepareEvaluation(context.Background(), parent.Environment, parent, opponents, c, &x, "validation")
	if err != nil {
		t.Fatal("declared parent cannot serve as baseline", err)
	}
	xid, _ := Digest(x)
	want, err := x.evaluationSuite("validation")
	if err != nil || s.Report.CandidateArtifact != x.InitialModel || s.Report.Candidate != parent.WeightsDigest || s.Report.ExperimentDigest != xid || !reflect.DeepEqual(s.Suite, want) {
		t.Fatal("parent baseline lost source identity or altered suite", err)
	}
	for _, split := range []string{"test", "train", ""} {
		if _, err := prepareEvaluation(context.Background(), parent.Environment, parent, opponents, c, &x, split); err == nil {
			t.Fatal("parent bypassed experiment split boundary", split)
		}
	}
	if err := FreezeFinalSelection(filepath.Join(t.TempDir(), "selection.json"), x, parent, opponents); err == nil {
		t.Fatal("parent gained final selection eligibility")
	}
	changed := parent
	changed.TrainingReport = strings.Repeat("a", 64)
	if _, err := prepareEvaluation(context.Background(), parent.Environment, changed, opponents, c, &x, "validation"); err == nil {
		t.Fatal("same weights with altered provenance passed as declared parent")
	}
	// A child opponent from this same experiment has reserved the same heldout
	// groups, but has not trained on them. Parent identity must not cause the
	// child's reservation to be mistaken for foreign evaluation exposure.
	child := experimentCandidate(t, x)
	child.Parent = x.InitialModel
	child.TrainingGroups = sortedUnion(child.TrainingGroups, x.InitialTrainingGroups)
	child.SelectionGroups = x.SelectionGroups
	if _, err := prepareEvaluation(context.Background(), parent.Environment, parent, []Opponent{{Name: "child", Model: &child}}, c, &x, "validation"); err != nil {
		t.Fatal("same experiment opponent reservation mistaken for leakage", err)
	}
	if _, err := prepareEvaluation(context.Background(), parent.Environment, child, opponents, c, &x, "test"); err != nil {
		t.Fatal("valid child test eligibility changed", err)
	}
	// Parent source restrictions must still be checked even when its digest is
	// correctly named by the experiment.
	badSource := x
	badSource.InitialTrainingGroups = nil
	if _, err := prepareEvaluation(context.Background(), parent.Environment, parent, opponents, c, &badSource, "validation"); err == nil {
		t.Fatal("parent training exclusions dropped")
	}
	c.Mode = 2
	transfer, err := NewExperimentFromSources(context.Background(), parent.Environment, c, [3]int{2, 1, 1}, nil, &parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareEvaluation(context.Background(), parent.Environment, parent, opponents, c, &transfer, "validation"); err == nil || !strings.Contains(err.Error(), "mode incompatible") {
		t.Fatal("parent acquired an untrained inference mode", err)
	}
	after, _ := json.Marshal(parent)
	if string(before) != string(after) {
		t.Fatal("evaluation preparation mutated parent artifact")
	}
}
