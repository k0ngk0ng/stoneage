package battlepolicy

import (
	"fmt"
	"math"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// History is one designated observer's incremental stream, not the union of
// every member's identical battle broadcasts. Its observation must match that
// observer's decision cutoff. Missing history is explicit, never fabricated.
type History struct {
	Batch          *aigame.BattleEventBatch
	PreviousStream string
	PreviousCursor uint64
	First          bool
	// A live stream may already contain earlier matches. A nonzero initial
	// cursor is accepted only when the first retained event proves the start
	// of THIS match at turn zero; it cannot be used to hide missing turns.
	InitialCursor uint64
}

// EncodeHistory validates the observer cutoff and produces the per-decision
// summary. EncodeHistorySequence additionally retains ordered public effects.
// Columns: attack ally/enemy, guard ally/enemy, HP delta ally/enemy,
// MP delta ally/enemy, status count ally/enemy, unknown count, effect count,
// stream gap, initial history, counter ally/enemy.
func EncodeHistory(observer aigame.BattleView, h History) ([EventFeatures]float32, error) {
	return EncodeHistoryVersion(observer, h, FeatureVersion)
}

func EncodeHistoryVersion(observer aigame.BattleView, h History, features string) ([EventFeatures]float32, error) {
	var x [EventFeatures]float32
	if !SupportedFeatures(features) {
		return x, fmt.Errorf("unsupported history features %q", features)
	}
	x[13] = flag(h.First)
	x[127] = 1 // Summary token, distinguished from individual effects.
	if h.Batch == nil {
		x[12] = 1
		return x, nil
	}
	b := h.Batch
	if b.Observation.ID != observer.ID || b.Observation.MatchID != observer.MatchID || b.Observation.Turn != observer.Turn || b.Observation.Battle.MyNo != observer.Battle.MyNo || b.Stream == "" {
		return x, fmt.Errorf("history observation does not match designated observer cutoff")
	}
	if h.InitialCursor != 0 {
		if !h.First || observer.Turn != 0 || len(b.Events) == 0 {
			return x, fmt.Errorf("initial cursor requires observed battle start")
		}
		start := b.Events[0]
		if h.InitialCursor == ^uint64(0) || start.Sequence != h.InitialCursor+1 || start.MatchID != observer.MatchID || start.Function != "EN" || len(start.Integers) == 0 || start.Integers[0] <= 0 {
			return x, fmt.Errorf("initial cursor does not precede this battle's EN")
		}
	}
	gap := b.Gap || !h.First && (h.PreviousStream == "" || b.Stream != h.PreviousStream) || h.First && (h.PreviousCursor != 0 || h.PreviousStream != "")
	if !gap && b.Cursor < h.PreviousCursor {
		return x, fmt.Errorf("history cursor moved backwards")
	}
	previous := h.PreviousCursor
	if gap {
		previous = 0
	} else if h.First {
		previous = h.InitialCursor
	}
	var sums [EventFeatures]float64
	side := int(observer.Battle.MyNo) / 10
	relation := func(id int) (int, bool) {
		if id < 0 || id >= 20 {
			return 0, false
		}
		if id/10 == side {
			return 0, true
		}
		return 1, true
	}
	for _, event := range b.Events {
		if event.Sequence <= previous || event.Sequence > b.Cursor {
			return x, fmt.Errorf("unordered, repeated or future history event")
		}
		if event.Sequence != previous+1 {
			gap = true
		}
		previous = event.Sequence
		if event.MatchID != observer.MatchID {
			// A live stream can include the end of the previous battle on the
			// initial read. Its sequence still counts; its effects never do.
			if h.First {
				continue
			}
			return x, fmt.Errorf("mixed battle history")
		}
		for _, e := range event.Effects {
			sums[11]++
			a, actorOK := relation(e.Actor)
			t, targetOK := relation(e.Target)
			switch e.Kind {
			case "wait", "pet_recall", "pet_summon":
				if actorOK {
					column := 16
					if e.Kind == "pet_recall" {
						column = 18
					}
					if e.Kind == "pet_summon" {
						column = 20
					}
					sums[column+a]++
				} else {
					sums[10]++
				}
			case "attack", "counter":
				if actorOK {
					if e.Kind == "counter" {
						sums[14+a]++
					} else {
						sums[a]++
					}
				}
				// Preserve the v6 feature contract: old trajectories lack typed
				// recipients. Adopting the new public recipient fields requires
				// an explicit feature migration, not reinterpretation of v6 data.
				if features == LegacyFeatureVersion && e.Flags&(512|1024) != 0 {
					sums[10]++
					continue
				}
				if features != LegacyFeatureVersion && e.Flags&(32|4096) == 0 {
					if e.Recipient == nil {
						targetOK = false
					} else {
						t, targetOK = relation(*e.Recipient)
					}
					if !targetOK {
						sums[10]++
					}
				}
				if targetOK && e.Flags&(32|4096) == 0 {
					delta := -float64(e.Damage)
					if e.Flags&2048 != 0 {
						delta = -delta
					}
					sums[4+t] += delta
				}
			case "BD":
				if !targetOK || e.Delta == nil {
					sums[10]++
					continue
				}
				if e.Resource == "hp" {
					sums[4+t] += float64(*e.Delta)
				} else if e.Resource == "mp" {
					sums[6+t] += float64(*e.Delta)
				} else {
					sums[10]++
				}
			case "BM":
				if targetOK && e.Status != nil {
					sums[8+t]++
				} else {
					sums[10]++
				}
			case "BG", "bg":
				if actorOK {
					sums[2+a]++
				} else {
					sums[10]++
				}
			default:
				sums[10]++
			}
		}
	}
	if previous != b.Cursor {
		gap = true
	}
	for i, n := range sums {
		x[i] = float32(math.Copysign(math.Log1p(math.Abs(n))/math.Log(1001), n))
	}
	x[12], x[13] = flag(gap), flag(h.First)
	x[127] = 1
	return x, nil
}

// EncodeHistorySequence preserves public packet/effect order, not timestamps
// or guessed engine timings. It never sorts effects, parses text or reads
// hidden state. Unresolved effects remain tokens in their original position.
func EncodeHistorySequence(observer aigame.BattleView, h History) ([EventFeatures]float32, [][EventFeatures]float32, error) {
	return EncodeHistorySequenceVersion(observer, h, FeatureVersion)
}

func EncodeHistorySequenceVersion(observer aigame.BattleView, h History, features string) ([EventFeatures]float32, [][EventFeatures]float32, error) {
	summary, err := EncodeHistoryVersion(observer, h, features)
	if err != nil || h.Batch == nil {
		return summary, nil, err
	}
	var sequence [][EventFeatures]float32
	for _, event := range h.Batch.Events {
		if event.MatchID != observer.MatchID {
			continue
		}
		for _, effect := range event.Effects {
			if len(sequence) == MaxHistoryEffects {
				return summary, nil, fmt.Errorf("history exceeds %d effects at one decision; refusing silent truncation", MaxHistoryEffects)
			}
			sequence = append(sequence, encodeEffectVersion(effect, int(observer.Battle.MyNo)/10, features))
		}
	}
	return summary, sequence, nil
}

// Columns: 0..7 effect classes; 8..13 known flags; 14..15 hit indexes;
// 16..35 actor, 36..55 intended target, 56..75 resolved recipient (canonical
// battle slots, not current entity rows); 76..79 HP/MP/pet deltas and pet-known;
// 80..95 hit flags; 96..103 status; 104..105 pet recall/summon;
// v7 uses 106..125 for guardian slots and 126 for guardian-known; 127 summary.
func encodeEffect(e aigame.BattleLogEntry, side int) [EventFeatures]float32 {
	return encodeEffectVersion(e, side, FeatureVersion)
}

func encodeEffectVersion(e aigame.BattleLogEntry, side int, features string) [EventFeatures]float32 {
	var x [EventFeatures]float32
	putID := func(id, offset, known int) bool {
		if id < 0 || id >= 20 {
			return false
		}
		x[offset+canonical(int32(id), side)] = 1
		x[known] = 1
		return true
	}
	// Only fields established for the given effect kind count as known; zero
	// values in an unknown/old record cannot invent actor slot zero.
	switch e.Kind {
	case "wait", "pet_recall", "pet_summon":
		column := 7
		if e.Kind == "pet_recall" {
			column = 104
		}
		if e.Kind == "pet_summon" {
			column = 105
		}
		x[column] = 1
		if !putID(e.Actor, 16, 8) {
			x[6] = 1
		}
		if e.Kind != "wait" {
			putID(e.Target, 36, 9)
			putID(e.Target, 56, 10)
		}
	case "attack", "counter":
		if e.Kind == "attack" {
			x[0] = 1
		} else {
			x[1] = 1
		}
		putID(e.Actor, 16, 8)
		putID(e.Target, 36, 9)
		for i := 0; i < 16; i++ {
			x[80+i] = flag(e.Flags&(1<<i) != 0)
		}
		x[14], x[15] = scale(int32(e.Hit)), scale(int32(e.Hits))
		// v6 retains its original unresolved semantics even on newer journals.
		if features == LegacyFeatureVersion && e.Flags&(512|1024) != 0 {
			x[6] = 1
			break
		}
		recipient := e.Target
		if features != LegacyFeatureVersion {
			if e.Flags&512 != 0 {
				if e.Guardian == nil || !putID(*e.Guardian, 106, 126) {
					x[6] = 1
				}
			}
			if e.Flags&(32|4096) != 0 {
				// The movie reports no applied damage and no damage recipient.
				x[11] = 1
				break
			}
			if e.Recipient == nil {
				x[6] = 1
				break
			}
			recipient = *e.Recipient
		}
		if !putID(recipient, 56, 10) {
			x[6] = 1
			break
		}
		x[11] = 1
		if e.Flags&(32|4096) == 0 {
			sign := int32(-1)
			if e.Flags&2048 != 0 {
				sign = 1
			}
			x[76] = scale(sign * int32(e.Damage))
			x[78], x[79] = scale(sign*int32(e.PetDamage)), 1
		}
	case "BD":
		putID(e.Target, 36, 9)
		if e.Delta == nil || !putID(e.Target, 56, 10) {
			x[6] = 1
			break
		}
		switch e.Resource {
		case "hp":
			x[2], x[11], x[76] = 1, 1, scale(int32(*e.Delta))
		case "mp":
			x[3], x[12], x[77] = 1, 1, scale(int32(*e.Delta))
		default:
			x[6] = 1
		}
	case "BM":
		x[4] = 1
		putID(e.Target, 36, 9)
		putID(e.Target, 56, 10)
		if e.Status == nil {
			x[6] = 1
			break
		}
		x[13] = 1
		status := *e.Status
		if status < 0 || status > 6 {
			status = 7
		}
		x[96+status] = 1
	case "BG", "bg":
		x[5] = 1
		if !putID(e.Actor, 16, 8) {
			x[6] = 1
		}
	default:
		x[6] = 1
	}
	return x
}
