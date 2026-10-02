package arenaagent

import (
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Full public history, bounded by the observation's causal cutoffs. Database
// record IDs and each observer's event sequence are separate ordering domains.
func llmHistory(team Object, history []Object) ([]Object, error) {
	cutoff, err := neuralRecordCutoff(team)
	if err != nil {
		return nil, err
	}
	if len(obj(team["members"])) != integer(team["mode"]) || len(arr(team["missing_ids"])) != 0 {
		return nil, llmError("incomplete_team", "complete team observation required", nil)
	}
	type streamKey struct{ member, stream string }
	bounds := map[streamKey]uint64{}
	addBounds := func(t Object) error {
		for member, raw := range obj(t["members"]) {
			v := obj(raw)
			if v["event_cutoff"] == nil {
				if cutoff != nil {
					return llmError("missing_cutoff", "member event boundary missing", nil)
				}
				continue
			}
			var c observationCutoff
			if decode(enc(v["event_cutoff"]), &c) != nil || c.Stream == "" {
				return llmError("invalid_cutoff", "invalid member event boundary", nil)
			}
			key := streamKey{member, c.Stream}
			bounds[key] = max(bounds[key], c.Cursor)
		}
		return nil
	}
	var records, events, legacy []Object
	ids := map[int64]bool{}
	for _, record := range history {
		if obj(record["event"]) != nil {
			events = append(events, record)
			continue
		}
		var id int64
		if cutoff != nil {
			if decode(enc(record["record_id"]), &id) != nil || id <= 0 {
				return nil, llmError("missing_history_order", "committed history record ID missing", nil)
			}
			if id > *cutoff {
				continue
			}
			if ids[id] {
				return nil, llmError("duplicate_history_record", "duplicate committed record", nil)
			}
			ids[id] = true
		}
		if t := obj(record["team"]); t != nil {
			if str(t["match_id"]) != str(team["match_id"]) || integer(t["turn"]) > integer(team["turn"]) {
				continue
			}
			if err := addBounds(t); err != nil {
				return nil, err
			}
		}
		for _, key := range []string{"submission", "submission_intent"} {
			if receipt := obj(record[key]); receipt != nil {
				s := obj(receipt["selection"])
				if str(s["match_id"]) != str(team["match_id"]) || integer(s["turn"]) > integer(team["turn"]) {
					return nil, llmError("mixed_history", "command history differs from current match", nil)
				}
			}
		}
		// Provider prompts/responses are never journalled recursively. Keep the
		// actual observation, final orders, receipts and bounded decision metadata.
		copy := clone(record)
		if diagnostics := obj(copy["diagnostics"]); diagnostics != nil {
			clean := Object{}
			for _, key := range []string{"reused_plan", "final_plan_id", "proposal_id", "model_status", "estimated_return", "fallback", "fallback_reason", "provider_outcome"} {
				if v, ok := diagnostics[key]; ok {
					clean[key] = v
				}
			}
			copy["diagnostics"] = clean
		}
		if cutoff == nil {
			legacy = append(legacy, copy)
		} else {
			records = append(records, copy)
		}
	}
	if err := addBounds(team); err != nil {
		return nil, err
	}
	// A current observer's exact cutoff overrides any later observation stored
	// in a supplied database snapshot. Historical streams retain their own bounds.
	for member, raw := range obj(team["members"]) {
		var c observationCutoff
		if decode(enc(obj(raw)["event_cutoff"]), &c) == nil && c.Stream != "" {
			bounds[streamKey{member, c.Stream}] = c.Cursor
		}
	}
	sort.Slice(records, func(i, j int) bool { return integer(records[i]["record_id"]) < integer(records[j]["record_id"]) })
	out := []Object{}
	for _, record := range append(records, legacy...) {
		if gap := obj(record["event_gap"]); gap != nil {
			var c observationCutoff
			if decode(enc(gap), &c) != nil {
				return nil, llmError("invalid_history", "invalid recorded event gap", nil)
			}
			limit, ok := bounds[streamKey{str(record["member"]), c.Stream}]
			if !ok || c.Cursor > limit {
				continue
			}
		}
		future := false
		for member, raw := range obj(obj(record["team"])["members"]) {
			var c observationCutoff
			if decode(enc(obj(raw)["event_cutoff"]), &c) == nil && c.Stream != "" && c.Cursor > bounds[streamKey{member, c.Stream}] {
				future = true
			}
		}
		if !future {
			out = append(out, record)
		}
	}
	filtered := []Object{}
	seen := map[streamKey]map[uint64]bool{}
	for _, record := range events {
		var event aigame.BattleEvent
		if decode(enc(record["event"]), &event) != nil {
			return nil, llmError("invalid_history", "invalid public battle event", nil)
		}
		if event.MatchID != str(team["match_id"]) {
			continue
		}
		key := streamKey{str(record["member"]), str(record["stream"])}
		limit, ok := bounds[key]
		if !ok || event.Sequence > limit {
			continue
		}
		if seen[key] == nil {
			seen[key] = map[uint64]bool{}
		}
		if event.Sequence == 0 || seen[key][event.Sequence] {
			return nil, llmError("duplicate_history_event", "invalid or duplicate event sequence", nil)
		}
		seen[key][event.Sequence] = true
		filtered = append(filtered, clone(record))
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if str(a["member"]) != str(b["member"]) {
			return str(a["member"]) < str(b["member"])
		}
		if str(a["stream"]) != str(b["stream"]) {
			return str(a["stream"]) < str(b["stream"])
		}
		return num(obj(a["event"])["sequence"]) < num(obj(b["event"])["sequence"])
	})
	return append(out, filtered...), nil
}
