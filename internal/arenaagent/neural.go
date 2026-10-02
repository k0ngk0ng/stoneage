package arenaagent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type neuralFailure struct{ code, detail string }

func (e *neuralFailure) Error() string      { return "learned: " + e.detail }
func neuralError(code, detail string) error { return &neuralFailure{code, detail} }

// A candidate is usable explicitly, but is never presented as a certified or
// stronger policy. Rules/platform are compatibility checks, not quality claims.
func (l *Learned) validateServer(view Object) error {
	if l.neural == nil {
		return nil
	}
	clock := obj(obj(view["battle"])["Clock"])
	if str(clock["RulesVersion"]) != RulesVersion || str(clock["RulesDigest"]) != l.neural.Environment.Rules || str(clock["EnginePlatform"]) != l.neural.Environment.Platform {
		return neuralError("rules_mismatch", "server must advertise the model's actual rules digest and engine platform")
	}
	return nil
}

func localModel(s Strategy) *Learned {
	if provider, ok := s.(interface{ learnedModel() *Learned }); ok {
		return provider.learnedModel()
	}
	switch strategy := s.(type) {
	case *Learned:
		return strategy
	case *Hybrid:
		return strategy.Local
	}
	return nil
}

// query only acknowledges sending a request. Wait for the shared projection
// instead of treating one CLI invocation or one arbitrary event as the reply.
func waitServerCapabilities(parent context.Context, requireRules bool, observe func(context.Context) (Object, error)) (Object, error) {
	return waitBattleMetadata(parent, requireRules, 0, observe)
}

// Optional metadata gets a bounded grace period: old servers can still play
// basic/llm, but their missing provenance must remain missing in recordings.
// The required BTIME contract and transport errors are never optional.
func waitBattleMetadata(parent context.Context, requireRules bool, grace time.Duration, observe func(context.Context) (Object, error)) (Object, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	optionalDeadline := time.Now().Add(grace)
	for {
		if e := parent.Err(); e != nil {
			return nil, e
		}
		v, e := observe(ctx)
		if e != nil {
			return nil, e
		}
		if e := parent.Err(); e != nil {
			return nil, e
		}
		clock := obj(obj(v["battle"])["Clock"])
		hasRules := str(clock["RulesDigest"]) != "" && str(clock["EnginePlatform"]) != ""
		if integer(v["schema_version"]) == 1 && str(clock["RulesVersion"]) == RulesVersion && (hasRules || !requireRules && !time.Now().Before(optionalDeadline)) {
			return v, nil
		}
		if e = sleep(ctx, 50*time.Millisecond); e != nil {
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			return nil, neuralError("missing_server_capability", "server did not return compatible battle metadata; update the client/server before queueing")
		}
	}
}

type observationCutoff struct {
	Stream string `json:"stream"`
	Cursor uint64 `json:"cursor"`
	Gap    bool   `json:"gap"`
}

type neuralTeam struct {
	views        []aigame.BattleView
	names        []string
	observer     int
	cutoff       observationCutoff
	recordCutoff *int64
}

func (l *Learned) neuralTeam(team Object) (neuralTeam, error) {
	var out neuralTeam
	var err error
	out.recordCutoff, err = neuralRecordCutoff(team)
	if err != nil {
		return out, err
	}
	members := obj(team["members"])
	// teamObservation returns []string directly; persisted JSON decodes it as
	// []any. Do not silently treat the native form (or malformed data) as empty.
	missing := 0
	switch ids := team["missing_ids"].(type) {
	case nil:
	case []string:
		missing = len(ids)
	case []any:
		missing = len(ids)
	default:
		return out, neuralError("invalid_observation", "invalid missing-member list")
	}
	if len(members) != l.mode || integer(team["mode"]) != l.mode || missing != 0 {
		return out, neuralError("incomplete_team", "one complete controlled team is required")
	}
	out.names = sortedKeys(members)
	for i, name := range out.names {
		v := obj(members[name])
		if e := l.validateServer(v); e != nil {
			return out, e
		}
		var view aigame.BattleView
		if e := decode(enc(v), &view); e != nil {
			return out, neuralError("invalid_observation", "invalid typed battle observation")
		}
		if view.MatchID != str(team["match_id"]) || int(view.Turn) != integer(team["turn"]) || int(view.Battle.MyNo)/10 != integer(team["side"]) {
			return out, neuralError("mixed_observation", "member observation differs from team identity")
		}
		out.views = append(out.views, view)
		if i == 0 || view.Battle.MyNo < out.views[out.observer].Battle.MyNo {
			out.observer = i
		}
	}
	v := obj(members[out.names[out.observer]])
	if obj(v["event_cutoff"]) == nil || decode(enc(v["event_cutoff"]), &out.cutoff) != nil || out.cutoff.Stream == "" {
		return out, neuralError("missing_cutoff", "event stream/cursor for this observation is missing; update the local commander")
	}
	return out, nil
}

