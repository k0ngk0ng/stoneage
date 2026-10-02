package aigame

import (
	"reflect"
	"strings"
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

func TestBattleRulesIsAdditiveAndDoesNotChangeCombatState(t *testing.T) {
	s := gameState{snapshot: decisionFixture()}
	s.snapshot.Connected = true
	s.applyBattleClock("BTIME|match-a|2|120000|100000|stoneage-native-ladder-v1")
	before := s.snapshot.Battle
	digest := strings.Repeat("a", 64)
	s.applySystem("BTRULES|" + digest + "|linux-arm64")
	if s.snapshot.Battle.Clock.RulesDigest != digest || s.snapshot.Battle.Clock.EnginePlatform != "linux-arm64" {
		t.Fatal("rules capability missing")
	}
	s.snapshot.Battle.Clock.RulesDigest, s.snapshot.Battle.Clock.EnginePlatform = "", ""
	if !reflect.DeepEqual(before, s.snapshot.Battle) {
		t.Fatal("rules query changed deadline or combat state")
	}
	s.applySystem("BTRULES|" + digest + "|linux-arm64")
	s.applyBattleClock("BTIME|match-a|2|120000|100000|stoneage-native-ladder-v1")
	if s.snapshot.Battle.Clock.RulesDigest != digest {
		t.Fatal("legacy BTIME reply erased rule metadata")
	}
	if _, function, e := validateActionLocked(&s, Action{Kind: ActionStatus, Command: "BTRULES"}); e != nil || function != "S" {
		t.Fatal("read-only rules query unavailable in battle", e)
	}
	for _, raw := range []string{"BTRULES||linux-arm64", "BTRULES|" + strings.Repeat("g", 64) + "|linux-amd64", "BTRULES|" + digest + "|other", "BTRULES|" + digest + "|linux-amd64|extra"} {
		s.applySystem(raw)
		if s.snapshot.Battle.Clock.RulesDigest != "" || s.snapshot.Battle.Clock.EnginePlatform != "" {
			t.Fatal("invalid/stale rules metadata retained", raw)
		}
	}
}

func TestWorldClockAdvertisesCompatibilityWithoutDeadline(t *testing.T) {
	s := gameState{snapshot: Snapshot{Phase: PhaseWorld}}
	s.applyBattleClock("BTIME||-1|0|100000|stoneage-native-ladder-v1")
	if s.snapshot.Battle.Clock.RulesVersion != "stoneage-native-ladder-v1" || s.snapshot.Battle.Clock.Known || s.snapshot.Battle.Active {
		t.Fatal("world capability response changed battle readiness", s.snapshot.Battle)
	}
}
