package aigame

import "testing"

func TestRawBattleEndSharesTypedExitGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		battle BattleSnapshot
		exits  bool
	}{
		{"unfinished", BattleSnapshot{Active: true}, false},
		{"concluded", BattleSnapshot{Active: true, Ended: true, Result: "victory"}, true},
		{"arena", BattleSnapshot{Active: true, Ended: true, Result: "victory", LadderID: "match"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := worldTestSession(t)
			s.stateMu.Lock()
			s.state.snapshot.Phase = PhaseBattle
			s.state.snapshot.Battle = tc.battle
			s.stateMu.Unlock()
			before := s.Snapshot()
			packet, err := s.buildPacket("EO", []wireValue{{kind: wireInt, integer: 0}})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyClientPacket(packet); err != nil {
				t.Fatal(err)
			}
			after := s.Snapshot()
			if tc.exits {
				if after.Phase != PhaseWorld || after.Battle.Active || after.Battle.LastCommand != "EO" {
					t.Fatal("raw EO did not finish a concluded battle", after.Battle)
				}
			} else if after.Phase != PhaseBattle || !after.Battle.Active {
				t.Fatal("raw EO bypassed battle exit guard")
			}
			if after.Revision != before.Revision {
				t.Fatal("outgoing acknowledgement invented server revision")
			}
		})
	}
}
func TestMalformedRawBattleEndCannotClearBattle(t *testing.T) {
	s, _ := worldTestSession(t)
	s.stateMu.Lock()
	s.state.snapshot.Phase = PhaseBattle
	s.state.snapshot.Battle = BattleSnapshot{Active: true, Ended: true, Result: "victory"}
	s.stateMu.Unlock()
	packet, err := s.buildPacket("EO", []wireValue{{kind: wireInt, integer: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyClientPacket(packet); err == nil {
		t.Fatal("malformed exit accepted")
	}
	if !s.Snapshot().Battle.Active {
		t.Fatal("malformed exit changed battle")
	}
}
