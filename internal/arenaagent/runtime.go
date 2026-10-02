package arenaagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Runner struct {
	config                        Config
	store                         *Store
	members                       []*member
	strategy                      Strategy
	selection                     *modelSelection
	selectionSaved                bool
	loadChampion                  championLoader
	ids                           map[string]string
	completed                     map[string]bool
	Stop                          atomic.Bool
	Output                        io.Writer
	outputMu                      sync.Mutex
	HumanOutput                   bool
	livePhase, liveTurn, livePlan string
	liveSince, liveLast           time.Time
}

func NewRunner(c Config) (*Runner, error) {
	return newRunner(context.Background(), c)
}

func newRunner(ctx context.Context, c Config) (*Runner, error) {
	if e := c.validateModelSource(); e != nil {
		return nil, e
	}
	store, e := OpenStore(c.StateDir)
	if e != nil {
		return nil, e
	}
	// Inspect a persisted selection even if the config was edited to a fixed
	// model/basic. Otherwise a restart while queued could bypass its version.
	strategy, selection, e := store.restoreSelection(c)
	saved := selection != nil
	if e == nil && strategy == nil {
		strategy, selection, e = configuredStrategy(ctx, c)
	}
	if e != nil {
		store.DB.Close()
		return nil, e
	}
	r := &Runner{config: c, store: store, strategy: strategy, selection: selection, selectionSaved: saved, ids: map[string]string{}, completed: map[string]bool{}, Output: io.Discard}
	for _, m := range c.Members {
		r.members = append(r.members, &member{cfg: m, binary: c.Sactl, ownership: c.OwnershipDir, store: store})
	}
	return r, nil
}
func (r *Runner) Close() {
	for _, m := range r.members {
		m.close()
	}
	r.store.DB.Close()
}
func (r *Runner) report(state string, fields Object) {
	if r.Output == nil {
		return
	}
	fields["state"] = state
	r.outputMu.Lock()
	defer r.outputMu.Unlock()
	if r.HumanOutput {
		_, _ = fmt.Fprintf(r.Output, "[%s] %s\n", time.Now().Format("15:04:05"), strings.Join(sanitizedLines(liveLine(state, fields)), "\n"))
	} else {
		_, _ = r.Output.Write(append(enc(fields), '\n'))
	}
}
func (r *Runner) expected() map[string]bool {
	out := map[string]bool{}
	for _, id := range r.ids {
		out[id] = true
	}
	return out
}
func (r *Runner) initialize(ctx context.Context) error {
	for _, m := range r.members {
		if e := m.start(ctx); e != nil {
			return e
		}
	}
	for _, m := range r.members {
		if str(m.login["character"]) == "" {
			return fmt.Errorf("member %s requires character", m.cfg.ID)
		}
		deadline := time.Now().Add(45 * time.Second)
		var observation Object
		for {
			v, e := m.data(ctx, "observe")
			if e == nil && yes(obj(v["Player"])["HasStatus"]) {
				observation = v
				break
			}
			if e != nil && !retryable(e) {
				return e
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("member %s login timed out", m.cfg.ID)
			}
			if e = sleep(ctx, 500*time.Millisecond); e != nil {
				return e
			}
		}
		if str(observation["Account"]) != str(m.login["account"]) || str(observation["Character"]) != str(m.login["character"]) {
			return fmt.Errorf("member socket belongs to another identity")
		}
		data, e := m.data(ctx, "arena", "status")
		if e != nil {
			return e
		}
		status := obj(data["snapshot"])
		r.ids[m.cfg.ID] = str(obj(status["self"])["id"])
		if r.ids[m.cfg.ID] == "" {
			return fmt.Errorf("missing authoritative character ID")
		}
		if match := obj(status["match"]); match != nil {
			if e = r.pinMatch(str(match["id"])); e != nil {
				return e
			}
		}
		if str(status["phase"]) == "queued" && r.selection != nil && !r.selectionSaved {
			return fmt.Errorf("queued team has no saved model selection; cancel the existing queue before using champion_directory")
		}
		if _, e = m.request(ctx, "query", "BTIME"); e != nil {
			return e
		}
		local := localModel(r.strategy)
		if _, e = m.request(ctx, "query", "BTRULES"); e != nil {
			return e
		}
		view, e := waitBattleMetadata(ctx, local != nil && local.neural != nil, 250*time.Millisecond, func(ctx context.Context) (Object, error) { return m.data(ctx, "battle-state") })
		if e != nil {
			return e
		}
		if integer(view["schema_version"]) != 1 || str(obj(obj(view["battle"])["Clock"])["RulesVersion"]) != RulesVersion {
			return fmt.Errorf("server does not advertise compatible BTIME rules; update server before queueing")
		}
		if local != nil {
			if e = local.validateServer(view); e != nil {
				return e
			}
		}
		if _, e = m.request(ctx, "auto-battle", "off"); e != nil {
			return e
		}
		if _, e = m.mutate(ctx, "strategy", "manual"); e != nil {
			return e
		}
	}
	if len(r.expected()) != r.config.Mode {
		return fmt.Errorf("same character configured more than once")
	}
	return r.store.Err()
}
func (r *Runner) statuses(ctx context.Context) (map[string]Object, error) {
	out := map[string]Object{}
	type result struct {
		m *member
		v Object
		e error
	}
	replies := make(chan result, len(r.members))
	for _, m := range r.members {
		go func(m *member) {
			v, e := m.call(ctx, 4*time.Second, true, "arena", "status")
			replies <- result{m, v, e}
		}(m)
	}
	for range r.members {
		reply := <-replies
		m := reply.m
		if reply.e != nil {
			if m.exited() {
				if e := m.start(ctx); e != nil {
					return nil, e
				}
				r.store.Record("daemon_restarted", Object{}, "", m.cfg.ID)
			}
			continue
		}
		v := obj(obj(reply.v["data"])["snapshot"])
		if str(obj(v["self"])["id"]) != r.ids[m.cfg.ID] {
			return nil, fmt.Errorf("character identity changed during execution")
		}
		out[m.cfg.ID] = v
	}
	return out, r.store.Err()
}
func (r *Runner) validateMatch(s Object) (string, error) {
	m := obj(s["match"])
	if integer(m["mode"]) != r.config.Mode {
		return "", fmt.Errorf("active match mode differs from configuration")
	}
	teams := objects(m["teams"])
	side := integer(m["side"])
	if side < 0 || side >= len(teams) {
		return "", fmt.Errorf("invalid match side")
	}
	own := map[string]bool{}
	for _, p := range objects(teams[side]["members"]) {
		own[str(p["id"])] = true
	}
	expected := r.expected()
	if len(own) != len(expected) {
		return "", fmt.Errorf("active team contains missing or uncontrolled members")
	}
	for id := range own {
		if !expected[id] {
			return "", fmt.Errorf("active team contains uncontrolled members")
		}
	}
	id := str(m["id"])
	return id, r.pinMatch(id)
}
func (r *Runner) collect(ctx context.Context, m *member) (Object, error) {
	if _, e := m.call(ctx, 3*time.Second, true, "query", "BTIME"); e != nil {
		return nil, e
	}
	if _, e := m.call(ctx, 3*time.Second, true, "query", "BTRULES"); e != nil {
		return nil, e
	}
	local := localModel(r.strategy)
	if _, e := waitBattleMetadata(ctx, local != nil && local.neural != nil, 250*time.Millisecond, func(ctx context.Context) (Object, error) { return m.data(ctx, "battle-state") }); e != nil {
		return nil, e
	}
	stream, cursor := r.store.Cursor(m.cfg.ID)
	reply, e := m.call(ctx, 3*time.Second, true, "battle-events", strconv.FormatInt(cursor, 10), stream)
	if e != nil {
		return nil, e
	}
	batch := obj(reply["data"])
	r.store.Ingest(m.cfg.ID, batch)
	if r.store.Err() == nil {
		r.reportEvents(m.cfg.ID, batch, cursor)
	}
	v := obj(batch["observation"])
	if v == nil {
		return nil, fmt.Errorf("missing battle observation")
	}
	// Keep the atomic event/observation cutoff in each recorded team view.
	// Later polls may ingest more events before a decision is reconstructed;
	// a recurrent strategy must never read those future events into this turn.
	v["event_cutoff"] = Object{"stream": batch["stream"], "cursor": batch["cursor"], "gap": batch["gap"]}
	reserved := Object{}
	for _, actor := range []string{"player", "pet"} {
		if intent := r.store.Intent(m.cfg.ID, str(v["match_id"]), integer(v["turn"]), actor); intent != "" {
			reserved[actor] = intent
		}
	}
	v["reserved_actors"] = reserved
	return v, r.store.Err()
}
func (r *Runner) acknowledge(ctx context.Context, statuses map[string]Object) error {
	for _, m := range r.members {
		result := obj(statuses[m.cfg.ID]["result"])
		if result == nil {
			continue
		}
		if r.selection != nil {
			if e := r.pinMatch(str(result["id"])); e != nil {
				return e
			}
		}
		stream, cursor := r.store.Cursor(m.cfg.ID)
		reply, e := m.call(ctx, 3*time.Second, true, "battle-events", strconv.FormatInt(cursor, 10), stream)
		if e != nil {
			return e
		}
		r.store.Ingest(m.cfg.ID, obj(reply["data"]))
		if r.store.Err() == nil {
			r.reportEvents(m.cfg.ID, obj(reply["data"]), cursor)
		}
		r.store.Result(result)
		if _, e = m.mutate(ctx, "ack"); e != nil {
			return e
		}
		id := str(result["id"])
		if r.completed[id] {
			continue
		}
		r.completed[id] = true
		r.report("result", Object{"match_id": id, "winner_side": result["winner_side"], "rated": result["rated"]})
	}
	return r.store.Err()
}
func remainingMS(view Object) float64 {
	clock := obj(obj(view["battle"])["Clock"])
	if !yes(clock["Known"]) {
		return 0
	}
	return num(clock["DeadlineMS"]) - num(clock["ServerNowMS"]) - max(0, float64(time.Now().UnixMilli())-num(clock["ReceivedAtMS"]))
}
func (r *Runner) battle(ctx context.Context, statuses map[string]Object) (returnErr error) {
	started := time.Now()
	matchID := ""
	for _, s := range statuses {
		if obj(s["match"]) == nil || str(s["phase"]) != "battle" {
			continue
		}
		id, e := r.validateMatch(s)
		if e != nil {
			return e
		}
		if matchID != "" && matchID != id {
			return fmt.Errorf("squad split across matches")
		}
		matchID = id
	}
	if matchID == "" {
		return nil
	}
	type collected struct {
		m    *member
		view Object
		err  error
	}
	replies := make(chan collected, len(r.members))
	for _, m := range r.members {
		go func(m *member) { v, e := r.collect(ctx, m); replies <- collected{m, v, e} }(m)
	}
	groups := map[int]Object{}
	newest := -1
	for range r.members {
		item := <-replies
		if item.err != nil {
			r.store.Record("observation_failure", Object{"kind": "unavailable"}, matchID, item.m.cfg.ID)
			continue
		}
		v := item.view
		b := obj(v["battle"])
		if str(v["match_id"]) != matchID || !yes(b["Active"]) || yes(b["Ended"]) || !yes(obj(b["Clock"])["Known"]) {
			continue
		}
		turn := integer(v["turn"])
		if groups[turn] == nil {
			groups[turn] = Object{}
		}
		groups[turn][item.m.cfg.ID] = v
		newest = max(newest, turn)
	}
	if e := r.store.Err(); e != nil {
		return e
	}
	if newest < 0 {
		return nil
	}
	team, e := teamObservation(groups[newest], r.expected(), r.config.Mode)
	if e != nil {
		return e
	}
	remaining := 1e30
	for _, v := range groups[newest] {
		remaining = min(remaining, remainingMS(obj(v)))
	}
	if len(groups[newest]) != r.config.Mode && remaining > 15000 || remaining <= 2000 || len(slots(team)) == 0 {
		return nil
	}
	deadline := time.Now().Add(time.Duration(min(15000, remaining-2000)) * time.Millisecond)
	decisionCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	timing := newTurnTiming(started, team, r.strategy.ID(), remaining, deadline)
	defer func() {
		if ctx.Err() != nil {
			timing.Outcome = "canceled"
		}
		timing.finish()
		// Keep timing out of model history and preserve the original error. A
		// failed store cannot promise that its final diagnostic was persisted.
		r.store.Record("turn_timing", timing, matchID, "")
		if returnErr == nil {
			returnErr = r.store.Err()
		}
	}()
	history, recordCutoff := r.store.HistorySnapshot(matchID)
	team["history_record_cutoff"] = recordCutoff
	if e = r.store.Err(); e != nil {
		return e
	}
	timing.next("decision")
	turnKey := fmt.Sprintf("%s:%d", matchID, newest)
	if r.liveTurn != turnKey {
		r.liveTurn = turnKey
		r.report("observing", Object{"turn": newest, "match_id": matchID, "strategy": r.strategy.ID()})
	}
	decisionStarted := time.Now()
	decision, e := r.decideForTurn(ctx, decisionCtx, team, history)
	timing.DecisionDeadlineExceeded = time.Now().After(deadline)
	if e != nil {
		return e
	}
	timing.DecisionStrategy, timing.DecisionVersion = decision.Strategy, decision.Version
	timing.Plan, timing.Orders = hash(decision.Plan), len(decision.Plan.Orders)
	timing.ReusedPlan = yes(decision.Diagnostics["reused_plan"])
	if time.Now().After(deadline.Add(time.Second)) {
		timing.Outcome = "late_decision"
		return nil
	}
	timing.next("plan_persistence")
	summary := Object{}
	for member, value := range groups[newest] {
		parts := []Object{}
		for _, p := range objects(obj(obj(value)["battle"])["Participants"]) {
			parts = append(parts, Object{"id": p["BattleID"], "hp": p["HP"], "max_hp": p["MaxHP"], "flags": p["Flags"]})
		}
		summary[member] = parts
	}
	r.store.Record("turn", Object{"turn": newest, "team": team, "plan": decision.Plan, "strategy": decision.Strategy, "version": decision.Version, "diagnostics": decision.Diagnostics, "summary": summary}, matchID, "")
	if e = r.store.Err(); e != nil {
		return e
	}
	r.reportDecision(team, decision, time.Since(decisionStarted))
	timing.next("dispatch")
	var wg sync.WaitGroup
	for _, m := range r.members {
		wg.Add(1)
		go func(m *member) { defer wg.Done(); r.dispatch(ctx, m, team, decision.Plan) }(m)
	}
	wg.Wait()
	if r.store.Err() == nil {
		timing.Outcome = "dispatch_complete"
	}
	return r.store.Err()
}

