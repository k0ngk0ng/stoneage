package battlepolicy

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestStatusAttackVocabularyAndUnknownBoundary(t *testing.T) {
	rows := map[int32]int{0: 0, 5: 1, 10: 2}
	entities := make([][EntityFeatures]float32, 3)
	entities[2][eAlive], entities[2][ePlayer], entities[2][eHPKnown] = 1, 1, 1
	for _, tc := range []struct {
		id     int32
		column int
	}{{60, 16}, {80, 17}, {90, 18}, {110, 19}} {
		c := aigame.BattleCandidate{ID: "candidate", Actor: "pet", Kind: "skill", SkillID: tc.id, Target: 10}
		n := encodeCandidate(c, 5, 0, rows, entities)
		if !n.Supported || n.Features[0] != 1 || n.Features[15] != 1 || n.Features[tc.column] != 1 || n.Features[20] != scale(3) || n.Features[21] != -.3 || n.Target != 2 {
			t.Fatal("status semantics missing", tc, n)
		}
		c.SkillID = 61 // Similar name/effect, different strength/duration: not this contract.
		c.Name = "poison attack"
		if encodeCandidate(c, 5, 0, rows, entities).Supported {
			t.Fatal("unverified skill admitted by name")
		}
		c.SkillID = tc.id
		c.Actor = "player"
		if encodeCandidate(c, 0, 0, rows, entities).Supported {
			t.Fatal("pet skill interpreted as player magic")
		}
	}
	entities[2][eAlive] = 0
	if encodeCandidate(aigame.BattleCandidate{ID: "dead", Actor: "pet", Kind: "skill", SkillID: 110, Target: 10}, 5, 0, rows, entities).Supported {
		t.Fatal("status attack against dead target supported")
	}
}
