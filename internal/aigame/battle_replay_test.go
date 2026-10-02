package aigame

import (
	"reflect"
	"testing"
)

func TestBattleReplayUsesLiveProjection(t *testing.T) {
	r, e := NewBattleReplay("offline-fixture", 1)
	if e != nil {
		t.Fatal(e)
	}
	live := &Session{state: newGameState(false)}
	live.state.snapshot.Phase = PhaseBattle
	live.state.snapshot.Battle = BattleSnapshot{Active: true, LadderID: "offline-fixture"}
	for _, p := range []struct{ f, s string }{
		{"S", "P1|50|100|10|20|30|30|30|30|0|10|35|50|30|40|100|0|100|0|0|0|0|0|0|0|-1|0|100000|name||"},
		{"B", "BP|0|0|A"},
		{"B", "BC|0|0|self||186A0|23|32|64|1|0||0|0|0|A|enemy||186A0|23|40|64|1|0||0|0|0|"},
	} {
		if e = r.Apply(p.f, []byte(p.s)); e != nil {
			t.Fatal(e)
		}
		live.applyEvent(stringEvent(p.f, p.s))
	}
	s := live.Snapshot()
	s.Battle.Turn = 4
	a, b := r.View(4), NewBattleView(s)
	if !reflect.DeepEqual(a.Battle, b.Battle) || !reflect.DeepEqual(a.Own, b.Own) || !reflect.DeepEqual(a.Candidates, b.Candidates) {
		t.Fatal("offline parser diverged from live parser")
	}
	if a.ID != r.View(4).ID {
		t.Fatal("reading replay advanced the observation")
	}
	for _, c := range a.Candidates {
		if c.Kind != "attack" {
			continue
		}
		action, e := r.Resolve(4, BattleSelection{MatchID: a.MatchID, Turn: 4, ObservationID: a.ID, CandidateID: c.ID})
		if e != nil || action.Command == "" {
			t.Fatalf("shared candidate resolution failed: %+v %v", action, e)
		}
	}
	if r.Apply("Chat", []byte("private")) == nil {
		t.Fatal("nonbattle packet accepted")
	}
}

func TestArenaWithdrawnMemberReceivesObservationsWithoutActions(t *testing.T) {
	r, err := NewBattleReplay("arena", 2)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(packet string) {
		t.Helper()
		if err := r.Apply("B", []byte(packet)); err != nil {
			t.Fatal(err)
		}
	}
	self := "0|self||186A0|23|64|64|4|0||0|0|0|"
	others := "1|ally||186A0|23|64|64|4|0||0|0|0|A|enemy||186A0|23|64|64|4|0||0|0|0|"
	apply("BP|0|8|64")
	apply("BC|0|" + self + others)
	before := r.View(0)
	if before.Withdrawn || len(before.Candidates) == 0 {
		t.Fatal("initial member not playable")
	}
	apply("BP|0|A|64")
	if r.View(1).Battle.CommandReady || len(r.View(1).Candidates) != 0 {
		t.Fatal("BP reused a stale roster")
	}
	apply("BC|0|" + others)
	apply("BA|0|1|")
	v := r.View(1)
	if !v.Withdrawn || !v.Battle.Active || v.Battle.Ended || v.Battle.PlayerCommandReady() || v.Battle.PetCommandReady() || len(v.Candidates) != 0 {
		t.Fatalf("withdrawal did not become observation-only: %+v", v)
	}
	projected := r.session.state.ladderPacketView(stringEvent("B", "BA|0|1|"), nil)
	if projected == nil || projected.Commands == nil || !projected.Commands.Withdrawn {
		t.Fatal("Web projection omitted withdrawal")
	}
	if _, err := r.Resolve(1, BattleSelection{MatchID: before.MatchID, Turn: before.Turn, ObservationID: before.ID, CandidateID: before.Candidates[0].ID}); err == nil {
		t.Fatal("withdrawn member accepted an old action")
	}
	apply("BH|a1|rA|f2|d1|p0|FF|")
	apply("BP|0|A|64")
	apply("BC|0|" + others)
	apply("BA|0|2|")
	if next := r.View(2); !next.Withdrawn || next.Battle.Movie || len(next.Battle.Participants) != 2 {
		t.Fatal("spectator did not receive the next public battle boundary")
	}
	// A torn or duplicate roster cannot prove a change of membership.
	apply("BP|0|0|64")
	for _, packet := range []string{"BC|0|1|ally||186A0|23|64|64|4|0|", "BC|0|" + self + self} {
		apply(packet)
		if next := r.View(3); next.Battle.BCReceived || next.Battle.CommandReady || len(next.Candidates) != 0 {
			t.Fatal("malformed roster reopened commands")
		}
	}
}

func TestCompleteCombatStatsPresence(t *testing.T) {
	s := newGameState(false)
	applyEventLocked(&s, stringEvent("S", "P1|1|2"))
	if s.snapshot.Player.CombatStatsKnown {
		t.Fatal("short P1 advertised unknown attributes as zero")
	}
	full := "P1|50|100|10|20|30|30|30|30|0|10|35|50|30|40|100|0|100|0|0|0|0|0|0|0|-1|0|100000|name||"
	applyEventLocked(&s, stringEvent("S", full))
	if !s.snapshot.Player.CombatStatsKnown {
		t.Fatal("complete P1 missing presence marker")
	}
	applyEventLocked(&s, stringEvent("S", "P2|0"))
	if !s.snapshot.Player.CombatStatsKnown || s.snapshot.Player.HP != 0 {
		t.Fatal("valid zero masked value lost known status")
	}
	applyEventLocked(&s, stringEvent("S", "P2|bad"))
	if s.snapshot.Player.CombatStatsKnown {
		t.Fatal("malformed masked player value remained known")
	}
	applyEventLocked(&s, stringEvent("S", full))
	applyEventLocked(&s, stringEvent("CharLogin", "successful"))
	if s.snapshot.Player.CombatStatsKnown {
		t.Fatal("previous character combat attributes survived login")
	}
	applyEventLocked(&s, stringEvent("S", "K0|1|100266|30|30|10|10|0|1|1|10|10|10"))
	pet, _ := s.petForSlot(0)
	if !pet.CombatStatsKnown {
		t.Fatal("complete K combat prefix not recognized")
	}
	applyEventLocked(&s, stringEvent("S", "K0|4|0"))
	pet, _ = s.petForSlot(0)
	if !pet.CombatStatsKnown || pet.HP != 0 {
		t.Fatal("valid masked pet value lost known status")
	}
	applyEventLocked(&s, stringEvent("S", "K0|4"))
	pet, _ = s.petForSlot(0)
	if pet.CombatStatsKnown {
		t.Fatal("missing masked pet value remained known")
	}
	applyEventLocked(&s, stringEvent("S", "K0|1|100266|30|30|10|10|0|1|1|10|10|10"))
	applyEventLocked(&s, stringEvent("S", "K0|1|100266"))
	pet, _ = s.petForSlot(0)
	if pet.CombatStatsKnown {
		t.Fatal("replacement inherited previous pet attributes")
	}
}
