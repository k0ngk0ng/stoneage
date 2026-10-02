package battlepolicy

import "fmt"

func RuleSupported(name string) bool {
	return name == "basic" || name == "focus" || name == "guard-break" || name == "defensive" || name == "sustain" || name == "control" || name == "independent-control"
}

func RuleNames() []string { return append(DefaultRuleNames(), "control", "independent-control") }

// New experimental teachers need not silently change the frozen baseline suite.
func DefaultRuleNames() []string {
	return []string{"basic", "focus", "guard-break", "defensive", "sustain"}
}

// RuleChoices supplies frozen, observation-only comparison/teacher policies.
// These are explicit baselines, not model decisions or hidden engine oracles.
func RuleChoices(f Frame, name string) ([]int, error) {
	if !RuleSupported(name) {
		return nil, fmt.Errorf("unknown rule policy %q", name)
	}
	if e := f.Validate(); e != nil {
		return nil, e
	}
	if name == "sustain" {
		return sustainChoices(f)
	}
	if name == "control" {
		return controlChoices(f)
	}
	if name == "independent-control" {
		return independentControlChoices(f)
	}
	choices := make([]int, len(f.Slots))
	for i, s := range f.Slots {
		chosen, guard, wait := -1, -1, -1
		for j, c := range s.Candidates {
			if !c.Supported {
				continue
			}
			if c.Features[1] == 1 {
				guard = j
			}
			if c.Features[2] == 1 {
				wait = j
			}
			attack := c.Features[0] == 1
			if name == "guard-break" && s.Actor == "pet" {
				attack = c.Features[3] == 1
			}
			if !attack || c.Target < 0 || f.Entities[c.Target][eAlly] == 1 {
				continue
			}
			if chosen < 0 {
				chosen = j
				continue
			}
			old := s.Candidates[chosen]
			if name == "basic" || name == "guard-break" {
				if c.Target < old.Target {
					chosen = j
				}
			} else if f.Entities[c.Target][eHPRatio] < f.Entities[old.Target][eHPRatio] || f.Entities[c.Target][eHPRatio] == f.Entities[old.Target][eHPRatio] && c.Target < old.Target {
				chosen = j
			}
		}
		if name == "defensive" && f.Entities[s.Entity][eHPRatio] < .3 && guard >= 0 {
			chosen = guard
		}
		if chosen < 0 {
			chosen = guard
		}
		if chosen < 0 {
			chosen = wait
		}
		if chosen < 0 {
			return nil, fmt.Errorf("rule has no supported action for actor")
		}
		choices[i] = chosen
	}
	return choices, nil
}
