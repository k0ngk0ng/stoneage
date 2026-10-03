package aigame

import (
	"slices"
	"strconv"
	"strings"
)

// CaptureObservation is a read-only quote from the current native PvE battle.
// Eligible excludes known static refusals; it never promises capture success.
// Consumption lists EVERY owned slot native capture will delete on success,
// not merely one item for each required template.
type CaptureObservation struct {
	RequestID                          string
	Revision                           uint64
	Active                             bool
	BattleIndex, Turn, Self, FreeSlots int32
	Targets                            []CaptureTarget
}

type CaptureTarget struct {
	Slot, SpeciesID, Level, Graphic int32
	Eligible                        bool
	RequiredItems                   []int32
	Consumption                     []AIInventoryItem
}

func cloneCaptureObservation(source *CaptureObservation) *CaptureObservation {
	if source == nil {
		return nil
	}
	result := *source
	result.Targets = slices.Clone(source.Targets)
	for i := range result.Targets {
		result.Targets[i].RequiredItems = slices.Clone(source.Targets[i].RequiredItems)
		result.Targets[i].Consumption = slices.Clone(source.Targets[i].Consumption)
	}
	return &result
}

func parseCaptureObservation(value string) (*CaptureObservation, bool) {
	parts := strings.Split(value, "|")
	if len(parts) < 3 || len(value) > 16383 || parts[0] != "BCAP" {
		return nil, false
	}
	result := &CaptureObservation{}
	seen := map[string]bool{}
	targets := map[int32]bool{}
	number := func(value string) (int32, bool) {
		if value == "" || strings.Trim(value, "0123456789") != "" {
			return 0, false
		}
		n, err := strconv.ParseInt(value, 10, 32)
		return int32(n), err == nil
	}
	for _, field := range parts[1:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" || (key != "target" && seen[key]) {
			return nil, false
		}
		seen[key] = true
		switch key {
		case "v":
			if value != "1" {
				return nil, false
			}
		case "request":
			if !validAIRequestID(value) {
				return nil, false
			}
			result.RequestID = value
		case "active":
			if value != "0" && value != "1" {
				return nil, false
			}
			result.Active = value == "1"
		case "battle", "turn", "self", "free":
			n, valid := number(value)
			if !valid {
				return nil, false
			}
			switch key {
			case "battle":
				result.BattleIndex = n
			case "turn":
				result.Turn = n
			case "self":
				result.Self = n
			case "free":
				result.FreeSlots = n
			}
		case "target":
			v := strings.Split(value, ",")
			if len(v) != 7 || len(targets) >= 10 {
				return nil, false
			}
			var ns [5]int32
			for i := range ns {
				n, valid := number(v[i])
				if !valid {
					return nil, false
				}
				ns[i] = n
			}
			if ns[0] >= 20 || targets[ns[0]] || ns[2] < 1 || ns[3] < 1 || ns[4] > 1 {
				return nil, false
			}
			targets[ns[0]] = true
			target := CaptureTarget{Slot: ns[0], SpeciesID: ns[1], Level: ns[2], Graphic: ns[3], Eligible: ns[4] == 1}
			needed := map[int32]bool{}
			if v[5] != "-" {
				for _, text := range strings.Split(v[5], ";") {
					id, valid := number(text)
					if !valid || needed[id] || len(needed) >= 15 {
						return nil, false
					}
					needed[id] = true
					target.RequiredItems = append(target.RequiredItems, id)
				}
			}
			used := map[int32]bool{}
			if v[6] != "-" {
				for _, text := range strings.Split(v[6], ";") {
					a, b, ok := strings.Cut(text, ":")
					if !ok {
						return nil, false
					}
					slot, sok := number(a)
					id, iok := number(b)
					if !sok || !iok || slot >= 20 || used[slot] || !needed[id] {
						return nil, false
					}
					used[slot] = true
					target.Consumption = append(target.Consumption, AIInventoryItem{Slot: slot, TemplateID: id})
				}
			}
			if target.Eligible {
				for id := range needed {
					present := false
					for _, item := range target.Consumption {
						present = present || item.TemplateID == id
					}
					if !present {
						return nil, false
					}
				}
			}
			result.Targets = append(result.Targets, target)
		default:
			return nil, false
		}
	}
	if !seen["v"] || !seen["active"] {
		return nil, false
	}
	if !result.Active {
		if seen["battle"] || seen["turn"] || seen["self"] || seen["free"] || seen["target"] {
			return nil, false
		}
		return result, true
	}
	if !seen["battle"] || !seen["turn"] || !seen["self"] || !seen["free"] || result.Self >= 20 || result.FreeSlots > 5 {
		return nil, false
	}
	for _, target := range result.Targets {
		if target.Slot/10 == result.Self/10 || (result.FreeSlots == 0 && target.Eligible) {
			return nil, false
		}
	}
	return result, true
}

func (state *gameState) applyCaptureObservation(value string) {
	result, ok := parseCaptureObservation(value)
	if !ok {
		state.snapshot.Capture = nil
		return
	}
	b := state.snapshot.Battle
	if result.Active && (!state.snapshot.Connected || state.snapshot.Phase != PhaseBattle || !b.Active || b.Ended || b.Result != "" || !b.MyNoKnown || result.Self != b.MyNo || result.Turn != b.Turn) {
		state.snapshot.Capture = nil
		return
	}
	// applyEvent increments the snapshot revision after this reducer returns.
	// Correlated callers must see the response's revision, not its predecessor.
	result.Revision = state.snapshot.Revision + 1
	state.snapshot.Capture = result
}
