package battlepolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func recordedArtifactFixture(t *testing.T) Artifact {
	t.Helper()
	c := NetworkConfig()
	c.Width, c.Heads, c.Layers = 8, 2, 1
	m, err := battlenet.NewModel[float32](c, 37)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NetworkDigest(m)
	h := strings.Repeat("1", 64)
	return Artifact{Schema: 3, Architecture: "commander-policy-v2", Features: FeatureVersion, Actions: ActionVersion, Status: "candidate", Environment: battleenv.Metadata{Rules: h, Platform: "linux-arm64"}, Modes: []int{2}, WeightsDigest: id, TrainingReport: h, Network: m,
		Recorded: &RecordedTraining{Schema: "commander-recorded-training-v1", Checkpoint: h, Dataset: h, Sources: []string{h}, GroupKind: "recorded-roster-v1", RosterGroups: []string{h}, Epochs: 1}}
}

func TestRecordedArtifactSeparatesObservedAndSyntheticProvenance(t *testing.T) {
	a := recordedArtifactFixture(t)
	raw, _ := json.Marshal(a)
	got, err := DecodeArtifact(raw)
	if err != nil || got.Environment.Scenario != "" || len(got.DataGroups()) != 0 || got.Recorded == nil {
		t.Fatal("recorded source became synthetic coverage", got, err)
	}
	for name, mutate := range map[string]func(*Artifact){
		"old_schema":              func(a *Artifact) { a.Schema = 2 },
		"missing_provenance":      func(a *Artifact) { a.Recorded = nil },
		"pretend_native_scenario": func(a *Artifact) { a.Environment.Scenario = "controlled-battle-v8" },
		"native_groups":           func(a *Artifact) { a.TrainingGroups = a.Recorded.RosterGroups },
		"native_shards":           func(a *Artifact) { a.TrainingShards = []string{a.Recorded.Dataset} },
		"heldout":                 func(a *Artifact) { a.HeldoutGroups = a.Recorded.RosterGroups },
		"parent":                  func(a *Artifact) { a.Parent = a.Recorded.Checkpoint },
		"experiment":              func(a *Artifact) { a.Experiment = a.Recorded.Checkpoint },
		"selection":               func(a *Artifact) { a.SelectionGroups = a.Recorded.RosterGroups },
		"multiple_modes":          func(a *Artifact) { a.Modes = []int{1, 2} },
		"unknown_platform":        func(a *Artifact) { a.Environment.Platform = "unknown" },
		"no_epoch":                func(a *Artifact) { a.Recorded.Epochs = 0 },
		"false_group_kind":        func(a *Artifact) { a.Recorded.GroupKind = "scenario" },
		"no_sources":              func(a *Artifact) { a.Recorded.Sources = nil },
		"duplicate_groups": func(a *Artifact) {
			a.Recorded.RosterGroups = append(a.Recorded.RosterGroups, a.Recorded.RosterGroups[0])
		},
		"report": func(a *Artifact) { a.TrainingReport = strings.Repeat("2", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var copy Artifact
			if err := json.Unmarshal(raw, &copy); err != nil {
				t.Fatal(err)
			}
			mutate(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("invalid source declaration accepted")
			}
		})
	}
}
