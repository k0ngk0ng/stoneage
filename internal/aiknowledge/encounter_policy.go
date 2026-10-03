package aiknowledge

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// EncounterPolicyToken matches the loaded native encounter rows (in lookup
// order). This is a compatibility checksum, not an
// authentication mechanism. It must arrive through the authenticated session;
// local missing data alone never establishes the server's spawn behavior.
func (k *Knowledge) EncounterPolicyToken() string {
	if k == nil || len(k.Encounters) == 0 {
		return ""
	}
	h := fnv.New64a()
	write := func(values ...int) {
		var b [4]byte
		for _, v := range values {
			binary.LittleEndian.PutUint32(b[:], uint32(v))
			_, _ = h.Write(b[:])
		}
	}
	write(len(k.Encounters))
	for _, r := range k.Encounters {
		if len(r.GroupIDs) != 10 || len(r.GroupProbabilities) != 10 || r.Bounds.X2 != r.Bounds.X+r.Bounds.Width || r.Bounds.Y2 != r.Bounds.Y+r.Bounds.Height {
			return ""
		}
		write(r.ID, r.Floor, r.Bounds.X, r.Bounds.Y, r.Bounds.Width, r.Bounds.Height,
			r.EncounterProbability.Min, r.EncounterProbability.Max, r.MaxEnemies, r.ZOrder,
			r.EventNow, r.EventEnd, r.EnemyGroup)
		for i, id := range r.GroupIDs {
			write(id, r.GroupProbabilities[i])
		}
	}
	return fmt.Sprintf("missing-group-abort-v1:%016x", h.Sum64())
}

// NonSpawningEncounters only applies the patched native unconditional abort
// for missing normal groups, as reported by the native loaded group table.
// The native loader can discard groups; the local source table cannot prove
// which groups were actually loaded. Missing event groups/enemies and
// item-dependent eligibility do not grant an exemption.
func (k *Knowledge) NonSpawningEncounters(observedToken string) map[int]bool {
	parts := strings.Split(observedToken, ":")
	if len(parts) != 3 || parts[0]+":"+parts[1] != k.EncounterPolicyToken() {
		return nil
	}
	if parts[2] == "-" {
		return nil
	}
	out := make(map[int]bool)
	for _, raw := range strings.Split(parts[2], ",") {
		id, err := strconv.Atoi(raw)
		if err != nil || id < 0 || id >= len(k.Encounters) || out[id] || strconv.Itoa(id) != raw {
			return nil
		}
		out[id] = true
	}
	return out
}
