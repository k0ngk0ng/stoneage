package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func feedbackStorageFixture(t *testing.T) (Experiment, FeedbackExample, map[string]battlepolicy.Artifact) {
	t.Helper()
	e := feedbackFixture(t)
	c := DefaultEvaluationConfig()
	c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = 1, 120, 0, 35, e.Source.Setup.MaxTurns
	x, err := NewExperiment(context.Background(), battleenv.Metadata{Rules: e.Source.Rules, Platform: e.Source.Platform, Scenario: e.Source.Environment}, c, [3]int{1, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	x.Families[0] = ExperimentFamily{Group: e.Source.Group, Split: "train", Scenario: e.Source.Setup}
	if err := x.Validate(); err != nil {
		t.Fatal(err)
	}
	a := experimentCandidate(t, x)
	return x, e, map[string]battlepolicy.Artifact{e.Source.Policy: a}
}

func TestFeedbackDatasetRoundTripAndTraining(t *testing.T) {
	x, e, artifacts := feedbackStorageFixture(t)
	ctx, root := context.Background(), t.TempDir()
	before, _ := Digest(e)
	id, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts)
	if err != nil || retry != id {
		t.Fatal("identical save not reusable", err)
	}
	d, gotX, got, err := LoadFeedbackDataset(ctx, root, id)
	if err != nil || !reflect.DeepEqual(x, gotX) || len(got) != 1 {
		t.Fatal("feedback experiment round trip differs", err)
	}
	gotID, _ := Digest(got[0])
	if gotID != before {
		t.Fatal("feedback round trip changed serialized facts")
	}
	if got[0].Behavior.Model == e.Behavior.Model {
		t.Fatal("loaded frozen behavior aliases caller weights")
	}
	sources, _, err := LoadShard(filepath.Join(root, "sources"), d.Source)
	// JSON omitempty normalizes empty observation slices to nil. Compare
	// every serialized fact, then verify numerical training parity below.
	wantSources, _ := Digest([]Trajectory{e.Source})
	gotSources, _ := Digest(sources)
	if err != nil || gotSources != wantSources {
		t.Fatal("suggestions replaced actual source actions", err)
	}
	m, loaded := testModel(t), testModel(t)
	a, b := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	cfg := DefaultWarmupConfig().Update
	r1, err := ImitateFeedback(ctx, m, a, []FeedbackExample{e}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ImitateFeedback(ctx, loaded, b, got, cfg)
	if err != nil || !reflect.DeepEqual(r1, r2) || !reflect.DeepEqual(m, loaded) || !reflect.DeepEqual(a, b) {
		t.Fatal("persisted feedback changes numerical training", err)
	}
	after, _ := Digest(e)
	if before != after {
		t.Fatal("storage or training mutated source evidence")
	}
}

func TestFeedbackDatasetRejectsContaminationBeforeWrites(t *testing.T) {
	for _, kind := range []string{"validation", "test", "missing-artifact", "unused-artifact", "foreign-experiment", "tainted-model", "wrong-model", "wrong-mode", "duplicate", "wrong-probability", "missing-turns", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			x, e, artifacts := feedbackStorageFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := artifacts[e.Source.Policy]
			switch kind {
			case "validation", "test":
				for i := range x.Families {
					if x.Families[i].Split == kind {
						x.Families[i].Split = "train"
						break
					}
				}
				x.Families[0].Split = kind
			case "missing-artifact":
				delete(artifacts, e.Source.Policy)
			case "unused-artifact":
				artifacts["unused"] = a
			case "foreign-experiment":
				a.Experiment = a.TrainingReport
			case "tainted-model":
				a.TrainingGroups = []string{x.groups("validation")[0].Group}
				a.HeldoutGroups = nil // Cannot evade split checks by omitting it.
			case "wrong-model":
				var err error
				a.Network, err = battlenet.NewModel[float32](a.Network.Config, 991)
				if err != nil {
					t.Fatal(err)
				}
				a.WeightsDigest, _ = ModelDigest(a.Network)
			case "wrong-mode":
				a.Modes = []int{2}
			case "wrong-probability":
				e.Source.Steps[0].LogProb -= .01
				e.Source.Steps[0].ConditionalLogProbs[0] -= .01
				e.Targets, _ = LabelRuleFeedback(ctx, e.Source, e.Targets.Teacher)
			case "missing-turns":
				e.Source.Steps = nil
			case "cancel":
				cancel()
			}
			if kind != "missing-artifact" {
				artifacts[e.Source.Policy] = a
			}
			examples := []FeedbackExample{e}
			if kind == "duplicate" {
				examples = append(examples, e)
			}
			root := filepath.Join(t.TempDir(), "uncreated")
			_, err := SaveFeedbackDataset(ctx, root, x, examples, artifacts)
			if err == nil || kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("invalid input accepted", err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid input wrote dataset files", err)
			}
		})
	}
}

