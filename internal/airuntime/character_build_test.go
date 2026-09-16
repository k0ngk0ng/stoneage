package airuntime

import (
	"context"
	"math"
	"testing"
)

func TestCharacterBuildValidate(t *testing.T) {
	var nilBuild *CharacterBuild
	if err := nilBuild.Validate(); err != nil {
		t.Fatalf("nil build should be valid (disabled): %v", err)
	}

	tests := []struct {
		name  string
		build *CharacterBuild
		valid bool
	}{
		{
			name:  "valid boundaries",
			build: &CharacterBuild{Weights: AttributeWeights{Vital: 0, Strength: 100, Toughness: 0, Dexterity: 0}, ReservePoints: 1000},
			valid: true,
		},
		{
			name:  "all weights zero",
			build: &CharacterBuild{ReservePoints: 0},
		},
		{
			name:  "negative weight",
			build: &CharacterBuild{Weights: AttributeWeights{Vital: -1}},
		},
		{
			name:  "weight over 100",
			build: &CharacterBuild{Weights: AttributeWeights{Strength: 101}},
		},
		{
			name:  "negative reserve",
			build: &CharacterBuild{Weights: AttributeWeights{Vital: 1}, ReservePoints: -1},
		},
		{
			name:  "reserve over range",
			build: &CharacterBuild{Weights: AttributeWeights{Vital: 1}, ReservePoints: 1001},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.build.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() accepted invalid build")
			}
		})
	}
}

func TestCharacterBuildNextAttributeChoosesLowestRatio(t *testing.T) {
	build := &CharacterBuild{Weights: AttributeWeights{Vital: 50, Strength: 100, Toughness: 25, Dexterity: 0}}
	index, ok := build.NextAttribute([4]int32{100, 120, 100, 1}, 1, true)
	if !ok || index != 1 {
		t.Fatalf("NextAttribute() = (%d, %t), want (1, true)", index, ok)
	}
}

func TestCharacterBuildNextAttributeTieUsesNativeOrder(t *testing.T) {
	build := &CharacterBuild{Weights: AttributeWeights{Vital: 10, Strength: 10, Toughness: 10, Dexterity: 10}}
	index, ok := build.NextAttribute([4]int32{100, 100, 100, 100}, 1, true)
	if !ok || index != 0 {
		t.Fatalf("NextAttribute() = (%d, %t), want native index 0 on tie", index, ok)
	}
}

func TestCharacterBuildNextAttributeSkipsZeroWeights(t *testing.T) {
	build := &CharacterBuild{Weights: AttributeWeights{Vital: 0, Strength: 1, Toughness: 0, Dexterity: 1}}
	index, ok := build.NextAttribute([4]int32{1, 100, 1, 100}, 1, true)
	if !ok || index != 1 {
		t.Fatalf("NextAttribute() = (%d, %t), want weighted index 1", index, ok)
	}
}

func TestCharacterBuildNextAttributeRejectsUnusableState(t *testing.T) {
	build := &CharacterBuild{Weights: AttributeWeights{Vital: 1}, ReservePoints: 2}
	tests := []struct {
		name       string
		attributes [4]int32
		points     int32
		known      bool
	}{
		{name: "unknown points", attributes: [4]int32{100, 100, 100, 100}, points: 3},
		{name: "at reserve", attributes: [4]int32{100, 100, 100, 100}, points: 2, known: true},
		{name: "below reserve", attributes: [4]int32{100, 100, 100, 100}, points: 1, known: true},
		{name: "missing attribute", attributes: [4]int32{100, 0, 100, 100}, points: 3, known: true},
		{name: "negative attribute", attributes: [4]int32{100, -1, 100, 100}, points: 3, known: true},
	}
	invalid := &CharacterBuild{Weights: AttributeWeights{Vital: -1}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index, ok := build.NextAttribute(test.attributes, test.points, test.known)
			if ok {
				t.Fatalf("NextAttribute() = (%d, true), want rejection", index)
			}
		})
	}
	t.Run("invalid build", func(t *testing.T) {
		index, ok := invalid.NextAttribute([4]int32{100, 100, 100, 100}, 3, true)
		if ok {
			t.Fatalf("NextAttribute() = (%d, true), want invalid build rejection", index)
		}
	})
}

func TestCharacterBuildNextAttributeUsesWideRatioArithmetic(t *testing.T) {
	build := &CharacterBuild{Weights: AttributeWeights{Vital: 99, Strength: 100}}
	attributes := [4]int32{math.MaxInt32, math.MaxInt32 - 1, 1, 1}
	index, ok := build.NextAttribute(attributes, 1, true)
	if !ok || index != 1 {
		t.Fatalf("NextAttribute() = (%d, %t), want index 1 without integer overflow", index, ok)
	}
}

func TestCharacterBuildProfilePersistenceAndCloneIsolation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	input := testProfile()
	input.ID = "character-build-profile"
	input.Goal.CharacterBuild = &CharacterBuild{
		Weights:       AttributeWeights{Vital: 10, Strength: 60, Toughness: 20, Dexterity: 10},
		ReservePoints: 7,
	}
	created, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Goal.CharacterBuild == nil || created.Goal.CharacterBuild.Weights != input.Goal.CharacterBuild.Weights || created.Goal.CharacterBuild.ReservePoints != 7 {
		t.Fatalf("created character build = %#v", created.Goal.CharacterBuild)
	}

	created.Goal.CharacterBuild.Weights.Strength = 1
	loaded, err := store.GetProfile(ctx, input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.CharacterBuild == nil || loaded.Goal.CharacterBuild.Weights.Strength != 60 {
		t.Fatalf("create/get clone isolation failed: %#v", loaded.Goal.CharacterBuild)
	}

	updatedGoal := loaded.Goal
	updatedGoal.CharacterBuild = &CharacterBuild{
		Weights:       AttributeWeights{Vital: 40, Strength: 30, Toughness: 20, Dexterity: 10},
		ReservePoints: 11,
	}
	updated, err := store.UpdateProfileCAS(ctx, input.ID, created.Version, ProfilePatch{Goal: &updatedGoal, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != created.Version+1 || updated.Goal.CharacterBuild == nil || updated.Goal.CharacterBuild.Weights.Vital != 40 || updated.Goal.CharacterBuild.ReservePoints != 11 {
		t.Fatalf("updated character build = %#v, version=%d", updated.Goal.CharacterBuild, updated.Version)
	}

	updated.Goal.CharacterBuild.Weights.Vital = 99
	readBack, err := store.GetProfile(ctx, input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readBack.Goal.CharacterBuild == nil || readBack.Goal.CharacterBuild.Weights.Vital != 40 || readBack.Goal.CharacterBuild.ReservePoints != 11 {
		t.Fatalf("update/get clone isolation failed: %#v", readBack.Goal.CharacterBuild)
	}
}
