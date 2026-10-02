package aigame

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

func TestLadderReplySurvivesFollowingEventAndDoesNotRewind(t *testing.T) {
	s := newGameState(true)
	s.applyLadder(`LADDER|{"version":1,"request_id":"req","request_wire":"LADDER|1|req|0|status|","ok":true,"revision":10,"snapshot":{"phase":"lobby"}}`)
	s.applyLadder(`LADDER|{"version":1,"ok":true,"revision":11,"snapshot":{"phase":"queued"}}`)
	s.applyLadder(`LADDER|{"version":1,"request_id":"old","ok":true,"revision":9,"snapshot":{"phase":"idle"}}`)
	if s.snapshot.Ladder.Revision != 11 || s.snapshot.Ladder.Snapshot.Phase != "queued" {
		t.Fatal("late receipt rewound state")
	}
	session := &Session{state: s}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := session.WaitLadderReply(ctx, ladder.Request{ID: "req", Operation: "status"})
	if err != nil || e.Revision != 10 {
		t.Fatalf("receipt=%+v err=%v", e, err)
	}
}

func ladderStateEvent(sequence int, phase string) string {
	return fmt.Sprintf(`LADDER|{"version":1,"ok":true,"revision":%d,"sequence":%d,"snapshot":{"phase":%q}}`, sequence, sequence, phase)
}

func TestLadderJournalRecoversGapsAndDoesNotInferLossFromGlobalRevisions(t *testing.T) {
	s := &Session{state: newGameState(true)}
	s.state.applyLadder(ladderStateEvent(100, "lobby"))
	first := s.LadderEvents("", 0)
	if first.Cursor != 100 || first.Stream == "" || first.Gap || len(first.Events) != 1 {
		t.Fatalf("initial: %+v", first)
	}
	s.state.applyLadder(ladderStateEvent(109, "queued")) // other players advanced the global revision
	s.state.applyLadder(ladderStateEvent(109, "queued")) // duplicate status
	s.state.applyLadder(ladderStateEvent(99, "idle"))    // late receipt
	next := s.LadderEvents(first.Stream, first.Cursor)
	if next.Gap || len(next.Events) != 1 || next.Cursor != 109 || next.Snapshot.Snapshot.Phase != "queued" {
		t.Fatalf("replay: %+v", next)
	}
	for i := 110; i < 250; i++ {
		s.state.applyLadder(ladderStateEvent(i, "battle"))
	}
	gap := s.LadderEvents(first.Stream, first.Cursor)
	if !gap.Gap || len(gap.Events) != 0 || gap.Snapshot.Revision != 249 {
		t.Fatalf("truncated replay: %+v", gap)
	}
	last := s.LadderEvents(first.Stream, 248)
	if last.Gap || len(last.Events) != 1 || last.Events[0].Sequence != 249 {
		t.Fatalf("available tail: %+v", last)
	}
	last.Snapshot.Snapshot.Phase = "corrupted"
	last.Events[0].Snapshot.Phase = "corrupted"
	if s.LadderEvents(first.Stream, 248).Snapshot.Snapshot.Phase != "battle" {
		t.Fatal("returned snapshot aliases mutable state")
	}
	other := &Session{state: newGameState(true)}
	other.state.applyLadder(ladderStateEvent(100, "result"))
	if got := other.LadderEvents(first.Stream, first.Cursor); !got.Gap || got.Snapshot.Snapshot.Phase != "result" {
		t.Fatalf("new connection with same numeric cursor: %+v", got)
	}
}

