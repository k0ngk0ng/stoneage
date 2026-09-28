package aigame

import "strings"

// BattleEvent is a combat-only wire observation, not a dump of all server
// traffic. Account, chat and login packets never enter this stream.
type BattleEvent struct {
	Sequence uint64           `json:"sequence"`
	AtMS     int64            `json:"at_ms"`
	MatchID  string           `json:"match_id"`
	Turn     int32            `json:"turn"`
	Function string           `json:"function"`
	Raw      string           `json:"raw,omitempty"`
	Integers []int32          `json:"integers,omitempty"`
	Effects  []BattleLogEntry `json:"effects,omitempty"`
}
type BattleEventBatch struct {
	Stream      string        `json:"stream"`
	Cursor      uint64        `json:"cursor"`
	Gap         bool          `json:"gap"`
	Events      []BattleEvent `json:"events"`
	Observation BattleView    `json:"observation"`
}
type battleEventStream struct {
	identity string
	sequence uint64
	floor    uint64
	bytes    int
	events   []BattleEvent
}

func cloneBattleLogEntries(entries []BattleLogEntry) []BattleLogEntry {
	out := append([]BattleLogEntry{}, entries...)
	for i := range out {
		if out[i].Status != nil {
			value := *out[i].Status
			out[i].Status = &value
		}
		if out[i].Delta != nil {
			value := *out[i].Delta
			out[i].Delta = &value
		}
	}
	return out
}

func battleEventBytes(e BattleEvent) int {
	n := len(e.Raw) + 128
	for _, effect := range e.Effects {
		n += len(effect.Raw) + len(effect.Text) + 160
	}
	return n
}

func (state *gameState) recordBattleEvent(event Event, previousMatchID string, effects ...BattleLogEntry) {
	stream := &state.battleStream
	if stream.identity != state.sessionToken {
		*stream = battleEventStream{identity: state.sessionToken}
	}
	switch event.Function {
	case "EN", "B", "BC", "RS", "RD":
	case "S":
		if !strings.HasPrefix(eventText(event, 0), "BTIME|") {
			return
		}
	default:
		return
	}
	stream.sequence++
	e := BattleEvent{Sequence: stream.sequence, AtMS: event.At.UnixMilli(), MatchID: state.snapshot.Battle.LadderID,
		Turn: state.snapshot.Battle.Turn, Function: event.Function, Effects: append([]BattleLogEntry(nil), effects...)}
	if e.MatchID == "" {
		e.MatchID = previousMatchID
	}
	if event.Function == "EN" {
		e.Integers = []int32{eventInt(event, 0, 0), eventInt(event, 1, 0)}
	} else {
		e.Raw = eventText(event, 0)
	}
	// Bound observer memory independently of an external recorder's speed.
	// Skipped or evicted packets are reported as a gap, never silently hidden.
	if len(e.Raw) > 64*1024 {
		*stream = battleEventStream{identity: stream.identity, sequence: stream.sequence, floor: stream.sequence}
		return
	}
	stream.events = append(stream.events, e)
	stream.bytes += battleEventBytes(e)
	for len(stream.events) > 4096 || stream.bytes > 4*1024*1024 {
		stream.bytes -= battleEventBytes(stream.events[0])
		stream.floor = stream.events[0].Sequence
		stream.events[0] = BattleEvent{}
		stream.events = stream.events[1:]
	}
}

// BattleEvents snapshots both cursor and decision state under the same lock.
// Initial empty stream/cursor requests are allowed; reconnects rotate stream.
func (session *Session) BattleEvents(streamID string, cursor uint64) BattleEventBatch {
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	s := session.state.battleStream
	identity := s.identity
	if identity == "" {
		identity = session.state.sessionToken
	}
	gap := cursor < s.floor || cursor > s.sequence || streamID != "" && streamID != identity || streamID == "" && cursor != 0
	if gap {
		cursor = 0
	}
	out := BattleEventBatch{Stream: identity, Cursor: s.sequence, Gap: gap, Events: []BattleEvent{}, Observation: NewBattleView(session.snapshotLocked())}
	for _, e := range s.events {
		if e.Sequence <= cursor {
			continue
		}
		e.Integers = append([]int32(nil), e.Integers...)
		e.Effects = cloneBattleLogEntries(e.Effects)
		// clone text so retaining one tiny event cannot retain a much larger
		// source buffer from the network decoder.
		e.Raw = strings.Clone(e.Raw)
		out.Events = append(out.Events, e)
	}
	return out
}