// neuralHistory selects only the designated observer's events at or before a
// saved decision cutoff. Server timestamps and animation turn labels are NOT
// used to decide causality; both can differ from the decision-turn boundary.
func neuralHistory(t neuralTeam, history []Object, previousStream string, previousCursor uint64, first bool) (battlepolicy.History, error) {
	v := t.views[t.observer]
	member, cutoff := t.names[t.observer], t.cutoff
	b := &aigame.BattleEventBatch{Stream: cutoff.Stream, Cursor: cutoff.Cursor, Gap: cutoff.Gap, Observation: v}
	for _, entry := range history {
		if str(entry["member"]) != member || str(entry["stream"]) != cutoff.Stream || obj(entry["event"]) == nil {
			continue
		}
		var event aigame.BattleEvent
		if e := decode(enc(entry["event"]), &event); e != nil {
			return battlepolicy.History{}, neuralError("invalid_history", "invalid typed battle event")
		}
		if event.Sequence <= previousCursor || event.Sequence > cutoff.Cursor {
			continue
		}
		if event.MatchID != v.MatchID {
			return battlepolicy.History{}, neuralError("mixed_history", "event belongs to another match")
		}
		b.Events = append(b.Events, event)
	}
	sort.Slice(b.Events, func(i, j int) bool { return b.Events[i].Sequence < b.Events[j].Sequence })
	h := battlepolicy.History{Batch: b, PreviousStream: previousStream, PreviousCursor: previousCursor, First: first}
	if first {
		// EN is an observed lifecycle boundary, so prior matches in the same
		// stream need not be replayed. Never infer a boundary at a later turn.
		for i, event := range b.Events {
			if event.Function == "EN" && len(event.Integers) > 0 && event.Integers[0] > 0 {
				if v.Turn != 0 || event.Sequence == 0 {
					return h, neuralError("missing_history", "battle history does not begin at turn zero")
				}
				h.InitialCursor = event.Sequence - 1
				b.Events = b.Events[i:]
				break
			}
		}
	}
	for _, entry := range history {
		gap := obj(entry["event_gap"])
		if str(entry["member"]) != member || gap == nil || str(gap["stream"]) != cutoff.Stream {
			continue
		}
		if t.recordCutoff != nil {
			var id int64
			if decode(enc(entry["record_id"]), &id) != nil || id <= 0 {
				return h, neuralError("missing_history_order", "captured gap has no committed command record ID")
			}
			if id > *t.recordCutoff {
				continue
			}
		}
		var g observationCutoff
		if e := decode(enc(gap), &g); e != nil {
			return h, neuralError("invalid_history", "invalid event-gap checkpoint")
		}
		if g.Cursor > max(previousCursor, h.InitialCursor) && g.Cursor <= cutoff.Cursor {
			b.Gap = true
		}
	}
	return h, nil
}

// Reuse the complete persisted plan if part of the turn has already been
// submitted. In hybrid this is the final LLM-approved plan, not a new local
// proposal. Unknown/written intents are excluded by the existing slot fence.
func (l *Learned) reuseNeuralPlan(current, record Object) (Decision, error) {
	original := obj(record["team"])
	if str(original["observation_id"]) != str(current["observation_id"]) {
		return Decision{}, neuralError("changed_observation", "saved turn plan no longer matches the current observation")
	}
	version, strategy := str(record["version"]), str(record["strategy"])
	if !(strategy == "learned" && version == l.Version() || strategy == "hybrid" && strings.HasPrefix(version, l.Version()+"+")) {
		return Decision{}, neuralError("different_policy", "saved turn was decided by another policy or fallback")
	}
	var saved Plan
	if e := decode(enc(record["plan"]), &saved); e != nil || validatePlan(original, saved) != nil {
		return Decision{}, neuralError("invalid_saved_plan", "saved turn plan is incomplete or invalid")
	}
	available := slots(current)
	choices := map[Slot]string{}
	for _, order := range saved.Orders {
		key := Slot{order.Member, order.Actor}
		if available[key] == nil {
			continue
		}
		if available[key][order.Candidate] == nil {
			return Decision{}, neuralError("changed_candidates", "saved candidate is no longer available")
		}
		choices[key] = order.Candidate
	}
	p := planFor(current, choices)
	return Decision{p, l.ID(), l.Version(), Object{"reused_plan": true, "model_status": l.neural.Status}}, validatePlan(current, p)
}

