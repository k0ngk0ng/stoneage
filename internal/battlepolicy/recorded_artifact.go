package battlepolicy

import (
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

// CompatibleNativeEnvironment checks execution compatibility, not independent
// test coverage. Callers first validate the artifact; recorded source overlap
// must remain explicit in every native evaluation and downstream decision.
func (a Artifact) CompatibleNativeEnvironment(meta battleenv.Metadata) bool {
	if meta.Validate() != nil || ValidateFeatureEnvironment(a.Features, meta.Scenario) != nil || a.Environment.Rules != meta.Rules || a.Environment.Platform != meta.Platform {
		return false
	}
	return a.Environment.Scenario == meta.Scenario || a.Schema == 3 && a.Recorded != nil && a.Environment.Scenario == ""
}

// RecordedTraining names actual local commander evidence. It does not invent
// a controlled scenario contract or recover hidden enemy configurations.
// Schema 3 is a pure demonstration candidate; schema 4 retains this source
// while a native child adds its actual controlled training environment/groups.
// Schema 5 additionally carries indirect recorded influence from build search.
type RecordedTraining struct {
	Schema       string   `json:"schema"`
	Checkpoint   string   `json:"checkpoint"`
	Dataset      string   `json:"dataset"`
	Sources      []string `json:"source_databases"`
	GroupKind    string   `json:"group_kind"`
	RosterGroups []string `json:"roster_groups"`
	Epochs       int      `json:"completed_epochs"`
}

func (r *RecordedTraining) Validate() error {
	if r == nil || r.Schema != "commander-recorded-training-v1" || !hashString(r.Checkpoint) || !hashString(r.Dataset) || r.GroupKind != "recorded-roster-v1" || r.Epochs < 1 || r.Epochs > 10000 {
		return fmt.Errorf("invalid recorded model provenance")
	}
	for _, list := range [][]string{r.Sources, r.RosterGroups} {
		if len(list) == 0 || len(list) > 10000 {
			return fmt.Errorf("recorded provenance requires bounded source/group identities")
		}
		previous := ""
		for _, id := range list {
			if !hashString(id) || id <= previous {
				return fmt.Errorf("recorded source/groups must be sorted unique hashes")
			}
			previous = id
		}
	}
	return nil
}

func (a Artifact) validateRecordedTraining() error {
	if err := a.Recorded.Validate(); err != nil {
		return err
	}
	if a.Schema == 4 || a.Schema == 5 || a.Schema == 6 {
		if a.Parent == "" || a.Experiment == "" || a.Schema != 6 && len(a.Modes) != 1 {
			return fmt.Errorf("native recorded-source child requires its parent and experiment")
		}
		return ValidateFeatureEnvironment(a.Features, a.Environment.Scenario)
	}
	if a.TrainingReport != a.Recorded.Checkpoint {
		return fmt.Errorf("recorded report differs from source checkpoint")
	}
	if a.Environment.Scenario != "" || !hashString(a.Environment.Rules) || a.Environment.Platform != "linux-amd64" && a.Environment.Platform != "linux-arm64" || len(a.Modes) != 1 {
		return fmt.Errorf("recorded model requires observed rules/platform and one mode; no synthetic scenario may be fabricated")
	}
	if a.Parent != "" || a.Experiment != "" || len(a.TrainingShards) != 0 || len(a.TrainingGroups) != 0 || len(a.SelectionGroups) != 0 || len(a.HeldoutGroups) != 0 {
		return fmt.Errorf("recorded roster provenance cannot masquerade as native training/held-out groups")
	}
	return nil
}

// Selection sources describe indirect influence through roster search, not
// demonstrations used for gradient updates. Keep the two domains separate.
func (a Artifact) HasRecordedSource() bool {
	return a.Recorded != nil || len(a.RecordedSelectionArtifacts) > 0
}

func (a Artifact) validateRecordedSelection() error {
	if a.Schema != 6 && (a.Schema == 5) != (len(a.RecordedSelectionArtifacts) > 0) {
		return fmt.Errorf("recorded build-selection sources require artifact schema 5 or 6")
	}
	if a.Schema != 5 && a.Schema != 6 || len(a.RecordedSelectionArtifacts) == 0 {
		return nil
	}
	if a.Experiment == "" || a.Schema != 6 && len(a.Modes) != 1 || len(a.RecordedSelectionArtifacts) > 10000 {
		return fmt.Errorf("recorded selection requires a native experiment and bounded sources")
	}
	previous := ""
	for _, id := range a.RecordedSelectionArtifacts {
		if !hashString(id) || id <= previous {
			return fmt.Errorf("recorded selection artifacts must be sorted unique hashes")
		}
		previous = id
	}
	return nil
}
