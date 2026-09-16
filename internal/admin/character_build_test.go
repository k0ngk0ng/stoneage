package admin

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestAdminCharacterBuildValidation(t *testing.T) {
	for _, build := range []*airuntime.CharacterBuild{
		{Weights: airuntime.AttributeWeights{}},
		{Weights: airuntime.AttributeWeights{Vital: -1}},
		{Weights: airuntime.AttributeWeights{Strength: 1}, ReservePoints: -1},
	} {
		goal := airuntime.Goal{CharacterBuild: build}
		if err := validateAIProfilePatch(airuntime.ProfilePatch{Goal: &goal}); err == nil {
			t.Fatal("patch accepted invalid character build")
		}
		if _, err := normalizeAIProfileCreate(aiProfileCreateRequest{Account: airuntime.AccountIdentity{ID: "a"}, Character: airuntime.CharacterIdentity{ID: "c"}, Goal: goal}); err == nil {
			t.Fatal("create accepted invalid character build")
		}
		if _, err := normalizeAIPlayerProvision(aiPlayerProvisionRequest{CharacterName: "hero", SkillNames: []string{"stoneage-play"}, Goal: goal}); err == nil {
			t.Fatal("provision accepted invalid character build")
		}
	}
	goal := airuntime.Goal{CharacterBuild: &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Strength: 1, Dexterity: 1}, ReservePoints: 2}}
	if err := validateAIProfilePatch(airuntime.ProfilePatch{Goal: &goal}); err != nil {
		t.Fatal(err)
	}
	provision, err := normalizeAIPlayerProvision(aiPlayerProvisionRequest{CharacterName: "hero", SkillNames: []string{"stoneage-play"}, Goal: goal})
	if err != nil || provision.Goal.CharacterBuild == nil || *provision.Goal.CharacterBuild != *goal.CharacterBuild {
		t.Fatalf("provision lost configured build: %+v %v", provision, err)
	}
}