// A strategy's own deadline may trigger a fallback while the commander still
// has time to submit. Stopping the commander (including a fixture timeout)
// must instead stop this turn, without fabricating a strategy failure/plan.
func (r *Runner) decideForTurn(ctx, decisionCtx context.Context, team Object, history []Object) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	decision, err := r.strategy.Decide(decisionCtx, team, history)
	if stop := ctx.Err(); stop != nil {
		return Decision{}, stop
	}
	if err == nil {
		err = validatePlan(team, decision.Plan)
	}
	if err == nil {
		return decision, nil
	}
	kind := "decision_failed"
	var failure *neuralFailure
	if errors.As(err, &failure) {
		kind = failure.code
	}
	var providerFailure *llmFailure
	if errors.As(err, &providerFailure) {
		kind = providerFailure.code
	}
	// A fallback is a new team decision. After any written/uncertain order,
	// only a validated continuation of the saved final plan may be submitted.
	// Returning the original error preserves the runner's normal lifecycle:
	// continuity errors stop it; an isolated deadline remains retryable.
	partial, observationErr := teamHasSubmittedOrReservedOrders(team)
	if partial || observationErr != nil {
		reason := "partial_submission"
		if observationErr != nil {
			reason = "invalid_submission_state"
		}
		r.store.Record("strategy_rejected", Object{"strategy": r.strategy.ID(), "kind": kind, "reason": reason}, str(team["match_id"]), "")
		if stored := r.store.Err(); stored != nil {
			return Decision{}, stored
		}
		if observationErr != nil {
			return Decision{}, observationErr
		}
		return Decision{}, err
	}
	decision, err = (Basic{}).Decide(ctx, team, history)
	if stop := ctx.Err(); stop != nil {
		return Decision{}, stop
	}
	r.report("strategy_fallback", Object{"strategy": r.strategy.ID(), "kind": kind})
	r.store.Record("strategy_fallback", Object{"strategy": r.strategy.ID(), "kind": kind}, str(team["match_id"]), "")
	if stored := r.store.Err(); stored != nil {
		return Decision{}, stored
	}
	return decision, err
}

