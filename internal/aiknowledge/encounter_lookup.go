package aiknowledge

import "slices"

// EncounterAt selects the effective encounter row using the legacy server's
// ENCOUNT_getEncountAreaArray rule: highest positive ZOrder on this floor,
// retaining the first source row on ties. It does not certify referenced
// enemies or apply character-specific event conditions to encounter rates.
func (k *Knowledge) EncounterAt(floor, x, y int) (EncounterArea, bool) {
	selected, ok := k.EncounterIndexAt(floor, x, y)
	if !ok {
		return EncounterArea{}, false
	}
	row := k.Encounters[selected]
	row.GroupIDs = slices.Clone(row.GroupIDs)
	row.GroupProbabilities = slices.Clone(row.GroupProbabilities)
	return row, true
}

// EncounterIndexAt returns the loaded row ordinal, not its non-unique ID.
func (k *Knowledge) EncounterIndexAt(floor, x, y int) (int, bool) {
	if k == nil {
		return -1, false
	}
	selected := -1
	for i, row := range k.Encounters {
		if row.Floor != floor || row.ZOrder <= 0 || !row.Bounds.Contains(x, y) {
			continue
		}
		if selected < 0 || row.ZOrder > k.Encounters[selected].ZOrder {
			selected = i
		}
	}
	if selected < 0 {
		return -1, false
	}
	return selected, true
}

// FindEncounterArea binds derived risk to the selected source row. IDs can
// repeat on different floors or rectangles; FindArea(ID) alone is insufficient
// for movement safety. Ambiguous derived data must remain unavailable.
func (k *Knowledge) FindEncounterArea(index int) (LevelingArea, bool) {
	if k == nil || index < 0 || index >= len(k.Encounters) {
		return LevelingArea{}, false
	}
	r := k.Encounters[index]
	var found *LevelingArea
	for i := range k.Leveling {
		a := &k.Leveling[i]
		if a.ID != r.ID || a.Floor != r.Floor || a.Bounds != r.Bounds {
			continue
		}
		if r.Source.Path != "" && !slices.Contains(a.Evidence, r.Source) {
			continue
		}
		if found != nil {
			return LevelingArea{}, false
		}
		found = a
	}
	if found == nil {
		return LevelingArea{}, false
	}
	return cloneArea(*found), true
}
