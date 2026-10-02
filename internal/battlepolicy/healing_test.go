package battlepolicy

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"testing"
)

func TestHealingSemanticsAndGroupTargets(t *testing.T) {
	rows := map[int32]int{0: 0, 5: 1, 10: 2}
	e := make([][EntityFeatures]float32, 3)
	for i := range e {
		e[i][eAlive], e[i][eHPKnown], e[i][eHPRatio] = 1, 1, .5
	}
	e[0][eAlly], e[1][eAlly] = 1, 1
	c := aigame.BattleCandidate{ID: "heal", Actor: "player", Kind: "magic", MagicIDKnown: true, MagicID: 10, MPCost: 8, Target: 5}
	x := encodeCandidate(c, 0, 0, rows, e)
	if !x.Supported || x.Target != 1 || x.Features[22] != 1 || x.Features[4] != 0 || x.Features[23] != scale(65) || x.Features[24] != scale(8) {
		t.Fatal(x)
	}
	c.MagicID, c.MPCost, c.Target = 20, 20, 20
	x = encodeCandidate(c, 0, 0, rows, e)
	if !x.Supported || x.Target != -1 || x.Features[7] != 1 || x.Features[25] != 1 || x.Features[26] != .2 || x.Features[27] != .5 {
		t.Fatal(x)
	}
	flipped := c
	flipped.Target = 21
	if other := encodeCandidate(flipped, 10, 1, map[int32]int{10: 0, 15: 1, 0: 2}, e); other != x {
		t.Fatal("group healing features changed on side swap", other, x)
	}
	e[1][eHPKnown], e[1][eHPRatio] = 0, 0
	partial := encodeCandidate(c, 0, 0, rows, e)
	if partial.Features[27] != .5 || partial.Features[28] != .5 {
		t.Fatal("unknown HP treated as real zero", partial)
	}
	e[0][eHPKnown] = 0
	unknown := encodeCandidate(c, 0, 0, rows, e)
	if !unknown.Supported || unknown.Features[27] != 0 || unknown.Features[28] != 0 {
		t.Fatal("fully unknown group not represented explicitly", unknown)
	}
	c.Target = 21
	if enemy := encodeCandidate(c, 0, 0, rows, e); !enemy.Supported || enemy.Features[8] != 1 || enemy.Features[26] != .1 {
		t.Fatal(enemy)
	}
	for _, mutate := range []func(*aigame.BattleCandidate){
		func(c *aigame.BattleCandidate) { c.MagicIDKnown = false },
		func(c *aigame.BattleCandidate) { c.MagicID = 11 },
		func(c *aigame.BattleCandidate) { c.Actor = "pet" },
		func(c *aigame.BattleCandidate) { c.Target = 22 },
		func(c *aigame.BattleCandidate) { c.MPCost = -1 },
	} {
		bad := c
		mutate(&bad)
		if encodeCandidate(bad, 0, 0, rows, e).Supported {
			t.Fatal("unverified heal admitted", bad)
		}
	}
	e[2][eAlive] = 0
	if encodeCandidate(c, 0, 0, rows, e).Supported {
		t.Fatal("dead-only group healed")
	}
}
