package aigame

import (
	"reflect"
	"testing"
)

func TestBattleClockObservationDoesNotChangeCombatState(t *testing.T) {
	s := gameState{snapshot: decisionFixture()}
	before := s.snapshot.Battle
	s.applyBattleClock("BTIME|match-a|2|120000|100000")
	clock := s.snapshot.Battle.Clock
	if !clock.Known || clock.ServerTurn != 2 || clock.DeadlineMS != 120000 || clock.ServerNowMS != 100000 {
		t.Fatal(clock)
	}
	s.snapshot.Battle.Clock = before.Clock
	if !reflect.DeepEqual(before, s.snapshot.Battle) {
		t.Fatal("clock changed game state")
	}
	for _, raw := range []string{"BTIME|other|2|120000|100000", "BTIME|match-a|2|0|100000", "BTIME|match-a|-1|120000|100000"} {
		s.applyBattleClock(raw)
		if s.snapshot.Battle.Clock.Known {
			t.Fatal("invalid clock accepted", raw)
		}
	}
	s.snapshot.Connected = true
	_, function, err := validateActionLocked(&s, Action{Kind: ActionStatus, Command: "BTIME"})
	if err != nil || function != "S" {
		t.Fatal(function, err)
	}
	if _, _, err := validateActionLocked(&s, Action{Kind: ActionStatus, Command: "P"}); err == nil {
		t.Fatal("unrelated status query admitted during combat")
	}
}

func TestWorldClockAdvertisesCompatibilityWithoutDeadline(t *testing.T) {
	s := gameState{snapshot: Snapshot{Phase: PhaseWorld}}
	s.applyBattleClock("BTIME||-1|0|100000|stoneage-native-ladder-v1")
	if s.snapshot.Battle.Clock.RulesVersion != "stoneage-native-ladder-v1" || s.snapshot.Battle.Clock.Known || s.snapshot.Battle.Active {
		t.Fatal("world capability response changed battle readiness", s.snapshot.Battle)
	}
}
