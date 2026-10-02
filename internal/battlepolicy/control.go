package battlepolicy

// control is a frozen observation-only teacher. Its shared plan reserves one
// status attempt per target, not a guaranteed success. Existing native statuses
// block additional status attacks; sleep is broken by damage and stone raises
// defense. No enemy resistance, allocation or unannounced order is consulted.
func controlChoices(f Frame) ([]int, error) {
	return controlMemberChoices(f, -1)
}

// independentControlChoices is an equal-observation rule baseline. Members
// share public entities/history, not provisional orders. Each independently
// coordinates its own player and pet. It is not an independently trained model.
func independentControlChoices(f Frame) ([]int, error) {
	choices := make([]int, len(f.Slots))
	for member := 0; member < f.Mode; member++ {
		local, err := controlMemberChoices(f, member)
		if err != nil {
			return nil, err
		}
		for i, slot := range f.Slots {
			if slot.Member == member {
				choices[i] = local[i]
			}
		}
	}
	return choices, nil
}

func controlMemberChoices(f Frame, member int) ([]int, error) {
	// Frozen focus/sustain can select status attacks as ordinary damage. Hide
	// those candidates only in a private planning copy; control eligibility is
	// decided below against the original observation and unchanged indices.
	base := f
	base.Slots = append([]Slot(nil), f.Slots...)
	for i := range base.Slots {
		base.Slots[i].Candidates = append([]Candidate(nil), f.Slots[i].Candidates...)
		for j := range base.Slots[i].Candidates {
			if base.Slots[i].Candidates[j].Features[15] == 1 {
				base.Slots[i].Candidates[j].Supported = false
			}
		}
	}
	choices, err := sustainMemberChoices(base, member)
	if err != nil {
		return nil, err
	}
	focus, best := -1, float64(0)
	// Prefer attacking a free target while teammates keep others disabled.
	// If all enemies are disabled, resume damage rather than wait forever.
	for pass := 0; pass < 2 && focus < 0; pass++ {
		for row, x := range f.Entities {
			if !controlTarget(x) || pass == 0 && hardControlled(x) {
				continue
			}
			hp := ruleUnscale(x[eHP])
			if x[eFlags+6] == 1 {
				hp *= 2
			} // observed stone defense penalty
			if focus < 0 || hp < best {
				focus, best = row, hp
			}
		}
	}
	if focus < 0 {
		return choices, nil
	}
	for i, slot := range f.Slots {
		if member >= 0 && slot.Member != member {
			continue
		}
		selected := slot.Candidates[choices[i]]
		if selected.Features[0] != 1 {
			continue
		} // retain healing/switch/guard
		for j, c := range slot.Candidates {
			if c.Supported && c.Target == focus && c.Features[0] == 1 && c.Features[15] == 0 {
				choices[i] = j
				break
			}
		}
	}
	attacks := make([]int, len(f.Entities))
	for i, slot := range f.Slots {
		if member >= 0 && slot.Member != member {
			continue
		}
		c := slot.Candidates[choices[i]]
		if c.Features[0] == 1 && c.Target >= 0 {
			attacks[c.Target]++
		}
	}
	planned := make([]bool, len(f.Entities))
	for i, slot := range f.Slots {
		if slot.Actor != "pet" || member >= 0 && slot.Member != member {
			continue
		}
		bestChoice, score := -1, float64(0)
		for j, c := range slot.Candidates {
			if !c.Supported || c.Features[15] != 1 || c.Target < 0 || planned[c.Target] {
				continue
			}
			x := f.Entities[c.Target]
			if !controlTarget(x) || x[eHPRatio] < .35 || anyPublicStatus(x) {
				continue
			}
			value := float64(0)
			switch {
			case c.Features[17] == 1 && c.Target != focus && attacks[c.Target] == 0:
				value = 3 // stone away from coordinated damage
			case c.Features[19] == 1 && c.Target != focus && attacks[c.Target] == 0:
				value = 2.5 // sleep away from anything that would wake it
			case c.Features[18] == 1:
				value = 2 // confusion can accompany focused physical damage
			case c.Features[16] == 1:
				value = 1 // poison applies sustained pressure
			}
			if value == 0 {
				continue
			}
			value += .25*float64(x[eHPRatio]) + .1*float64(x[ePlayer])
			if value > score {
				bestChoice, score = j, value
			}
		}
		if bestChoice >= 0 {
			old := slot.Candidates[choices[i]]
			if old.Features[0] == 1 && old.Target >= 0 {
				attacks[old.Target]--
			}
			choices[i] = bestChoice
			chosen := slot.Candidates[bestChoice]
			planned[chosen.Target] = true
			// Status attacks themselves also cause physical damage. Later pets
			// must not plan a sleep/stone on this target in the same turn.
			attacks[chosen.Target]++
		}
	}
	return choices, nil
}

func controlTarget(x [EntityFeatures]float32) bool {
	return x[eAlly] == 0 && x[eAlive] == 1 && x[eBench] == 0 && x[eHPKnown] == 1
}

func hardControlled(x [EntityFeatures]float32) bool {
	// BC flags, not BM status IDs or attack-hit flags.
	return x[eFlags+4] == 1 || x[eFlags+5] == 1 || x[eFlags+6] == 1 || x[eFlags+13] == 1
}

func anyPublicStatus(x [EntityFeatures]float32) bool {
	for _, bit := range []int{3, 4, 5, 6, 7, 8, 11, 12, 13, 14, 15} {
		if x[eFlags+bit] == 1 {
			return true
		}
	}
	return false
}