func (r *Runner) dispatch(ctx context.Context, m *member, t Object, p Plan) {
	for _, actor := range []string{"player", "pet"} {
		for _, order := range p.Orders {
			if order.Member != m.cfg.ID || order.Actor != actor {
				continue
			}
			reply, e := m.call(ctx, 3*time.Second, true, "battle-state")
			if e != nil {
				return
			}
			fresh := obj(reply["data"])
			original := obj(obj(t["members"])[m.cfg.ID])
			if str(fresh["observation_id"]) != str(original["observation_id"]) || remainingMS(fresh) <= 500 {
				return
			}
			var candidate Object
			for _, c := range objects(fresh["candidates"]) {
				if str(c["id"]) == order.Candidate {
					candidate = c
					break
				}
			}
			if candidate == nil || !yes(candidate["ready"]) {
				continue
			}
			selection := Object{"match_id": p.Match, "turn": p.Turn, "observation_id": fresh["observation_id"], "candidate_id": order.Candidate}
			if !r.store.Reserve(m.cfg.ID, selection, actor) {
				continue
			}
			response, e := m.call(ctx, 3*time.Second, false, "battle-act", string(enc(selection)))
			if e != nil {
				response = Object{"ok": false, "kind": "unknown"}
			}
			r.store.Finish(m.cfg.ID, selection, actor, response)
			outcome := "提交结果不确定，保留记录，避免重复指令"
			status := r.store.Intent(m.cfg.ID, p.Match, p.Turn, actor)
			if r.store.Err() != nil {
				status = "storage_error"
				outcome = "提交记录保存失败，停止后须核对实际状态"
			} else if status == "written" {
				outcome = "指令已发送，等待服务器战报"
			} else if status == "" {
				status = "rejected"
				outcome = "指令未接受：" + str(obj(response["data"])["code"])
			}
			r.report("submission", Object{"match_id": p.Match, "turn": p.Turn, "member": m.cfg.ID, "actor": actor, "outcome": outcome, "outcome_code": status})
		}
	}
}
func (r *Runner) contact(ctx context.Context, leader, target *member) (Object, error) {
	lock, e := claim(filepath.Join(leader.ownership, leader.service+".meeting.lock"))
	if e != nil {
		return nil, nil
	}
	defer lock.Close()
	find := func() (Object, error) {
		v, e := leader.data(ctx, "arena", "contacts")
		if e != nil {
			return nil, e
		}
		for _, c := range objects(v["contacts"]) {
			if str(c["id"]) == r.ids[target.cfg.ID] {
				return c, nil
			}
		}
		return nil, nil
	}
	if c, e := find(); e != nil || c != nil {
		return c, e
	}
	lead, e := leader.data(ctx, "observe")
	if e != nil {
		return nil, e
	}
	follower, e := target.data(ctx, "observe")
	if e != nil {
		return nil, e
	}
	if str(lead["Phase"]) != "world" || str(follower["Phase"]) != "world" {
		return nil, fmt.Errorf("card exchange requires world phase")
	}
	pos := obj(lead["Position"])
	origin := obj(follower["Position"])
	if integer(origin["Floor"]) != integer(pos["Floor"]) {
		if _, e = target.call(ctx, 60*time.Second, true, "warp", strconv.Itoa(integer(pos["Floor"]))); e != nil {
			return nil, e
		}
	}
	type tile struct {
		x, y      int
		direction string
	}
	adjacent := []tile{{1, 0, "right"}, {1, 1, "down-right"}, {0, 1, "down"}, {-1, 1, "down-left"}, {-1, 0, "left"}, {-1, -1, "up-left"}, {0, -1, "up"}, {1, -1, "up-right"}}
	id := r.ids[target.cfg.ID]
	if len(id) < 4 {
		return nil, fmt.Errorf("invalid character ID")
	}
	suffix, _ := strconv.ParseUint(id[len(id)-4:], 16, 16)
	offset := int(suffix) % 8
	occupied := map[[2]int]bool{}
	for _, a := range objects(lead["Actors"]) {
		occupied[[2]int{integer(a["X"]), integer(a["Y"])}] = true
	}
	var place *tile
	for i := 0; i < 8 && place == nil; i++ {
		d := adjacent[(i+offset)%8]
		d.x += integer(pos["X"])
		d.y += integer(pos["Y"])
		if occupied[[2]int{d.x, d.y}] {
			continue
		}
		for attempt := 0; attempt < 8; attempt++ {
			reply, e := target.call(ctx, 60*time.Second, false, "goto", strconv.Itoa(d.x), strconv.Itoa(d.y))
			if e != nil {
				return nil, e
			}
			observed, e := target.data(ctx, "observe")
			if e != nil {
				return nil, e
			}
			p := obj(observed["Position"])
			if str(observed["Phase"]) != "world" || integer(p["Floor"]) != integer(pos["Floor"]) {
				return nil, fmt.Errorf("member left rendezvous floor")
			}
			if integer(p["X"]) == d.x && integer(p["Y"]) == d.y {
				place = &d
				break
			}
			if str(obj(reply["data"])["code"]) != "stale_observation" {
				r.store.Record("navigation_failure", Object{"position": p, "destination": []int{d.x, d.y}}, "", target.cfg.ID)
				break
			}
		}
	}
	if place == nil {
		return nil, fmt.Errorf("no reachable empty adjacent tile for card exchange")
	}
	restore := func() {
		if integer(origin["Floor"]) == integer(pos["Floor"]) {
			for i := 0; i < 8; i++ {
				reply, e := target.call(ctx, 60*time.Second, false, "goto", strconv.Itoa(integer(origin["X"])), strconv.Itoa(integer(origin["Y"])))
				if e != nil || yes(reply["ok"]) || str(obj(reply["data"])["code"]) != "stale_observation" {
					break
				}
			}
		}
	}
	confirmed := false
	for i := 0; i < 10; i++ {
		seen, e := leader.data(ctx, "observe")
		if e != nil {
			return nil, e
		}
		people := []Object{}
		for _, a := range objects(seen["Actors"]) {
			if integer(a["X"]) == place.x && integer(a["Y"]) == place.y {
				people = append(people, a)
			}
		}
		if len(people) == 1 && str(people[0]["PersistentCharacterID"]) == id {
			confirmed = true
			break
		}
		if e = sleep(ctx, 200*time.Millisecond); e != nil {
			return nil, e
		}
	}
	if !confirmed {
		r.store.Record("contact_identity_unconfirmed", Object{"expected": id}, "", target.cfg.ID)
		restore()
		return nil, nil
	}
	if _, e = target.request(ctx, "social", "trade-card", "1"); e != nil {
		return nil, e
	}
	if _, e = leader.request(ctx, "look", place.direction); e != nil {
		return nil, e
	}
	if _, e = leader.request(ctx, "mail", "add"); e != nil {
		return nil, e
	}
	for i := 0; i < 10; i++ {
		c, e := find()
		if e != nil {
			return nil, e
		}
		if c != nil {
			restore()
			return c, nil
		}
		if e = sleep(ctx, 200*time.Millisecond); e != nil {
			return nil, e
		}
	}
	return nil, fmt.Errorf("name-card exchange not confirmed")
}
func (r *Runner) prepare(ctx context.Context, statuses map[string]Object) error {
	if len(statuses) != len(r.members) {
		return nil
	}
	expected := r.expected()
	for _, s := range statuses {
		switch str(s["phase"]) {
		case "battle", "countdown", "result", "queued":
			return nil
		}
		if room := obj(s["room"]); room != nil {
			for _, p := range objects(room["members"]) {
				if !expected[str(p["id"])] {
					return fmt.Errorf("refusing to reorganize room containing uncontrolled character")
				}
			}
		}
	}
	leader := r.members[0]
	lead := statuses[leader.cfg.ID]
	room := obj(lead["room"])
	if room == nil {
		_, e := leader.mutate(ctx, "create", strconv.Itoa(r.config.Mode))
		return e
	}
	if str(room["leader_id"]) != r.ids[leader.cfg.ID] {
		for _, m := range r.members {
			if r.ids[m.cfg.ID] == str(room["leader_id"]) {
				_, e := m.mutate(ctx, "leader", r.ids[leader.cfg.ID])
				return e
			}
		}
		return fmt.Errorf("room leader is uncontrolled")
	}
	if integer(room["mode"]) != r.config.Mode {
		_, e := leader.mutate(ctx, "mode", strconv.Itoa(r.config.Mode))
		return e
	}
	members := map[string]bool{}
	for _, p := range objects(room["members"]) {
		members[str(p["id"])] = true
	}
	for _, m := range r.members[1:] {
		if members[r.ids[m.cfg.ID]] {
			continue
		}
		current := statuses[m.cfg.ID]
		if obj(current["room"]) != nil {
			_, e := m.mutate(ctx, "leave")
			return e
		}
		for _, inv := range objects(current["invitations"]) {
			if str(inv["room_id"]) == str(room["id"]) {
				_, e := m.mutate(ctx, "accept", str(inv["id"]))
				return e
			}
		}
		c, e := r.contact(ctx, leader, m)
		if e != nil || c == nil {
			return e
		}
		_, e = leader.mutate(ctx, "invite", strconv.Itoa(integer(c["slot"])), str(c["id"]))
		return e
	}
	for _, m := range r.members {
		self := obj(statuses[m.cfg.ID]["self"])
		if str(self["strategy"]) != "manual" {
			return fmt.Errorf("another controller changed member strategy")
		}
		if mask := m.cfg.PetMask; mask != nil && integer(self["pet_mask"]) != *mask {
			if yes(self["ready"]) {
				if _, e := m.mutate(ctx, "unready"); e != nil {
					return e
				}
			}
			_, e := m.mutate(ctx, "loadout", strconv.Itoa(*mask))
			return e
		}
		if !yes(self["ready"]) {
			_, e := m.mutate(ctx, "ready")
			return e
		}
	}
	if e := r.selectForQueue(ctx); e != nil {
		return e
	}
	_, e := leader.mutate(ctx, "queue")
	if e == nil {
		r.report("queued", Object{"mode": r.config.Mode, "strategy": r.strategy.ID(), "version": r.strategy.Version()})
	}
	return e
}
func (r *Runner) Run(ctx context.Context, matches int) error {
	defer r.Close()
	if e := r.initialize(ctx); e != nil {
		return e
	}
	r.report("running", Object{"mode": r.config.Mode, "strategy": r.strategy.ID(), "state_dir": r.config.StateDir, "model": r.config.Model})
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := r.store.Err(); e != nil {
			return e
		}
		statuses, e := r.statuses(ctx)
		if e != nil {
			return e
		}
		r.reportPhase(statuses)
		active, pending := false, false
		for _, s := range statuses {
			phase := str(s["phase"])
			active = active || (obj(s["match"]) != nil && (phase == "battle" || phase == "countdown"))
			pending = pending || obj(s["result"]) != nil
		}
		done := r.Stop.Load() || matches > 0 && len(r.completed) >= matches
		if len(statuses) == len(r.members) && !active && !pending && done {
			for _, s := range statuses {
				if str(s["phase"]) == "queued" {
					_, e = r.members[0].mutate(ctx, "cancel")
					break
				}
			}
			return e
		}
		switch {
		case pending:
			e = r.acknowledge(ctx, statuses)
		case active:
			e = r.battle(ctx, statuses)
		case !done:
			e = r.prepare(ctx, statuses)
		}
		if e != nil {
			if !retryable(e) {
				return e
			}
			r.store.Record("reconcile", Object{"kind": "retryable_command"}, "", "")
			if e = sleep(ctx, time.Second); e != nil {
				return e
			}
		}
		if e = sleep(ctx, 200*time.Millisecond); e != nil {
			return e
		}
	}
}
