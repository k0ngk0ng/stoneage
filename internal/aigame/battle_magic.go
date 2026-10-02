package aigame

import (
	"sort"
	"strconv"
	"strings"
)

// Split before legacy unescaping: spell names may contain escaped pipes.
func (state *gameState) applyMagic(value string) {
	p := strings.Split(value, "|")
	if len(p) < 2 || len(p[0]) < 2 {
		return
	}
	slot, err := strconv.Atoi(p[0][1:])
	if err != nil || slot < 0 || slot >= 5 {
		return
	}
	if p[1] == "0" {
		// Native unequip/no-spell response is only J<slot>|0|.
		state.storeMagic(MagicSnapshot{Index: int32(slot)})
		return
	}
	if len(p) < 7 {
		return
	}
	values := [4]int32{}
	for i := range values {
		values[i] = base62Int(p[i+1], -1)
		if values[i] < 0 {
			return
		}
	}
	entry := MagicSnapshot{Index: int32(slot), UseFlag: values[0], MP: values[1], Field: values[2], Target: values[3] % 100,
		DeadTarget: values[3] >= 100, Name: legacyText(p[5]), Memo: legacyText(p[6])}
	seenID, validID := false, true
	for _, field := range p[7:] {
		if strings.HasPrefix(field, "id=") {
			id, ok := parseSignedDecimal(strings.TrimPrefix(field, "id="))
			validID = validID && !seenID && ok && id >= 0
			seenID, entry.ID = true, id
		}
	}
	entry.IDKnown = seenID && validID
	if !entry.IDKnown {
		entry.ID = 0
	}
	state.storeMagic(entry)
}

func (state *gameState) storeMagic(entry MagicSnapshot) {
	for i := range state.snapshot.Magic {
		if state.snapshot.Magic[i].Index == entry.Index {
			state.snapshot.Magic[i] = entry
			return
		}
	}
	state.snapshot.Magic = append(state.snapshot.Magic, entry)
	sort.Slice(state.snapshot.Magic, func(i, j int) bool { return state.snapshot.Magic[i].Index < state.snapshot.Magic[j].Index })
}
