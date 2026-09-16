// Package characterbuild contains the server-owned policy used to allocate
// character stat points. The policy only selects a native attribute index from
// an authoritative observation; it does not submit a game action.
package characterbuild

import "errors"

// Policy balances observed base attributes toward explicit weights. It does
// not prescribe a combat role or derive weights from personality. A nil policy
// leaves points unallocated by an autonomous player.
type Policy struct {
	Weights       Weights `json:"weights"`
	ReservePoints int     `json:"reserve_points"`
}

// Weights are the relative priorities for the four native base attributes.
type Weights struct {
	Vital     int `json:"vital"`
	Strength  int `json:"strength"`
	Toughness int `json:"toughness"`
	Dexterity int `json:"dexterity"`
}

// Values returns weights in native SKUP order: vital, strength, toughness,
// dexterity.
func (w Weights) Values() [4]int {
	return [4]int{w.Vital, w.Strength, w.Toughness, w.Dexterity}
}

func (p *Policy) Validate() error {
	if p == nil {
		return nil
	}
	if p.ReservePoints < 0 || p.ReservePoints > 1000 {
		return errors.New("character_build.reserve_points must be between 0 and 1000")
	}
	total := 0
	for _, weight := range p.Weights.Values() {
		if weight < 0 || weight > 100 {
			return errors.New("character_build weights must be between 0 and 100")
		}
		total += weight
	}
	if total == 0 {
		return errors.New("character_build requires at least one positive weight")
	}
	return nil
}

// Clone returns an independent copy of the policy. The policy currently only
// contains value fields, but keeping cloning here makes the ownership boundary
// explicit for callers that receive configuration from another subsystem.
func (p *Policy) Clone() *Policy {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// NextAttribute returns one native SKUP index. Compare ratios using integer
// cross-products; a tie prefers the lower native index. Unknown attributes
// and invalid policy/state never authorize an allocation. The caller must
// also validate the observation revision and normal game action constraints.
func (p *Policy) NextAttribute(attributes [4]int32, points int32, known bool) (int, bool) {
	if p == nil || p.Validate() != nil || !known || points <= int32(p.ReservePoints) {
		return 0, false
	}
	weights := p.Weights.Values()
	best := -1
	for i, attribute := range attributes {
		if attribute <= 0 {
			return 0, false
		}
		if weights[i] == 0 {
			continue
		}
		if best < 0 || int64(attribute)*int64(weights[best]) < int64(attributes[best])*int64(weights[i]) {
			best = i
		}
	}
	return best, best >= 0
}
