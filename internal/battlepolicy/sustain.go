package battlepolicy

import "math"

// sustain is a frozen observation-only teacher. Recovery is a nominal planning
// estimate, never an assertion about future engine effects. It reserves that
// estimate across the whole team to avoid every healer choosing the same wound.
// Keep its behavior stable; a future change needs a new rule identity.
func sustainChoices(f Frame) ([]int, error) {
	return sustainMemberChoices(f, -1)
}

// member < 0 retains the frozen team planner. A nonnegative member keeps the
// same public battlefield but may reserve only its own healing/switch orders.
func sustainMemberChoices(f Frame, member int) ([]int, error) {
	choices, err := RuleChoices(f, "focus")
	if err != nil {
		return nil, err
	}
	recovery := make([]float64, len(f.Entities))
	leaving := make([]bool, len(f.Entities))
	for i, slot := range f.Slots {
		if slot.Actor != "player" || member >= 0 && slot.Member != member {
			continue
		}
		best, score := -1, .08
		for j, c := range slot.Candidates {
			if !c.Supported || c.Features[22] != 1 || c.Features[7] != 1 {
				continue
			}
			power := ruleUnscale(c.Features[23])
			value := 0.
			for row, x := range f.Entities {
				if leaving[row] || !healingTarget(c, row, x) {
					continue
				}
				maximum := ruleUnscale(x[eMaxHP])
				if maximum <= 0 {
					continue
				}
				hp := min(maximum, ruleUnscale(x[eHP])+recovery[row])
				if hp/maximum >= .65 {
					continue
				}
				benefit := min(power, maximum-hp) / maximum
				if hp/maximum < .35 {
					benefit *= 2
				}
				value += benefit
			}
			if c.Features[29] == 1 {
				value *= .8
			} // finite consumable stock
			if value > score {
				best, score = j, value
			}
		}
		if best >= 0 {
			choices[i] = best
			c := slot.Candidates[best]
			for row, x := range f.Entities {
				if !leaving[row] && healingTarget(c, row, x) {
					recovery[row] += ruleUnscale(c.Features[23])
				}
			}
			continue
		}
		// If a pet is absent/dead or critically wounded, preserve its remaining
		// HP by summoning a healthy, observed, usable reserve. A planned heal
		// from an earlier teammate can remove the need to switch.
		active := -1
		for row, x := range f.Entities {
			if x[eAlly] == 1 && x[ePet] == 1 && x[eBench] == 0 && x[eOwnerSeat] == f.Entities[slot.Entity][eOwnerSeat] {
				active = row
				break
			}
		}
		if active >= 0 && f.Entities[active][eAlive] == 1 {
			x := f.Entities[active]
			maximum := ruleUnscale(x[eMaxHP])
			if x[eHPKnown] != 1 || maximum <= 0 || (ruleUnscale(x[eHP])+recovery[active])/maximum >= .35 {
				continue
			}
		}
		best, score = -1, 0
		for j, c := range slot.Candidates {
			if !c.Supported || c.Features[30] != 1 || c.Target < 0 {
				continue
			}
			x := f.Entities[c.Target]
			if x[eBench] != 1 || x[eAlly] != 1 || x[eAlive] != 1 || x[eHPKnown] != 1 || x[eHPRatio] < .65 || x[eSummonKnown] != 1 || x[eSummonable] != 1 || x[eSkillEvidence] != 1 {
				continue
			}
			// A known attack is required; guard-only reserves do not restore a
			// useful combatant. Compare only the observed own-pet attack/HP.
			attack := x[56]+x[58]+x[59]+x[60]+x[61]+x[62] > 0
			if !attack {
				continue
			}
			value := float64(x[eHPRatio])
			if x[eStatsKnown] == 1 {
				value += .1 * float64(x[eAttack])
			}
			if value > score {
				best, score = j, value
			}
		}
		if best >= 0 {
			choices[i] = best
			if active >= 0 {
				leaving[active] = true
			}
		}
	}
	return choices, nil
}

func healingTarget(c Candidate, row int, x [EntityFeatures]float32) bool {
	return x[eAlly] == 1 && x[eAlive] == 1 && x[eHPKnown] == 1 && x[eBench] == 0 && (c.Target == row || c.Features[25] == 1)
}

func ruleUnscale(x float32) float64 {
	if x <= 0 {
		return 0
	}
	n := math.Expm1(float64(x) * math.Log(1001))
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return n
}
