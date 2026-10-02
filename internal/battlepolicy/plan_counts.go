package battlepolicy

import (
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// PlanTargetCounts encodes only earlier friendly choices in this decision.
// Columns (all counts /10): earlier orders, same individual target orders,
// physical attacks, poison, stone, confusion, sleep, overlapping healing.
// Status attacks are also physical. Healing overlap counts orders, not HP or
// guaranteed successful effects; group healing covers living on-field allies
// OR enemies as actually targeted. Sentinel -1 never means a common target.
// The caller supplies a validated Frame; prefix must end before the scored slot.
// Member baselines see only their own earlier choices. No action is excluded.
func PlanTargetCounts(f Frame, slot int, prefix []int, scope string) ([]float32, error) {
	if slot < 0 || slot >= len(f.Slots) || len(prefix) != slot || scope != "" && scope != "member" {
		return nil, fmt.Errorf("invalid causal plan prefix")
	}
	for i, choice := range prefix {
		if choice < 0 || choice >= len(f.Slots[i].Candidates) || !f.Slots[i].Candidates[choice].Supported {
			return nil, fmt.Errorf("invalid earlier plan choice")
		}
	}
	current := f.Slots[slot]
	values := make([]float32, len(current.Candidates)*battlenet.PlanTargetFeatures)
	for j, candidate := range current.Candidates {
		var counts [battlenet.PlanTargetFeatures]int
		for i, choice := range prefix {
			if scope == "member" && f.Slots[i].Member != current.Member {
				continue
			}
			previous := f.Slots[i].Candidates[choice]
			counts[0]++
			if candidate.Target >= 0 && candidate.Target == previous.Target {
				counts[1]++
				if previous.Features[0] == 1 || previous.Features[3] == 1 {
					counts[2]++
				}
				for k := 0; k < 4; k++ {
					if previous.Features[16+k] == 1 {
						counts[3+k]++
					}
				}
			}
			if previous.Features[22] == 1 && healingOverlaps(f, previous, candidate) {
				counts[7]++
			}
		}
		for k, count := range counts {
			values[j*battlenet.PlanTargetFeatures+k] = float32(count) / 10
		}
	}
	return values, nil
}

func healingOverlaps(f Frame, heal, candidate Candidate) bool {
	group := func(c Candidate) bool { return c.Features[22] == 1 && c.Features[25] == 1 }
	covered := func(c Candidate, target int) bool {
		if target < 0 || target >= len(f.Entities) {
			return false
		}
		x := f.Entities[target]
		return x[eBench] == 0 && x[eAlive] == 1 && x[eAlly] == c.Features[7]
	}
	if group(heal) {
		if group(candidate) {
			return heal.Features[7] == candidate.Features[7]
		}
		return covered(heal, candidate.Target)
	}
	if group(candidate) {
		return covered(candidate, heal.Target)
	}
	return heal.Target >= 0 && heal.Target == candidate.Target
}
