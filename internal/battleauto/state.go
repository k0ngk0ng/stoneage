package battleauto

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// State is what a running loop is doing, in numbers a host can phrase itself.
// A host that hands the character over needs to be able to show whether it is
// in a fight, how that fight is going, and how the last few went; without it
// the panel can only say "automation is on".
type State struct {
	InBattle bool  `json:"in_battle"`
	Turn     int32 `json:"turn"`
	Enemies  int   `json:"enemies"`
	Battles  int   `json:"battles"`
	Wins     int   `json:"wins"`
	Losses   int   `json:"losses"`
	// Seeking means the loop is walking to find a fight rather than answering
	// one, and Blocked names what is stopping it when it cannot: "window" for
	// an unanswered dialogue, "phase" when the character is not in the world.
	Seeking bool   `json:"seeking"`
	Blocked string `json:"blocked,omitempty"`
}

// live keeps the running totals and the current fight across passes.
type live struct {
	inBattle  bool
	sawDefeat bool
	sawResult bool
	battles   int
	wins      int
	losses    int
	reported  State
}

// observe folds one snapshot into the totals. A fight is counted when it
// starts, and settled when it ends: a wiped party is a loss, a server result
// is a win, and a fight that simply stopped being reported (a dropped session)
// is neither.
func (l *live) observe(snapshot aigame.Snapshot) {
	battle := snapshot.Battle
	if battle.Active {
		if !l.inBattle {
			l.inBattle = true
			l.battles++
			l.sawDefeat = false
			l.sawResult = false
		}
		// MySideDefeated only answers while the battle is live, so the wipe
		// has to be noticed here rather than at the end.
		l.sawDefeat = l.sawDefeat || battle.MySideDefeated()
		l.sawResult = l.sawResult || battle.Result != ""
		return
	}
	if !l.inBattle {
		return
	}
	l.inBattle = false
	// The snapshot that ends the fight is also the one carrying the server's
	// result; the wipe was already noticed while it was live.
	l.sawResult = l.sawResult || battle.Result != ""
	switch {
	case l.sawDefeat:
		l.losses++
	case l.sawResult:
		l.wins++
	}
}

// describe reports the current state, and whether it differs from the last one
// the host was told about.
func (l *live) describe(snapshot aigame.Snapshot, seeking bool, blocked string) (State, bool) {
	state := State{
		InBattle: snapshot.Battle.Active,
		Turn:     snapshot.Battle.Turn,
		Enemies:  LiveEnemies(snapshot),
		Battles:  l.battles,
		Wins:     l.wins,
		Losses:   l.losses,
		Seeking:  seeking && !snapshot.Battle.Active,
		Blocked:  blocked,
	}
	if state == l.reported {
		return state, false
	}
	l.reported = state
	return state, true
}
