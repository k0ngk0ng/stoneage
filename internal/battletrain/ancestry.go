package battletrain

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func (x Experiment) validateInitialModel(a *battlepolicy.Artifact) error {
	if (a == nil) != (x.InitialModel == "") {
		return fmt.Errorf("experiment requires its declared --from-model")
	}
	var recordedSelection []string
	if a != nil {
		recordedSelection = a.RecordedSelectionArtifacts
	}
	if x.Pool != nil {
		recordedSelection = sortedUnion(recordedSelection, x.Pool.RecordedSelectionArtifacts)
	}
	if !slices.Equal(recordedSelection, x.RecordedSelectionArtifacts) {
		return fmt.Errorf("experiment recorded selection differs from sources")
	}
	if a == nil {
		return nil
	}
	if e := a.Validate(); e != nil {
		return e
	}
	id, _ := Digest(*a)
	// Initialization can transfer weights across team sizes. The frozen target
	// experiment supplies the new mode; this does not extend the parent's
	// declared inference modes or bypass independent child evaluation.
	if id != x.InitialModel || !a.CompatibleNativeEnvironment(x.Environment) || !reflect.DeepEqual(a.TrainingGroups, x.InitialTrainingGroups) || !reflect.DeepEqual(a.Recorded, x.InitialRecorded) {
		return fmt.Errorf("initial model or ancestry differs from experiment")
	}
	selection := sortedUnion(a.SelectionGroups, a.HeldoutGroups)
	if x.Pool != nil {
		selection = sortedUnion(selection, x.Pool.SelectionGroups)
	}
	if !slices.Equal(selection, x.SelectionGroups) {
		return fmt.Errorf("experiment selection ancestry differs from sources")
	}
	return nil
}

func (x Experiment) validateCandidateProvenance(a battlepolicy.Artifact) error {
	if !slices.Equal(a.RecordedSelectionArtifacts, x.RecordedSelectionArtifacts) {
		return fmt.Errorf("candidate lost recorded build-selection ancestry")
	}
	if !reflect.DeepEqual(a.Recorded, x.InitialRecorded) {
		return fmt.Errorf("candidate lost recorded-source ancestry")
	}
	if a.Parent != x.InitialModel || !slices.Equal(a.SelectionGroups, x.SelectionGroups) {
		return fmt.Errorf("candidate lost experiment model/selection ancestry")
	}
	if len(a.HeldoutGroups) > 0 && !slices.Equal(a.HeldoutGroups, x.heldoutGroups()) {
		return fmt.Errorf("candidate held-out declaration differs from its experiment")
	}
	allowed := x.allowedTrainingGroups()
	actual := map[string]bool{}
	for _, id := range a.TrainingGroups {
		if !allowed[id] {
			return fmt.Errorf("candidate training provenance is outside declared families and ancestry")
		}
		actual[id] = true
	}
	for _, id := range x.InitialTrainingGroups {
		if !actual[id] {
			return fmt.Errorf("candidate dropped parent training groups")
		}
	}
	return nil
}

// The exact declared parent may serve as the pre-training validation baseline.
// Do not relabel its artifact/experiment or grant it a new inference mode. Final
// selection and promotion still require a trained child of this experiment.
func (x Experiment) validateEvaluationCandidate(a battlepolicy.Artifact, split string) error {
	if split != "validation" && split != "test" {
		return fmt.Errorf("evaluation split must be validation or test")
	}
	if split == "validation" && x.InitialModel != "" {
		id, err := Digest(a)
		if err != nil {
			return err
		}
		if id == x.InitialModel {
			return x.validateInitialModel(&a)
		}
	}
	id, _ := Digest(x)
	if a.Experiment != id {
		return fmt.Errorf("evaluation requires a trained experiment candidate, or its exact declared parent for validation")
	}
	return x.validateCandidateProvenance(a)
}

func loadInitialModel(root string, c RunConfig) (*battlepolicy.Artifact, error) {
	if c.InitialModel == "" {
		return nil, nil
	}
	a, e := battlepolicy.LoadArtifact(filepath.Join(root, "initial-models", c.InitialModel+".json"))
	if e != nil {
		return nil, e
	}
	id, _ := Digest(a)
	if id != c.InitialModel || a.Network.Config != c.Network {
		return nil, fmt.Errorf("initial model checksum/network differs from checkpoint")
	}
	return &a, nil
}
