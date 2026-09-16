package aiknowledge

import "slices"

// EncounterAt selects the effective encounter row using the legacy server's
// ENCOUNT_getEncountAreaArray rule: highest positive ZOrder on this floor,
// retaining the first source row on ties. It does not certify referenced
// enemies or apply character-specific event conditions to encounter rates.
func (k *Knowledge) EncounterAt(floor, x, y int) (EncounterArea, bool) {
	if k == nil {
		return EncounterArea{}, false
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
		return EncounterArea{}, false
	}
	row := k.Encounters[selected]
	row.GroupIDs = slices.Clone(row.GroupIDs)
	row.GroupProbabilities = slices.Clone(row.GroupProbabilities)
	return row, true
}
