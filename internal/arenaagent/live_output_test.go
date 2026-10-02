package arenaagent

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestLiveOutputDecisionSubmissionAndEvents(t *testing.T) {
	var out bytes.Buffer
	r := Runner{Output: &out, HumanOutput: true, members: []*member{{cfg: MemberConfig{ID: "member-0"}}}}
	team := fixture(t, 1)
	d, e := (Basic{}).Decide(context.Background(), team, nil)
	if e != nil {
		t.Fatal(e)
	}
	d.Diagnostics = Object{"estimated_return": float32(1.25)}
	r.reportDecision(team, d, 12*time.Millisecond)
	r.reportDecision(team, d, 12*time.Millisecond)
	if strings.Count(out.String(), "决策 · 耗时") != 1 || !strings.Contains(out.String(), "attack") || !strings.Contains(out.String(), "HP 100/100") || !strings.Contains(out.String(), "1.250") {
		t.Fatal(out.String())
	}
	r.report("submission", Object{"turn": 4, "member": "member-0", "actor": "player", "outcome": "指令已发送，等待服务器战报"})
	batch := clone(Object{"events": []Object{{"sequence": 5, "turn": 4, "effects": []Object{{"text": "伤害 20\x1b[2J"}}}}})
	r.reportEvents("member-0", batch, 4)
	r.reportEvents("member-0", batch, 5)
	r.reportEvents("member-1", batch, 0)
	if strings.Count(out.String(), "战报：") != 1 || strings.Contains(out.String(), "\x1b") {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "等待服务器战报") {
		t.Fatal(out.String())
	}
	out.Reset()
	r.HumanOutput = false
	r.livePlan = ""
	r.reportDecision(team, d, time.Millisecond)
	var parsed Object
	if e := decode(out.Bytes(), &parsed); e != nil || str(parsed["state"]) != "decision" || len(arr(parsed["actions"])) != 2 {
		t.Fatal(out.String(), e)
	}
	// Embedded users and tests may deliberately omit an output stream.
	(&Runner{}).report("strategy_fallback", Object{})
}
func TestLiveQueuePollingDoesNotSpam(t *testing.T) {
	var out bytes.Buffer
	r := Runner{Output: &out, members: []*member{{cfg: MemberConfig{ID: "one"}}}}
	statuses := map[string]Object{"one": {"phase": "queued"}}
	for i := 0; i < 20; i++ {
		r.reportPhase(statuses)
	}
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
	r.liveLast = time.Now().Add(-16 * time.Second)
	r.reportPhase(statuses)
	r.reportPhase(statuses)
	if strings.Count(out.String(), "waiting") != 1 {
		t.Fatal(out.String())
	}
	statuses["one"] = Object{"phase": "battle", "match": Object{"id": "m"}}
	r.reportPhase(statuses)
	r.reportPhase(statuses)
	if strings.Count(out.String(), `"state":"battle"`) != 1 {
		t.Fatal(out.String())
	}
}