func TestLadderWaitTimeoutKeepsSnapshotAndCancellationIsAnError(t *testing.T) {
	left, right := net.Pipe()
	s := NewSession(left, Config{})
	defer s.Close()
	defer right.Close()
	s.state.applyLadder(ladderStateEvent(10, "queued"))
	first := s.LadderEvents("", 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	batch, err := s.WaitLadderEvents(ctx, first.Stream, first.Cursor)
	if err != nil || !batch.TimedOut || batch.Snapshot == nil || batch.Snapshot.Snapshot.Phase != "queued" {
		t.Fatalf("timeout lost snapshot: %+v %v", batch, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	batch, err = s.WaitLadderEvents(ctx, first.Stream, first.Cursor)
	if !errors.Is(err, context.Canceled) || batch.TimedOut {
		t.Fatalf("cancellation became success: %+v %v", batch, err)
	}
}

func TestHistoricalLadderResultDoesNotReplaceCurrentMatch(t *testing.T) {
	s := newGameState(true)
	s.applyLadder(ladderStateEvent(10, "battle"))
	s.applyLadder(`LADDER|{"version":1,"request_id":"history","event":"result_lookup","revision":10,"sequence":10,"snapshot":{"phase":"battle","result":{"id":"previous"}}}`)
	if s.snapshot.Ladder.Snapshot.Result != nil || len(s.ladderReplies) != 1 {
		t.Fatal("historical result replaced live projection or lost its receipt")
	}
}

func TestLadderContactsReceiptDoesNotEraseCurrentBattle(t *testing.T) {
	s := newGameState(true)
	s.applyLadder(ladderStateEvent(10, "battle"))
	s.applyLadder(`LADDER|{"version":1,"request_id":"cards","event":"contacts_lookup","revision":10,"sequence":0,"snapshot":{"self":{"id":"self"}},"contacts":[{"slot":2,"id":"selected"}]}`)
	if s.snapshot.Ladder.Snapshot.Phase != "battle" || len(s.ladderEvents) != 1 || len(s.ladderReplies) != 1 || s.ladderReplies[0].Contacts[0].ID != "selected" {
		t.Fatal("directory read replaced gameplay state or lost its receipt")
	}
}

func TestLadderReceiptMustMatchTheWholeRequest(t *testing.T) {
	left, right := net.Pipe()
	s := NewSession(left, Config{})
	defer s.Close()
	defer right.Close()
	s.state.applyLadder(`LADDER|{"version":1,"request_id":"same","request_wire":"LADDER|1|same|10|create|1","ok":true,"revision":11,"snapshot":{"phase":"lobby"}}`)
	request := ladder.Request{ID: "same", Revision: 10, Operation: "create", Argument: "2"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := s.WaitLadderReply(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cached success for a different request was returned: %v", err)
	}
	s.state.applyLadder(`LADDER|{"version":1,"request_id":"same","request_wire":"LADDER|1|same|10|create|2","ok":false,"code":"request_conflict","revision":11,"snapshot":{"phase":"lobby"}}`)
	reply, err := s.WaitLadderReply(context.Background(), request)
	if err != nil || reply.OK || reply.Code != "request_conflict" {
		t.Fatalf("%+v %v", reply, err)
	}
}

func TestLadderBattleKeepsIdentityThroughEndAndRejectsOrdinaryCleanup(t *testing.T) {
	s := newGameState(true)
	s.snapshot.Phase = PhaseWorld
	s.applyLadder(`LADDER|{"version":1,"revision":10,"sequence":10,"snapshot":{"phase":"countdown","match":{"id":"match_a"}}}`)
	s.beginBattle(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	if s.snapshot.Battle.LadderID != "match_a" {
		t.Fatal("EN did not bind ladder match")
	}
	s.endBattle()
	if _, _, err := validateActionLocked(&s, EndBattle()); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("ordinary EO after ladder BU: %v", err)
	}
	s.applyLadder(ladderStateEvent(11, "lobby"))
	s.beginBattle(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	if s.snapshot.Battle.LadderID != "" {
		t.Fatal("next normal battle inherited ladder identity")
	}
}

func TestLadderReconnectRestoresAcceptedCommands(t *testing.T) {
	s, peer := battleControlSession(t, "2", true)
	s.applyEvent(stringEvent("S", `LADDER|{"version":1,"revision":10,"sequence":10,"snapshot":{"phase":"battle","match":{"id":"same_match"}}}`))
	s.applyEvent(stringEvent("B", "BA|1|0|"))
	b := s.Snapshot().Battle
	if !b.PlayerSubmitted || b.PetSubmitted || b.PlayerCommandReady() || !b.PetCommandReady() {
		t.Fatalf("accepted player command not restored: %+v", b)
	}
	if err := s.Execute(context.Background(), Battle("N")); !errors.Is(err, ErrBattleNotReady) {
		t.Fatalf("resubmission after reconnect: %v", err)
	}
	writeBattleTestAction(t, s, peer, "W|FF|FF")
	// An older mask cannot erase this connection's submitted pet action.
	s.applyEvent(stringEvent("B", "BA|1|0|"))
	if b := s.Snapshot().Battle; !b.PetSubmitted || b.CommandReady {
		t.Fatalf("acknowledgement reopened submitted commands: %+v", b)
	}
	s.applyEvent(stringEvent("B", "BP|0|0|64"))
	if s.Snapshot().Battle.PlayerCommandReady() {
		t.Fatal("new BP reused the previous arena roster")
	}
	s.applyEvent(stringEvent("B", "BC|0|0|self||186A0|23|64|64|4|0||0|0|0|A|enemy||186A0|23|64|64|4|0||0|0|0|"))
	s.applyEvent(stringEvent("B", "BA|0|1|"))
	if b := s.Snapshot().Battle; b.PlayerSubmitted || b.PetSubmitted || !b.PlayerCommandReady() {
		t.Fatalf("next round did not reopen commands: %+v", b)
	}
	// The other side uses absolute native slots, not side-relative bits.
	s.applyEvent(stringEvent("B", "BP|A|A|64"))
	s.applyEvent(stringEvent("B", "BA|8400|1|"))
	if b := s.Snapshot().Battle; !b.PlayerSubmitted || !b.PetSubmitted || b.CommandReady {
		t.Fatalf("opposite side's accepted commands not restored: %+v", b)
	}
}

func TestLadderActionAvailableInBattleButNotBeforeLogin(t *testing.T) {
	s := newGameState(true)
	a := Ladder(ladder.Request{ID: "status", Operation: "status"})
	if _, _, err := validateActionLocked(&s, a); err == nil {
		t.Fatal("accepted before entering character")
	}
	s.snapshot.Phase = PhaseBattle
	values, function, err := validateActionLocked(&s, a)
	if err != nil || function != "S" || string(values[0].text) != "LADDER|1|status|0|status|" {
		t.Fatalf("%v %s %v", values, function, err)
	}
}
