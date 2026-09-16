package admin

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestAdminLifePolicyValidation(t *testing.T) {
	goal := airuntime.Goal{Kind: "life", Life: &airuntime.LifePolicy{DecisionIntervalSeconds: 59}}
	if err := validateAIProfilePatch(airuntime.ProfilePatch{Goal: &goal}); err == nil {
		t.Fatal("invalid life policy accepted by patch")
	}
	if _, err := normalizeAIProfileCreate(aiProfileCreateRequest{Account: airuntime.AccountIdentity{ID: "a"}, Character: airuntime.CharacterIdentity{ID: "c"}, Goal: goal}); err == nil {
		t.Fatal("invalid life policy accepted by create")
	}
	if _, err := normalizeAIPlayerProvision(aiPlayerProvisionRequest{CharacterName: "hero", SkillNames: []string{"stoneage-play"}, Goal: goal}); err == nil {
		t.Fatal("invalid life policy accepted by provision")
	}
	goal.Life.DecisionIntervalSeconds = 900
	provision, err := normalizeAIPlayerProvision(aiPlayerProvisionRequest{CharacterName: "hero", SkillNames: []string{"stoneage-play"}, Goal: goal})
	if err != nil || provision.Goal.Life.DecisionIntervalSeconds != 900 {
		t.Fatalf("life policy lost: %+v err=%v", provision, err)
	}
}
