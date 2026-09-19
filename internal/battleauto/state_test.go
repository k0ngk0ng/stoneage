package battleauto

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func fightingSnapshot() aigame.Snapshot {
	battle := readyBattle()
	battle.Turn = 3
	return aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: battle}
}

// A wiped party is the loss the loop has to notice while the battle is still
// live: once EO has been sent the snapshot stops saying whose side is down.
func wipedSnapshot(turn int32) aigame.Snapshot {
	battle := readyBattle()
	battle.Turn = turn
	for index, participant := range battle.Participants {
		if Side(participant.BattleID) == 0 && participant.BattleID%10 < 5 {
			battle.Participants[index].Dead = true
			battle.Participants[index].HP = 0
		}
	}
	return aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: battle}
}

// endedWithResult is the server's own conclusion: it sends RS/RD, the client
// answers EO, and the battle is over with a result on the books.
func endedWithResult(turn int32, result string) aigame.Snapshot {
	battle := readyBattle()
	battle.Active = false
	battle.Ended = true
	battle.Result = result
	battle.Turn = turn
	return aigame.Snapshot{Phase: aigame.PhaseWorld, Battle: battle}
}

// endedAfterWipe is the same transition after the client gave up on a party
// that was already down: EO with no result, because the server sends none.
func endedAfterWipe(turn int32) aigame.Snapshot {
	snapshot := endedWithResult(turn, "")
	return snapshot
}

func worldSnapshotAfter(turn int32) aigame.Snapshot {
	return aigame.Snapshot{Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 100, X: 480, Y: 520}}
}

func TestLiveCountsFightsAndHowTheyEnded(t *testing.T) {
	var l live

	// A fight the server resolves.
	l.observe(fightingSnapshot())
	if state, changed := l.describe(fightingSnapshot(), true, ""); !changed || !state.InBattle || state.Battles != 1 || state.Turn != 3 {
		t.Fatalf("fight not reported: %+v changed=%v", state, changed)
	}
	if state, _ := l.describe(fightingSnapshot(), true, ""); state.Enemies != 2 {
		t.Fatalf("enemies = %d, want the two living opponents", state.Enemies)
	}
	l.observe(endedWithResult(4, "win"))
	l.observe(worldSnapshotAfter(4))
	state, _ := l.describe(worldSnapshotAfter(4), true, "")
	if state.Wins != 1 || state.Losses != 0 || state.InBattle {
		t.Fatalf("a resolved fight must count as a win: %+v", state)
	}

	// A fight the party loses.
	l.observe(wipedSnapshot(1))
	if state, _ := l.describe(wipedSnapshot(1), true, ""); !state.InBattle || state.Battles != 2 {
		t.Fatalf("second fight not counted: %+v", state)
	}
	l.observe(endedAfterWipe(2))
	state, _ = l.describe(worldSnapshotAfter(2), true, "")
	if state.Losses != 1 || state.Wins != 1 {
		t.Fatalf("a wiped party must count as a loss: %+v", state)
	}
}

func TestLiveReportsOnlyChangesAndKnowsWhenItIsLooking(t *testing.T) {
	var l live
	l.observe(worldSnapshotAfter(0))
	if _, changed := l.describe(worldSnapshotAfter(0), true, ""); !changed {
		t.Fatal("the first description is always a change")
	}
	if _, changed := l.describe(worldSnapshotAfter(0), true, ""); changed {
		t.Fatal("an unchanged state must not be reported twice")
	}
	state, changed := l.describe(worldSnapshotAfter(0), false, "")
	if !changed || state.Seeking {
		t.Fatalf("walking off must be visible: %+v changed=%v", state, changed)
	}

	l.observe(fightingSnapshot())
	if state, _ := l.describe(fightingSnapshot(), true, ""); state.Seeking {
		t.Fatalf("a loop in a fight is not looking for one: %+v", state)
	}
}

// Walking is only half the answer to "is it fighting?": the panel has to be
// able to say how much of the fight is left.
func TestLiveEnemiesCountsOpponentsOnly(t *testing.T) {
	battle := readyBattle()
	battle.Participants = append(battle.Participants, aigame.BattleParticipant{BattleID: 12, Name: "Wolf", HP: 20, MaxHP: 20})
	snapshot := aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: battle}
	if got := LiveEnemies(snapshot); got != 3 {
		t.Fatalf("LiveEnemies = %d, want the three living opponents", got)
	}
	// A defeated opponent stops counting, so the number falls as the fight runs.
	for index, participant := range battle.Participants {
		if participant.BattleID == 10 {
			battle.Participants[index].HP = 0
			battle.Participants[index].Dead = true
		}
	}
	snapshot.Battle = battle
	if got := LiveEnemies(snapshot); got != 2 {
		t.Fatalf("LiveEnemies after a kill = %d, want 2", got)
	}
	unknown := aigame.Snapshot{Battle: aigame.BattleSnapshot{MyNoKnown: false}}
	if got := LiveEnemies(unknown); got != 0 {
		t.Fatalf("an unknown side has no opponents to count: %d", got)
	}
}
