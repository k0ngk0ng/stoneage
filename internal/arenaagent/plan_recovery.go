package arenaagent

// recoverFinalPlan runs before either adviser. Persisted final orders, rather
// than a fresh inference or an old local proposal, own a partially written turn.
func recoverFinalPlan(team Object, history []Object, strategy, version string) (Decision, bool, error) {
	cutoff, err := neuralRecordCutoff(team)
	if err != nil {
		return Decision{}, false, err
	}
	partial, err := teamHasSubmittedOrReservedOrders(team)
	if err != nil {
		return Decision{}, false, err
	}
	var saved Object
	var latest int64 = -1
	for index, record := range history {
		original := obj(record["team"])
		if original == nil || str(original["match_id"]) != str(team["match_id"]) || integer(original["turn"]) != integer(team["turn"]) {
			continue
		}
		id := int64(index)
		if cutoff != nil {
			if decode(enc(record["record_id"]), &id) != nil || id <= 0 {
				return Decision{}, false, neuralError("missing_history_order", "saved plan has no committed record ID")
			}
			if id > *cutoff {
				continue
			}
		}
		if id >= latest {
			latest, saved = id, record
		}
	}
	if saved != nil {
		original := obj(saved["team"])
		if str(saved["strategy"]) == strategy && str(saved["version"]) == version && str(original["observation_id"]) == str(team["observation_id"]) {
			var full Plan
			if decode(enc(saved["plan"]), &full) != nil || validatePlan(original, full) != nil {
				return Decision{}, false, neuralError("invalid_saved_plan", "saved final plan is invalid")
			}
			available, choices := slots(team), map[Slot]string{}
			for _, order := range full.Orders {
				key := Slot{order.Member, order.Actor}
				if available[key] == nil {
					continue
				}
				if available[key][order.Candidate] == nil {
					return Decision{}, false, neuralError("changed_candidates", "saved final candidate is unavailable")
				}
				choices[key] = order.Candidate
			}
			plan := planFor(team, choices)
			if err := validatePlan(team, plan); err != nil {
				return Decision{}, false, neuralError("invalid_saved_plan", "saved final plan cannot cover remaining actors")
			}
			diagnostics := clone(obj(saved["diagnostics"]))
			if diagnostics == nil {
				diagnostics = Object{}
			}
			if diagnostics["final_plan_id"] == nil {
				diagnostics["final_plan_id"] = hash(full)
			}
			diagnostics["reused_plan"] = true
			return Decision{plan, strategy, version, diagnostics}, true, nil
		}
	}
	if partial {
		return Decision{}, false, neuralError("changed_observation", "partially submitted turn requires its original final plan and exact strategy version")
	}
	return Decision{}, false, nil
}
