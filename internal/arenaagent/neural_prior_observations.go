package arenaagent

import (
	"fmt"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// neuralPriorObservations reconstructs full decision frames, never execution
// subsets. Append-only IDs establish command causality; event causality remains
// separately clipped by each original observation's stream/cursor.
func neuralPriorObservations(current Object, history []Object) (map[int]Object, error) {
	match, turn := str(current["match_id"]), integer(current["turn"])
	cutoff, err := neuralRecordCutoff(current)
	if err != nil {
		return nil, err
	}
	var records []Object
	ids, noIDs := false, false
	for _, record := range history {
		team := obj(record["team"])
		receipt := obj(record["submission"])
		if receipt == nil {
			receipt = obj(record["submission_intent"])
		}
		n, id := -1, ""
		if team != nil {
			n, id = integer(team["turn"]), str(team["match_id"])
		}
		if receipt != nil {
			selection := obj(receipt["selection"])
			n, id = integer(selection["turn"]), str(selection["match_id"])
		}
		if id != match || n < 0 || n >= turn {
			continue
		}
		rid := integer(record["record_id"])
		if cutoff != nil {
			if rid <= 0 {
				return nil, neuralError("missing_history_order", "captured history lacks a committed command record ID")
			}
			if int64(rid) > *cutoff {
				continue
			}
		}
		ids = ids || rid > 0
		noIDs = noIDs || rid <= 0
		records = append(records, record)
	}
	if ids && noIDs {
		return nil, neuralError("mixed_history_order", "cannot order legacy and committed command records together")
	}
	if ids {
		sort.Slice(records, func(i, j int) bool { return integer(records[i]["record_id"]) < integer(records[j]["record_id"]) })
		for i := 1; i < len(records); i++ {
			if integer(records[i-1]["record_id"]) == integer(records[i]["record_id"]) {
				return nil, neuralError("duplicate_history_record", "duplicate committed command record")
			}
		}
	}
	type attempt struct {
		team              Object
		plan              Plan
		strategy, version string
		origin            int
	}
	type pendingCommand struct {
		origin, attempt int
		written         bool
	}
	type turnHistory struct {
		attempts []attempt
		active   map[Slot]pendingCommand
		sources  map[int]Object
	}
	turns := map[int]*turnHistory{}
	for _, record := range records {
		if team := obj(record["team"]); team != nil {
			n := integer(team["turn"])
			h := turns[n]
			if h == nil {
				h = &turnHistory{active: map[Slot]pendingCommand{}, sources: map[int]Object{}}
				turns[n] = h
			}
			a := attempt{team: team, origin: -1, strategy: str(record["strategy"]), version: str(record["version"])}
			if err := decode(enc(record["plan"]), &a.plan); err != nil || validatePlan(team, a.plan) != nil {
				return nil, neuralError("invalid_saved_plan", "historical decision plan does not match its observation")
			}
			if yes(obj(record["diagnostics"])["reused_plan"]) {
				// The latest matching complete source carries the SAME full final plan.
				// This attempt may contain only the remaining pet or other squad members.
				for j := len(h.attempts) - 1; j >= 0; j-- {
					before := h.attempts[j]
					if before.origin < 0 || str(before.team["observation_id"]) != str(team["observation_id"]) || before.strategy != a.strategy || before.version != a.version {
						continue
					}
					source := h.attempts[before.origin]
					if neuralPlanSubset(a.plan, source.plan) {
						a.origin = before.origin
						break
					}
				}
			} else if neuralFullDecisionObservation(team) {
				a.origin = len(h.attempts)
				h.sources[a.origin] = team
			}
			h.attempts = append(h.attempts, a)
			continue
		}
		receipt, intent := obj(record["submission"]), false
		if receipt == nil {
			receipt, intent = obj(record["submission_intent"]), true
		}
		selection := obj(receipt["selection"])
		n := integer(selection["turn"])
		h := turns[n]
		if h == nil || len(h.attempts) == 0 {
			return nil, neuralError("orphan_submission", "command evidence has no preceding decision")
		}
		a := h.attempts[len(h.attempts)-1]
		slot := Slot{str(record["member"]), str(receipt["actor"])}
		view := obj(obj(a.team["members"])[slot.Member])
		matched := str(selection["observation_id"]) == str(view["observation_id"])
		orderMatches := false
		for _, order := range a.plan.Orders {
			if order.Member == slot.Member && order.Actor == slot.Actor && order.Candidate == str(selection["candidate_id"]) {
				orderMatches = true
				break
			}
		}
		if !matched || !orderMatches {
			return nil, neuralError("conflicting_submission", "command receipt differs from preceding decision")
		}
		response := obj(receipt["response"])
		code := str(obj(response["data"])["code"])
		rejected := !intent && !yes(response["ok"]) && (code == "stale_observation" || code == "not_ready" || code == "invalid_candidate" || code == "automation_conflict")
		old, exists := h.active[slot]
		attemptIndex := len(h.attempts) - 1
		if exists && (old.attempt != attemptIndex || intent) {
			return nil, neuralError("conflicting_submission", "a new attempt overlaps an unresolved actor command")
		}
		if rejected {
			if exists && old.written {
				return nil, neuralError("conflicting_submission", "a rejection cannot undo a written command")
			}
			delete(h.active, slot)
			continue
		}
		if a.origin < 0 {
			return nil, neuralError("missing_full_observation", "submitted plan has no original complete observation")
		}
		h.active[slot] = pendingCommand{origin: a.origin, attempt: attemptIndex, written: old.written || !intent && yes(response["ok"])}
	}
	out := map[int]Object{}
	for n, h := range turns {
		origin := -1
		for _, command := range h.active {
			source := command.origin
			if origin >= 0 && source != origin {
				return nil, neuralError("mixed_executed_observations", fmt.Sprintf("turn %d commands use incompatible decision frames", n))
			}
			origin = source
		}
		if origin < 0 {
			// Legacy evidence with a single full decision frame is unambiguous for
			// observation-only recurrence. Multiple unproven alternatives are not.
			if len(h.sources) != 1 {
				return nil, neuralError("ambiguous_history", fmt.Sprintf("turn %d has no unique executed observation", n))
			}
			for source := range h.sources {
				origin = source
			}
		}
		out[n] = h.sources[origin]
	}
	return out, nil
}

func neuralFullDecisionObservation(team Object) bool {
	for _, raw := range obj(team["members"]) {
		view := obj(raw)
		if len(obj(view["reserved_actors"])) > 0 {
			return false
		}
		var battle aigame.BattleSnapshot
		if decode(enc(view["battle"]), &battle) != nil {
			return false
		}
		if battle.PlayerSubmitted && !battle.DeadPlayerCommandComplete() || battle.PetSubmitted {
			return false
		}
	}
	return len(obj(team["members"])) > 0
}

func neuralPlanSubset(subset, full Plan) bool {
	if subset.Match != full.Match || subset.Turn != full.Turn || subset.Observation != full.Observation {
		return false
	}
	for _, order := range subset.Orders {
		found := false
		for _, original := range full.Orders {
			if order == original {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// The absence of a boundary identifies legacy in-memory history; a malformed
// declared boundary must never silently mean zero or unbounded.
func neuralRecordCutoff(team Object) (*int64, error) {
	raw, present := team["history_record_cutoff"]
	if !present {
		return nil, nil
	}
	var cutoff int64
	if raw == nil || decode(enc(raw), &cutoff) != nil || cutoff < 0 {
		return nil, neuralError("invalid_history_order", "invalid committed command history cutoff")
	}
	return &cutoff, nil
}
