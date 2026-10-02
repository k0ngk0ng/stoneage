package aigame

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

func (state *gameState) applyBattleClock(raw string) {
	p := strings.Split(raw, "|")
	b := &state.snapshot.Battle
	rules, platform := b.Clock.RulesDigest, b.Clock.EnginePlatform
	// A world-state reply advertises observer compatibility before queueing.
	// It carries no active deadline and must never make a battle ready.
	if len(p) == 6 && p[1] == "" && p[2] == "-1" && p[3] == "0" && p[5] != "" {
		b.Clock = BattleClock{RulesVersion: p[5], RulesDigest: rules, EnginePlatform: platform}
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
	b.Clock = BattleClock{Known: true, ServerTurn: int32(turn), DeadlineMS: deadline, ServerNowMS: now, ReceivedAtMS: time.Now().UnixMilli(), RulesDigest: rules, EnginePlatform: platform}
	if len(p) == 6 {
		b.Clock.RulesVersion = p[5]
	}
}

// Separate from BTIME so existing clients still receive their six-field clock
// reply. This metadata never sets readiness, extends a timer or infers power.
func (state *gameState) applyBattleRules(raw string) {
	p := strings.Split(raw, "|")
	c := &state.snapshot.Battle.Clock
	c.RulesDigest, c.EnginePlatform = "", ""
	if len(p) != 3 || p[0] != "BTRULES" || len(p[1]) != 64 || strings.ToLower(p[1]) != p[1] || p[2] != "linux-amd64" && p[2] != "linux-arm64" {
		return
	}
	if _, e := hex.DecodeString(p[1]); e != nil {
		return
	}
	c.RulesDigest, c.EnginePlatform = p[1], p[2]
}
