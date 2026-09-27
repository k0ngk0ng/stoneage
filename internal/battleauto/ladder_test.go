package battleauto

import (
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

func TestLadderBattleNeverUsesOrdinaryEndCleanup(t *testing.T) {
	battle := readyBattle()
	battle.LadderID = "match_a"
	battle.Participants[0].HP = 0
	battle.Participants[0].Dead = true
	battle.Participants[1].HP = 0
	battle.Participants[1].Dead = true
	for _, ended := range []bool{false, true} {
		battle.Ended = ended
		decision, ok := Decide(aigame.Snapshot{Battle: battle}, nil, DefaultPolicy())
		if ok && decision.Action.Kind == aigame.ActionBattleEnd {
			t.Fatal("ladder submitted ordinary EO")
		}
	}
}

func TestSeekingWaitsForLadderUntilPreparationIsCancelled(t *testing.T) {
	s := worldSnapshot()
	s.Ladder = &ladder.Envelope{Snapshot: ladder.Snapshot{Phase: "lobby", Self: ladder.Player{Ready: true}}}
	seeker := &seeker{interval: time.Millisecond}
	for _, phase := range []string{"lobby", "queued", "countdown", "battle", "settling", "result"} {
		s.Ladder.Snapshot.Phase = phase
		if action, ok := seeker.next(s, time.Now()); ok {
			t.Fatalf("%s walked during ladder: %+v", phase, action)
		}
	}
	s.Ladder.Snapshot.Phase = "lobby"
	s.Ladder.Snapshot.Self.Ready = false
	if _, ok := seeker.next(s, time.Now()); !ok {
		t.Fatal("cancelled preparation still blocks seeking")
	}
}
