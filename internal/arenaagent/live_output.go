package arenaagent

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
}
func liveLine(state string, f Object) string {
	switch state {
	case "running":
		line := fmt.Sprintf("AI 指挥已启动 · %s · %dv%d", str(f["strategy"]), integer(f["mode"]), integer(f["mode"]))
		if model := str(f["model"]); model != "" {
			line += "\n模型：" + terminalText(model)
		}
		return line + "\n数据目录：" + terminalText(str(f["state_dir"]))
	case "queued":
		return "已加入竞技场匹配，等待对手；无需再开终端匹配。"
	case "waiting":
		return fmt.Sprintf("仍在匹配中 · 已等待 %d 秒", integer(f["elapsed_seconds"]))
	case "countdown":
		return fmt.Sprintf("匹配成功 · 对局 %s · 等待战斗开始", str(f["match_id"]))
	case "battle":
		return fmt.Sprintf("战斗开始 · 对局 %s", str(f["match_id"]))
	case "observing":
		return fmt.Sprintf("第 %d 回合 · %s 正在根据战场状态决策", integer(f["turn"]), str(f["strategy"]))
	case "decision":
		var b strings.Builder
		fmt.Fprintf(&b, "第 %d 回合 · %s 决策 · 耗时 %d ms", integer(f["turn"]), str(f["strategy"]), integer(f["elapsed_ms"]))
		d := obj(f["diagnostics"])
		score := d
		if local := obj(d["local"]); local != nil {
			score = local
		}
		if v, ok := score["policy_log_probability"]; ok {
			fmt.Fprintf(&b, " · 联合动作 logP %.3f", displayNumber(v))
		}
		if v := str(d["fallback"]); v != "" {
			fmt.Fprintf(&b, " · 回退 %s（%s）", v, str(d["fallback_reason"]))
		}
		if v, ok := f["local_plan_changed"].(bool); ok {
			if v {
				b.WriteString(" · 大模型调整了本地建议")
			} else {
				b.WriteString(" · 最终指令沿用本地建议")
			}
		}
		if v, ok := score["estimated_return"]; ok {
			fmt.Fprintf(&b, " · 预期回报 %.3f（非胜率）", displayNumber(v))
		}
		if v := str(d["provider_outcome"]); v != "" {
			fmt.Fprintf(&b, " · 大模型结果 %s", v)
		}
		for _, p := range objects(f["participants"]) {
			fmt.Fprintf(&b, "\n  战位 %d %s HP %d/%d", integer(p["BattleID"]), terminalText(str(p["Name"])), integer(p["HP"]), integer(p["MaxHP"]))
		}
		for _, a := range objects(f["actions"]) {
			fmt.Fprintf(&b, "\n  %s/%s → %s %s · 目标战位 %v", terminalText(str(a["member"])), str(a["actor"]), str(a["kind"]), terminalText(str(a["name"])), a["target"])
		}
		return b.String()
	case "submission":
		return fmt.Sprintf("第 %d 回合 · %s/%s · %s", integer(f["turn"]), str(f["member"]), str(f["actor"]), str(f["outcome"]))
	case "effect":
		return fmt.Sprintf("第 %d 回合 · 战报：%s", integer(f["turn"]), terminalText(str(obj(f["effect"])["text"])))
	case "event_gap":
		return "战报存在缺口，当前观察仍以服务器状态为准。"
	case "strategy_fallback":
		return fmt.Sprintf("策略 %s 本回合不可用（%s），改用 basic", str(f["strategy"]), str(f["kind"]))
	case "result":
		return fmt.Sprintf("对局结束 · %s · 获胜方 %v · 已计分 %v", str(f["match_id"]), f["winner_side"], f["rated"])
	case "stopping_after_match":
		return "将在本场结束后停止；再次 Ctrl+C 立即退出指挥。"
	}
	return state
}

func (r *Runner) reportPhase(statuses map[string]Object) {
	if len(r.members) == 0 {
		return
	}
	s := statuses[r.members[0].cfg.ID]
	phase := str(s["phase"])
	id := str(obj(s["match"])["id"])
	key := phase + ":" + id
	now := time.Now()
	if key != r.livePhase {
		r.livePhase = key
		r.liveSince = now
		r.liveLast = now
		if phase == "battle" || phase == "countdown" {
			r.report(phase, Object{"match_id": id})
		}
	} else if phase == "queued" && now.Sub(r.liveLast) >= 15*time.Second {
		r.liveLast = now
		r.report("waiting", Object{"elapsed_seconds": int(now.Sub(r.liveSince).Seconds())})
	}
}
func (r *Runner) reportEvents(memberID string, batch Object, cursor int64) {
	// The first controlled member is the display observer. Other members' events
	// still enter training storage, but must not repeat the same battle movie.
	if len(r.members) == 0 || memberID != r.members[0].cfg.ID {
		return
	}
	if yes(batch["gap"]) {
		r.report("event_gap", Object{})
	}
	for _, event := range objects(batch["events"]) {
		if int64(num(event["sequence"])) <= cursor && !yes(batch["gap"]) {
			continue
		}
		for _, effect := range objects(event["effects"]) {
			r.report("effect", Object{"match_id": event["match_id"], "turn": event["turn"], "effect": effect})
		}
	}
}
func (r *Runner) reportDecision(team Object, d Decision, elapsed time.Duration) {
	key := hash(d.Plan)
	if r.livePlan == key {
		return
	}
	r.livePlan = key
	actions := []any{}
	participants := []any{}
	members := obj(team["members"])
	for _, id := range sortedKeys(members) {
		participants = arr(obj(obj(members[id])["battle"])["Participants"])
		if len(participants) > 0 {
			break
		}
	}
	for _, order := range d.Plan.Orders {
		for _, c := range objects(obj(members[order.Member])["candidates"]) {
			if str(c["id"]) == order.Candidate {
				actions = append(actions, Object{"member": order.Member, "actor": order.Actor, "kind": c["kind"], "name": c["name"], "target": c["target"], "candidate_id": order.Candidate})
				break
			}
		}
	}
	fields := Object{"match_id": d.Plan.Match, "turn": d.Plan.Turn, "strategy": d.Strategy, "version": d.Version, "diagnostics": d.Diagnostics, "actions": actions, "participants": participants, "elapsed_ms": elapsed.Milliseconds()}
	if proposal := obj(d.Diagnostics["local_proposal"]); proposal != nil {
		fields["local_plan_changed"] = string(enc(obj(proposal["plan"])["orders"])) != string(enc(d.Plan.Orders))
	}
	r.report("decision", fields)
}

func sanitizedLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = terminalText(lines[i])
	}
	return lines
}

func displayNumber(v any) float64 {
	if n, ok := v.(float32); ok {
		return float64(n)
	}
	return num(v)
}
