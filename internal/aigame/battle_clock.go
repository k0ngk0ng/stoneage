package aigame

import (
	"strconv"
	"strings"
	"time"
)

func (state *gameState) applyBattleClock(raw string) {
	p := strings.Split(raw, "|")
	b := &state.snapshot.Battle
	// A world-state reply advertises observer compatibility before queueing.
	// It carries no active deadline and must never make a battle ready.
	if len(p) == 6 && p[1] == "" && p[2] == "-1" && p[3] == "0" && p[5] != "" {
		b.Clock = BattleClock{RulesVersion: p[5]}
		return
	}
	if (len(p) != 5 && len(p) != 6) || p[1] == "" || p[1] != b.LadderID || !b.Active || b.Ended || b.Movie {
		return
	}
	turn, err1 := strconv.ParseInt(p[2], 10, 32)
	deadline, err2 := strconv.ParseInt(p[3], 10, 64)
	now, err3 := strconv.ParseInt(p[4], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || turn < 0 || deadline <= 0 || now <= 0 {
		return
	}
	b.Clock = BattleClock{Known: true, ServerTurn: int32(turn), DeadlineMS: deadline, ServerNowMS: now, ReceivedAtMS: time.Now().UnixMilli()}
	if len(p) == 6 {
		b.Clock.RulesVersion = p[5]
	}
}
