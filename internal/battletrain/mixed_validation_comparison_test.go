package battletrain

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMixedValidationComparisonKeepsForeignProtection(t *testing.T) {
	left := mixedExperimentFixture(t)
	right := cloneMixedExperiment(t, left)
	right.Mixture.Modes[0].Weight++ // Distinct legitimate experiment identity.
	a, b := mixedCandidateFixture(t, left), mixedCandidateFixture(t, right)
	x, err := NewMixedValidationComparison(left, right)
	if err != nil {
		t.Fatal(err)
	}
	beforeA, _ := Digest(a)
	beforeB, _ := Digest(b)
	for _, mode := range []int{1, 5} {
		groups, err := x.ValidatePair(a, b, mode, "validation")
		if err != nil || len(groups) != 3 {
			t.Fatal("explicit pair not recognized", mode, err)
		}
		part, _ := left.evaluationPart(mode)
		for i, f := range part.groups("validation") {
			if groups[i] != f.Group {
				t.Fatal("different validation family authorized")
			}
		}
		// The ordinary evaluator must still reject the foreign held-out overlap.
		// Creating a declaration is not an implicit global exception.
		_, err = prepareEvaluationWithMixed(context.Background(), a.Environment, a, []Opponent{{Name: "peer", Model: &b}}, EvaluationConfig{}, &part, &left, "validation")
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatal("foreign heldout protection changed", err)
		}
	}
	for _, split := range []string{"test", "train", ""} {
		if _, err := x.ValidatePair(a, b, 1, split); err == nil {
			t.Fatal("comparison released a non-validation split", split)
		}
	}
	if _, err := x.ValidatePair(a, b, 3, "validation"); err == nil {
		t.Fatal("undeclared mode accepted")
	}
	if _, err := x.ValidatePair(b, a, 1, "validation"); err == nil {
		t.Fatal("reversed experiment identities silently accepted")
	}
	bad := b
	bad.Experiment = a.Experiment
	if _, err := x.ValidatePair(a, bad, 1, "validation"); err == nil {
		t.Fatal("relabeled opponent accepted")
	}
	bad = b
	bad.TrainingGroups = sortedUnion(b.TrainingGroups, []string{left.Parts[0].Experiment.groups("validation")[0].Group})
	if _, err := x.ValidatePair(a, bad, 1, "validation"); err == nil {
		t.Fatal("consumed validation family accepted")
	}
	if id, _ := Digest(a); id != beforeA {
		t.Fatal("left artifact changed")
	}
	if id, _ := Digest(b); id != beforeB {
		t.Fatal("right artifact changed")
	}
}

func TestMixedValidationComparisonRejectsPartitionAndScenarioChanges(t *testing.T) {
	left := mixedExperimentFixture(t)
	for _, kind := range []string{"partition", "order", "seed", "mode-count"} {
		t.Run(kind, func(t *testing.T) {
			right := cloneMixedExperiment(t, left)
			switch kind {
			case "partition":
				families := right.Parts[0].Experiment.Families
				// Preserve the number of groups in each split, but change their roles.
				i, j := -1, -1
				for k, f := range families {
					if f.Split == "validation" {
						i = k
					}
					if f.Split == "test" {
						j = k
					}
				}
				families[i].Split, families[j].Split = families[j].Split, families[i].Split
			case "order":
				families := right.Parts[0].Experiment.Families
				families[0], families[1] = families[1], families[0]
			case "seed":
				right.Parts[0].Experiment.Seed++
			case "mode-count":
				right.Parts = right.Parts[:1]
				right.Mixture.Modes = right.Mixture.Modes[:1]
			}
			for i := range right.Parts {
				right.Parts[i].Digest, _ = Digest(right.Parts[i].Experiment)
			}
			if _, err := NewMixedValidationComparison(left, right); err == nil {
				t.Fatal("mismatched comparison accepted", kind)
			}
		})
	}
}

func TestMixedValidationComparisonPersistenceAndOwnership(t *testing.T) {
	left := mixedExperimentFixture(t)
	right := cloneMixedExperiment(t, left)
	right.Mixture.Modes[0].Weight++
	x, err := NewMixedValidationComparison(left, right)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "comparison.json")
	id, err := SaveMixedValidationComparison(path, x)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadMixedValidationComparison(path)
	if err != nil || !reflect.DeepEqual(x, loaded) {
		t.Fatal("comparison changed", err)
	}
	if got, _ := Digest(loaded); got != id {
		t.Fatal("wrong comparison identity")
	}
	left.Parts[0].Experiment.Families[0].Scenario.Builds[0][0]++
	right.Mixture.Modes[0].Weight++
	if got, _ := Digest(x); got != id {
		t.Fatal("caller mutated owned comparison")
	}
	x.LeftDigest = strings.Repeat("e", 64)
	if _, err := SaveMixedValidationComparison(path, x); err == nil {
		t.Fatal("corrupt binding accepted")
	}
	x = loaded
	x.Right.Mixture.Modes[0].Weight++
	x.RightDigest, _ = Digest(x.Right)
	if _, err := SaveMixedValidationComparison(path, x); err == nil {
		t.Fatal("frozen comparison replaced")
	}
}