func (l *Learned) decideNeural(ctx context.Context, team Object, history []Object) (Decision, error) {
	current, e := l.neuralTeam(team)
	if e != nil {
		return Decision{}, e
	}
	turn := integer(team["turn"])
	if turn < 0 || turn > 10000 {
		return Decision{}, neuralError("invalid_turn", "decision turn outside supported range")
	}
	// Recover from observations, not a mutable hidden-state counter. Polling
	// twice or restarting the process therefore cannot advance the GRU twice.
	priorTurnPlan := false
	for _, record := range history {
		t := obj(record["team"])
		if t == nil || str(t["match_id"]) != str(team["match_id"]) {
			continue
		}
		n := integer(t["turn"])
		if n == turn {
			// Recovery can receive a complete later database. A plan must have
			// existed when this observation captured its command history.
			if current.recordCutoff != nil {
				var id int64
				if decode(enc(record["record_id"]), &id) != nil || id <= 0 {
					return Decision{}, neuralError("missing_history_order", "captured current-turn plan has no committed command record ID")
				}
				if id > *current.recordCutoff {
					continue
				}
			}
			priorTurnPlan = true
			version, strategy := str(record["version"]), str(record["strategy"])
			if str(t["observation_id"]) == str(team["observation_id"]) && (strategy == "learned" && version == l.Version() || strategy == "hybrid" && strings.HasPrefix(version, l.Version()+"+")) {
				return l.reuseNeuralPlan(team, record)
			}
			continue
		}
	}
	if priorTurnPlan {
		partial, err := teamHasSubmittedOrReservedOrders(team)
		if err != nil {
			return Decision{}, err
		}
		if partial {
			return Decision{}, neuralError("changed_observation", "partially submitted turn no longer matches a saved model plan")
		}
		// A stale/failed attempt that wrote no actions may be replanned from
		// the fresh observation. It must not poison the entire current turn.
	}
	// A validated persisted final plan is authoritative for remaining orders.
	// Reconstruct earlier memory only when a genuinely fresh inference is needed.
	previous, e := neuralPriorObservations(team, history)
	if e != nil {
		return Decision{}, e
	}
	var memory []float32
	var stream, observerName string
	var cursor uint64
	var decision battlepolicy.Output
	var frame battlepolicy.Frame
	for n := 0; n <= turn; n++ {
		if e = ctx.Err(); e != nil {
			return Decision{}, e
		}
		t := current
		if n < turn {
			if previous[n] == nil {
				return Decision{}, neuralError("missing_history", fmt.Sprintf("missing complete observation for turn %d", n))
			}
			t, e = l.neuralTeam(previous[n])
			if e != nil {
				return Decision{}, e
			}
		}
		if n > 0 && t.names[t.observer] != observerName {
			return Decision{}, neuralError("changed_observer", "designated history observer changed during the match")
		}
		observerName = t.names[t.observer]
		h, err := neuralHistory(t, history, stream, cursor, n == 0)
		if err != nil {
			return Decision{}, err
		}
		frame, e = battlepolicy.EncodeVersion(t.views, h, l.neural.Features)
		if e != nil {
			return Decision{}, neuralError("invalid_history_or_observation", e.Error())
		}
		if frame.Events[12] != 0 {
			return Decision{}, neuralError("history_gap", "incomplete history has no trained coverage in this model")
		}
		g := &battlenet.Graph[float32]{}
		bound := l.neural.Network.Bind(g)
		previousMemory := g.New(1, l.neural.Network.Config.Width, memory)
		var nextMemory *battlenet.Tensor[float32]
		if n < turn {
			nextMemory, e = battlepolicy.Recall(ctx, bound, frame, previousMemory)
		} else {
			decision, e = battlepolicy.Forward(ctx, bound, frame, previousMemory, nil, nil)
			nextMemory = decision.Memory
		}
		if e != nil {
			return Decision{}, e
		}
		memory = append([]float32(nil), nextMemory.Data...)
		stream, cursor = t.cutoff.Stream, t.cutoff.Cursor
	}
	choices := map[Slot]string{}
	for i, slot := range frame.Slots {
		choices[Slot{current.names[slot.Member], slot.Actor}] = slot.Candidates[decision.Choices[i]].ID
	}
	p := planFor(team, choices)
	d := Decision{p, l.ID(), l.Version(), Object{
		"model_status": l.neural.Status, "architecture": l.neural.Architecture,
		"policy_log_probability": decision.LogProb.Data[0], "conditional_log_probabilities": decision.ConditionalLogProbs,
		"estimated_return": decision.Value.Data[0], "excluded_candidates": frame.Excluded,
		"history_turns": turn + 1, "history_complete": true,
	}}
	return d, validatePlan(team, p)
}
