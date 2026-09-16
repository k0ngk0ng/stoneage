package aileveling

import "github.com/k0ngk0ng/stoneage/internal/aigame"

// World HP may not update until battle ends. Use the current native roster
// to decide whether another attack is appropriate. E is an escape attempt,
// not an acknowledgement of escape; the normal pending-turn fence remains.
func levelingBattleNeedsRecovery(s aigame.Snapshot) bool {
	for _, actor := range s.Battle.Participants {
		if actor.BattleID != s.Battle.MyNo && actor.BattleID != s.Battle.MyNo+5 {
			continue
		}
		if actor.Dead || actor.HP <= 0 || (actor.MaxHP > 0 && actor.HP < actor.MaxHP/2+actor.MaxHP%2) {
			return true
		}
	}
	return false
}
