package aigame

import (
	"reflect"
	"strings"
	"testing"
)

const captureQuoteFixture = "BCAP|v=1|request=0123456789abcdef|active=1|battle=3|turn=2|self=0|free=4|target=10,113,1,100113,1,1810,1:1810;5:1810;7:1810"

func TestCaptureObservationPreservesAllCostsAndDoesNotChangeCombat(t *testing.T) {
	s := gameState{snapshot: Snapshot{Revision: 9, Connected: true, Phase: PhaseBattle, Battle: BattleSnapshot{Active: true, MyNo: 0, MyNoKnown: true, Turn: 2}}}
	before := s.snapshot.Battle
	s.applySystem(captureQuoteFixture)
	q := s.snapshot.Capture
	if q == nil || q.RequestID != "0123456789abcdef" || q.Revision != 10 || len(q.Targets) != 1 || len(q.Targets[0].Consumption) != 3 || q.Targets[0].Consumption[0].Slot != 1 {
		t.Fatal(q)
	}
	if !reflect.DeepEqual(before, s.snapshot.Battle) {
		t.Fatal("read-only observation changed combat")
	}
	copy := cloneSnapshot(s.snapshot)
	copy.Capture.Targets[0].Consumption[0].TemplateID = 999
	copy.Capture.Targets[0].RequiredItems[0] = 999
	if q.Targets[0].Consumption[0].TemplateID != 1810 || q.Targets[0].RequiredItems[0] != 1810 {
		t.Fatal("snapshot aliases internal quote")
	}
	s.applyBattlePacket("BA|0|3|")
	if s.snapshot.Capture != nil {
		t.Fatal("next battle packet retained old quote")
	}
	s.applySystem(captureQuoteFixture)
	if s.snapshot.Capture != nil {
		t.Fatal("old turn response accepted")
	}
	for _, command := range []string{"BCAP", "BCAP:0123456789abcdef"} {
		if _, function, err := validateActionLocked(&s, Action{Kind: ActionStatus, Command: command}); err != nil || function != "S" {
			t.Fatal(command, err)
		}
	}
}

func TestCaptureObservationRejectsMalformedAndIncompleteQuotes(t *testing.T) {
	for _, value := range []string{
		strings.Replace(captureQuoteFixture, "v=1", "v=2", 1),
		strings.Replace(captureQuoteFixture, "|free=4", "", 1),
		strings.Replace(captureQuoteFixture, "|free=4", "|free=0", 1),
		strings.Replace(captureQuoteFixture, "target=10,", "target=0,", 1),
		strings.Replace(captureQuoteFixture, ";7:1810", ";5:1810", 1),
		strings.Replace(captureQuoteFixture, "1:1810;5:1810;7:1810", "-", 1),
		strings.Replace(captureQuoteFixture, "1:1810", "20:1810", 1),
		strings.Replace(captureQuoteFixture, "5:1810", "5:999", 1),
		captureQuoteFixture + "|target=10,113,1,100113,1,-,-",
		captureQuoteFixture + "|self=0",
		"BCAP|v=1|active=0|free=4",
		"BCAP|v=1|active=0|request=bad",
	} {
		if _, ok := parseCaptureObservation(value); ok {
			t.Fatal("malformed quote accepted", value)
		}
	}
	if q, ok := parseCaptureObservation("BCAP|v=1|active=0"); !ok || q.Active {
		t.Fatal(q, ok)
	}
	for _, command := range []string{"BCAP:", "BCAP:FFFFFFFFFFFFFFFF", "BCAP:0123456789abcde", "BCAP:0123456789abcdef|1"} {
		if ValidStatusRequest(command) {
			t.Fatal(command)
		}
	}
}

func TestCaptureQuoteInvalidatedByResultsRosterAndOwnState(t *testing.T) {
	for _, event := range []Event{
		stringEvent("RS", "-2|0|1,,,,,"),
		stringEvent("RD", "-2|0|1,,,,,"),
		stringEvent("BC", "BC|0|"),
		stringEvent("S", "K0|4|25"),
		stringEvent("S", "I"),
		stringEvent("S", partyObservationBase),
		stringEvent("CharLogout", "successful"),
	} {
		s := &Session{state: newGameState(true)}
		s.state.snapshot.Phase = PhaseBattle
		s.state.snapshot.Battle = BattleSnapshot{Active: true, MyNo: 0, MyNoKnown: true, Turn: 2}
		before := s.Snapshot().Revision
		s.applyEvent(stringEvent("S", captureQuoteFixture))
		observed := s.Snapshot()
		if observed.Capture == nil || observed.Capture.Revision <= before || observed.Capture.Revision != observed.Revision {
			t.Fatal("response freshness did not advance with actual packet", observed.Capture)
		}
		s.applyEvent(event)
		if s.Snapshot().Capture != nil {
			t.Fatal("retained stale quote", event.Function)
		}
		if event.Function == "RS" || event.Function == "RD" || event.Function == "CharLogout" {
			s.applyEvent(stringEvent("S", captureQuoteFixture))
			if s.Snapshot().Capture != nil {
				t.Fatal("late quote revived after terminal state", event.Function)
			}
		}
	}
}