func TestFeedbackDatasetRejectsDamagedOrIncompleteObjects(t *testing.T) {
	for _, kind := range []string{"manifest", "experiment", "shard", "labels", "behavior", "missing-label", "missing-behavior", "rebound-label", "rebound-artifact", "unknown-field", "path", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			x, e, artifacts := feedbackStorageFixture(t)
			ctx, root := context.Background(), t.TempDir()
			id, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts)
			if err != nil {
				t.Fatal(err)
			}
			d, _, _, err := LoadFeedbackDataset(ctx, root, id)
			if err != nil {
				t.Fatal(err)
			}
			paths := map[string]string{
				"manifest":   filepath.Join(root, "datasets", id+".json"),
				"experiment": filepath.Join(root, "experiments", d.Experiment+".json"),
				"shard":      filepath.Join(root, "sources", d.Source+".jsonl.gz"),
				"labels":     filepath.Join(root, "labels", d.Examples[0].Targets+".json"),
				"behavior":   filepath.Join(root, "behaviors", d.Examples[0].Behavior.Artifact+".json"),
			}
			switch kind {
			case "missing-label", "missing-behavior":
				path := paths["labels"]
				if kind == "missing-behavior" {
					path = paths["behavior"]
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "rebound-label":
				e.Targets.Choices[0][0] = -1
				d.Examples[0].Targets, _ = Digest(e.Targets)
				if err := writeObject(filepath.Join(root, "labels", d.Examples[0].Targets+".json"), e.Targets); err != nil {
					t.Fatal(err)
				}
			case "rebound-artifact":
				a := artifacts[e.Source.Policy]
				a.TrainingGroups = []string{x.groups("test")[0].Group}
				a.HeldoutGroups = nil
				d.Examples[0].Behavior.Artifact, _ = Digest(a)
				if err := writeObject(filepath.Join(root, "behaviors", d.Examples[0].Behavior.Artifact+".json"), a); err != nil {
					t.Fatal(err)
				}
			case "path":
				d.Examples[0].Targets = "../outside"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unknown-field":
				b, _ := json.Marshal(d)
				b = append([]byte(`{"unknown":true,`), b[1:]...)
				if err := os.WriteFile(paths["manifest"], b, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(paths[kind], []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "rebound-label" || kind == "rebound-artifact" || kind == "path" {
				id, _ = Digest(d)
				if err := writeObject(filepath.Join(root, "datasets", id+".json"), d); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, got, err := LoadFeedbackDataset(ctx, root, id); err == nil || got != nil {
				t.Fatal("damaged dataset returned training examples", err)
			}
		})
	}
}

func TestFeedbackDatasetPublishesManifestLastAndRefusesOverwrite(t *testing.T) {
	x, e, artifacts := feedbackStorageFixture(t)
	ctx, root := context.Background(), t.TempDir()
	id, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _, err := LoadFeedbackDataset(ctx, root, id)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "datasets", id+".json")
	label := filepath.Join(root, "labels", d.Examples[0].Targets+".json")
	// Emulate dependencies left before a commit, with conflicting label data.
	// A retry must preserve that evidence and never publish a complete batch.
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	conflict := []byte("{\"conflicting_evidence\":true}\n")
	if err := os.WriteFile(label, conflict, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts); err == nil {
		t.Fatal("overwrote different existing evidence")
	}
	if b, err := os.ReadFile(label); err != nil || string(b) != string(conflict) {
		t.Fatal("conflicting evidence was modified", err)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatal("failed save published a dataset manifest", err)
	}
	if _, _, examples, err := LoadFeedbackDataset(ctx, root, id); err == nil || examples != nil {
		t.Fatal("incomplete dataset is trainable", err)
	}
	if err := os.Remove(label); err != nil {
		t.Fatal(err)
	}
	if retry, err := SaveFeedbackDataset(ctx, root, x, []FeedbackExample{e}, artifacts); err != nil || retry != id {
		t.Fatal("valid orphan dependencies cannot be reused", err)
	}
}
