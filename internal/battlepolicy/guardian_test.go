package battlepolicy

import (
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestGuardianCandidateAndOwnedSkillVersioning(t *testing.T) {
	if ValidateFeatureEnvironment(FeatureVersion, "controlled-battle-v7") == nil || ValidateFeatureEnvironment(FeatureVersion, "controlled-battle-v8") != nil {
		t.Fatal("new guardian vocabulary not tied to audited native environment")
	}
	for _, side := range []int{0, 1} {
		team := fixture(1, side)
		actor, target := int32(side*10+5), int32((1-side)*10)
		team[0].Pets[0].Skills = append(team[0].Pets[0].Skills, aigame.PetSkillSnapshot{Index: 3, ID: 20, Field: 1, Target: 7})
		team[0].Candidates = append(team[0].Candidates, aigame.BattleCandidate{ID: "guardian", Actor: "pet", Kind: "skill", SkillID: 20, Target: target})
		for _, version := range []string{LegacyFeatureVersion, RecipientFeatureVersion, FeatureVersion} {
			frame, err := EncodeVersion(team, History{First: true}, version)
			if err != nil {
				t.Fatal(err)
			}
			if err = frame.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, slot := range frame.Slots {
				if slot.Actor != "pet" {
					continue
				}
				for _, candidate := range slot.Candidates {
					if candidate.ID != "guardian" {
						continue
					}
					if version != FeatureVersion {
						if candidate.Supported || candidate.Features[32] != 0 || frame.Entities[slot.Entity][eGuardianSkill] != 0 || frame.Actions != LegacyActionVersion {
							t.Fatal("legacy weights acquired new skill semantics", version)
						}
						continue
					}
					owner := frame.Entities[frame.Members[0]]
					if !candidate.Supported || candidate.Features[0] != 1 || candidate.Features[32] != 1 || candidate.Features[21] != -.2 || candidate.Features[33] != owner[eHPRatio] || candidate.Features[34] != owner[eMaxHP] || candidate.Features[35] != owner[eHPKnown] || candidate.Features[8] != 1 || frame.Entities[slot.Entity][eGuardianSkill] != 1 {
						t.Fatal("guardian is an attack with own-owner benefit", actor, candidate)
					}
					if candidate.Target == frame.Members[0] {
						t.Fatal("protected owner substituted for attack target")
					}
				}
			}
			frame.Actions = "wrong-action-contract"
			if frame.Validate() == nil {
				t.Fatal("mismatched action contract accepted")
			}
		}
		// v7 and v8 keep identical recipient/guardian history meanings.
		batch := aigame.BattleEventBatch{Stream: "guardian", Cursor: 1, Observation: team[0], Events: []aigame.BattleEvent{{Sequence: 1, MatchID: team[0].MatchID, Effects: []aigame.BattleLogEntry{{Kind: "attack", Actor: int(actor), Target: int(target), Flags: 512, Recipient: new(int), Guardian: new(int), Damage: 7}}}}}
		*batch.Events[0].Effects[0].Recipient, *batch.Events[0].Effects[0].Guardian = int(target+5), int(target+5)
		h := History{Batch: &batch, First: true}
		a, x, err := EncodeHistorySequenceVersion(team[0], h, RecipientFeatureVersion)
		b, y, err2 := EncodeHistorySequenceVersion(team[0], h, FeatureVersion)
		if err != nil || err2 != nil || a != b || !reflect.DeepEqual(x, y) {
			t.Fatal("v8 changed the existing v7 history encoding", err, err2)
		}
	}
}
