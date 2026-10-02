package aigame

import (
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// BattleReplay projects ordered, already decoded client packet payloads from
// an offline engine. It has no connection, credentials, timers or submission
// methods. The normal client's parser, journal and candidate rules are reused.
// A replay belongs to one participant in one battle and is not concurrent.
type BattleReplay struct {
	session *Session
	mode    int
}

func NewBattleReplay(match string, mode int) (*BattleReplay, error) {
	if match == "" || mode < 1 || mode > 5 {
		return nil, fmt.Errorf("battle replay requires a match identity and mode 1..5")
	}
	s := &Session{state: newGameState(false)}
	s.state.snapshot.Phase = PhaseBattle
	s.state.snapshot.Battle = BattleSnapshot{Active: true, LadderID: match}
	// The explicit native reset is the battle-start boundary. Initialize the
	// shared journal's lifecycle without inventing an EN wire event in the
	// captured stream (the offline process only emits B/S packets).
	s.journal.record(Event{Function: "EN", At: time.Unix(0, 0), Fields: []Field{{Kind: FieldInt, Int: 1}}})
	s.journal.emitted = nil
	return &BattleReplay{s, mode}, nil
}

func (r *BattleReplay) Apply(function string, payload []byte) error {
	if function != "B" && function != "S" {
		return fmt.Errorf("unsupported battle replay function %q", function)
	}
	if len(payload) == 0 || len(payload) > 262144 {
		return fmt.Errorf("battle replay payload outside size limit")
	}
	r.session.applyEvent(Event{Function: function, At: time.Unix(0, 0), Fields: []Field{{
		Kind: FieldString, Text: append([]byte(nil), payload...), Raw: namedproto.EncodeString(payload),
	}}})
	return nil
}

// View sets the authoritative decision turn from the offline engine, not a
// synthetic deadline or a count of repeated BP packets. Merely setting the
// mode does not assert parity with arena rules; the native differential suite
// verifies that separately for its explicit controlled scenarios.
func (r *BattleReplay) View(turn int32) BattleView {
	s := r.session.Snapshot()
	s.Battle.Turn = turn
	v := NewBattleView(s)
	v.Mode = r.mode
	return v
}

// Events uses the same atomic observation/cursor cutoff as a live session.
// turn is the native adapter's authoritative decision counter. Event sequence,
// rather than animation turn labels, establishes ordering within the batch.
func (r *BattleReplay) Events(turn int32, stream string, cursor uint64) BattleEventBatch {
	b := r.session.BattleEvents(stream, cursor)
	b.Observation = r.View(turn)
	return b
}

// Resolve uses the same stale-observation and candidate checks as live play.
func (r *BattleReplay) Resolve(turn int32, choice BattleSelection) (Action, error) {
	s := r.session.Snapshot()
	s.Battle.Turn = turn
	return ResolveBattleSelection(s, choice)
}

// ResolvePlan validates the same player-then-pet submission sequence as the
// live runner, using a private snapshot. No submission/acceptance is invented
// in the replay itself; the native step still decides the effects.
func (r *BattleReplay) ResolvePlan(turn int32, choices []BattleSelection) ([]Action, error) {
	s := r.session.Snapshot()
	s.Battle.Turn = turn
	var actions []Action
	for _, choice := range choices {
		view := NewBattleView(s)
		a, err := ResolveBattleSelection(s, choice)
		if err != nil {
			return nil, err
		}
		for _, c := range view.Candidates {
			if c.ID == choice.CandidateID {
				if c.Actor == "player" {
					s.Battle.PlayerSubmitted = true
				} else {
					s.Battle.PetSubmitted = true
				}
				break
			}
		}
		actions = append(actions, a)
	}
	return actions, nil
}
