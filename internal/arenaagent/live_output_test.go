package arenaagent

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
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

func TestLiveOutputUsesHumanRoundWithoutChangingProtocolRound(t *testing.T) {
	for _, state := range []string{"observing", "decision", "submission", "effect"} {
		f := Object{"turn": 0, "strategy": "basic", "effect": Object{"text": "合击"}}
		if line := liveLine(state, f); !strings.Contains(line, "第 1 回合") {
			t.Fatalf("%s: %s", state, line)
		}
		if integer(f["turn"]) != 0 {
			t.Fatal("protocol round mutated")
		}
	}
}

func TestLiveCandidateStatusIsNotStrengthCertification(t *testing.T) {
	line := liveLine("running", Object{"strategy": "learned", "model_status": "candidate"})
	if !strings.Contains(line, "候选模型") {
		t.Fatal(line)
	}
	if learnedModelStatus(Basic{}) != "" {
		t.Fatal("basic has no learned artifact")
	}
	learned := &Learned{neural: &battlepolicy.Artifact{Status: "candidate"}}
	for _, strategy := range []Strategy{learned, &Hybrid{Local: learned}, &Learned{Model: Object{"status": "candidate"}}} {
		if got := learnedModelStatus(strategy); got != "candidate" {
			t.Fatalf("lost actual artifact status for %T: %q", strategy, got)
		}
	}
}

func TestLiveEffectsUseResolvedRoundAtBattleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		eventTurn, effectTurn int
	}{
		{"start before first menu", 1, 1},
		{"ordinary attack", 7, 8},
		{"settlement after unused menu", 8, 8},
		{"exit after turn reset", 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := Runner{Output: &out, HumanOutput: true, members: []*member{{cfg: MemberConfig{ID: "one"}}}}
			batch := clone(Object{"events": []Object{{"sequence": 1, "turn": tc.eventTurn, "effects": []Object{{"turn": tc.effectTurn, "text": "战报"}}}}})
			r.reportEvents("one", batch, 0)
			want := "第 8 回合"
			if tc.effectTurn == 1 {
				want = "第 1 回合"
			}
			if !strings.Contains(out.String(), want) {
				t.Fatal(out.String())
			}
			out.Reset()
			r.HumanOutput = false
			r.reportEvents("one", batch, 0)
			var event Object
			if err := decode(out.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if integer(event["turn"]) != tc.eventTurn || integer(obj(event["effect"])["turn"]) != tc.effectTurn {
				t.Fatalf("structured rounds changed: %s", out.String())
			}
		})
	}
}
