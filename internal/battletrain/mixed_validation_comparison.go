package battletrain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const mixedValidationComparisonSchema = "commander-mixed-validation-comparison-v1"
const mixedValidationComparisonBytes = 2*mixedExperimentBytes + (1 << 20)

// MixedValidationComparison explicitly binds two training experiments which
// intentionally share the same frozen families and partitions. Different
// parents or learning objectives retain their own identities. This declaration
// is validation-only: it neither releases reserved tests nor certifies matched
// training budgets, equal information, superior coordination or model strength.
//
// Merely loading this declaration does not change existing evaluation APIs or
// their exclusion of foreign held-out groups. An evaluator must bind this full
// declaration to its evidence and validate the actual pair before using it.
type MixedValidationComparison struct {
	Schema      string          `json:"schema"`
	LeftDigest  string          `json:"left_experiment_digest"`
	RightDigest string          `json:"right_experiment_digest"`
	Left        MixedExperiment `json:"left_experiment"`
	Right       MixedExperiment `json:"right_experiment"`
}

func NewMixedValidationComparison(left, right MixedExperiment) (MixedValidationComparison, error) {
	x := MixedValidationComparison{Schema: mixedValidationComparisonSchema, Left: left, Right: right}
	var err error
	x.LeftDigest, err = Digest(left)
	if err != nil {
		return x, err
	}
	x.RightDigest, err = Digest(right)
	if err != nil {
		return x, err
	}
	data, err := json.Marshal(x)
	if err != nil {
		return x, err
	}
	if len(data) > mixedValidationComparisonBytes {
		return x, fmt.Errorf("mixed validation comparison exceeds size limit")
	}
	// Do not share nested family, roster, provenance or objective slices with
	// either caller; a later mutation must not change a declared comparison.
	var owned MixedValidationComparison
	if err := json.Unmarshal(data, &owned); err != nil {
		return owned, err
	}
	return owned, owned.Validate()
}

func (x MixedValidationComparison) Validate() error {
	if x.Schema != mixedValidationComparisonSchema {
		return fmt.Errorf("unsupported mixed validation comparison schema")
	}
	for i, part := range []MixedExperiment{x.Left, x.Right} {
		if err := part.Validate(); err != nil {
			return fmt.Errorf("comparison experiment %d: %w", i, err)
		}
		id, err := Digest(part)
		if err != nil || id != []string{x.LeftDigest, x.RightDigest}[i] {
			return fmt.Errorf("comparison experiment %d digest mismatch", i)
		}
	}
	if len(x.Left.Parts) != len(x.Right.Parts) {
		return fmt.Errorf("comparison experiments must declare the same modes")
	}
	for i, part := range x.Left.Parts {
		left, right := part.Experiment, x.Right.Parts[i].Experiment
		// An equal set of labels alone is insufficient. Check complete scenario
		// values, order and split assignments, including still-reserved tests.
		if left.Environment != right.Environment ||
			experimentEvaluationConfig(left, "validation") != experimentEvaluationConfig(right, "validation") ||
			!reflect.DeepEqual(left.Families, right.Families) {
			return fmt.Errorf("comparison mode %d differs in environment, settings or frozen families/partitions", left.Mode)
		}
	}
	return nil
}

// ValidatePair returns ONLY the requested mode's shared validation identities.
// All usual candidate provenance checks still apply. Callers must not treat
// this list as permission to ignore either model's training/selection data or
// any other held-out group, and must retain both unchanged artifact identities.
func (x MixedValidationComparison) ValidatePair(left, right battlepolicy.Artifact, mode int, split string) ([]string, error) {
	if split != "validation" {
		return nil, fmt.Errorf("shared mixed comparison is validation-only")
	}
	if err := x.Validate(); err != nil {
		return nil, err
	}
	if err := x.Left.ValidateCandidate(left); err != nil {
		return nil, fmt.Errorf("left comparison candidate: %w", err)
	}
	if err := x.Right.ValidateCandidate(right); err != nil {
		return nil, fmt.Errorf("right comparison candidate: %w", err)
	}
	part, err := x.Left.evaluationPart(mode)
	if err != nil {
		return nil, err
	}
	consumed := map[string]bool{}
	for _, a := range []battlepolicy.Artifact{left, right} {
		for _, id := range a.DataGroups() {
			consumed[id] = true
		}
	}
	var groups []string
	for _, family := range part.groups("validation") {
		if consumed[family.Group] {
			return nil, fmt.Errorf("shared validation overlaps candidate training or selection")
		}
		groups = append(groups, family.Group)
	}
	return groups, nil
}

func SaveMixedValidationComparison(path string, x MixedValidationComparison) (string, error) {
	if err := x.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(x)
	if err != nil {
		return "", err
	}
	if len(data) > mixedValidationComparisonBytes {
		return "", fmt.Errorf("mixed validation comparison exceeds size limit")
	}
	id, err := Digest(x)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return id, writeObject(path, x)
}

func LoadMixedValidationComparison(path string) (MixedValidationComparison, error) {
	var x MixedValidationComparison
	if err := readObject(path, &x, mixedValidationComparisonBytes); err != nil {
		return x, err
	}
	return x, x.Validate()
}
