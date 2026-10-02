package aigame

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

// LadderPacketView contains only the canonical state needed by native battle
// presentation. It is immutable after capture and follows the native packet
// through bridge replay and client animation queues. No client decodes the
// ladder wire format or accepted-command mask independently.
type LadderPacketView struct {
	MatchID  string             `json:"match_id,omitempty"`
	Envelope *ladder.Envelope   `json:"envelope,omitempty"`
	Commands *LadderCommandView `json:"commands,omitempty"`
}

type LadderCommandView struct {
	Turn            int32 `json:"turn"`
	MyNo            int32 `json:"my_no"`
	PlayerSubmitted bool  `json:"player_submitted"`
	PetSubmitted    bool  `json:"pet_submitted"`
	Withdrawn       bool  `json:"withdrawn"`
}

func (state *gameState) ladderPacketView(event Event, previous *ladder.Envelope) *LadderPacketView {
	battle := state.snapshot.Battle
	if event.Function == "S" && state.snapshot.Ladder != previous && state.snapshot.Ladder != nil {
		e := state.snapshot.Ladder.Clone()
		return &LadderPacketView{Envelope: &e}
	}
	if battle.LadderID == "" || !battle.Active {
		return nil
	}
	if event.Function == "EN" {
		return &LadderPacketView{MatchID: battle.LadderID}
	}
	if event.Function == "B" && strings.HasPrefix(eventText(event, 0), "BA|") && battle.MyNoKnown {
		return &LadderPacketView{MatchID: battle.LadderID, Commands: &LadderCommandView{
			Turn: battle.Turn, MyNo: battle.MyNo,
			PlayerSubmitted: battle.PlayerSubmitted, PetSubmitted: battle.PetSubmitted,
			Withdrawn: battle.Withdrawn(),
		}}
	}
	return nil
}

func Ladder(request ladder.Request) Action {
	return Action{Kind: ActionLadder, LadderRequest: &request}
}

func (state *gameState) applyLadder(wire string) {
	e, err := ladder.Decode(wire)
	if err != nil {
		state.snapshot.LastError = fmt.Sprintf("ladder: %v", err)
		return
	}
	// Store replies independently: a following room event must not erase the
	// receipt before its caller wakes. Late replies never rewind the snapshot.
	if e.RequestID != "" {
		state.ladderReplies = append(state.ladderReplies, e)
		if len(state.ladderReplies) > 64 {
			state.ladderReplies = state.ladderReplies[len(state.ladderReplies)-64:]
		}
	}
	// A historical lookup is a receipt, not the character's current result.
	if e.Event == "result_lookup" || e.Event == "contacts_lookup" {
		return
	}
	previous := state.snapshot.Ladder
	if previous == nil || e.Revision > previous.Revision || (e.Revision == previous.Revision && e.ServerTimeMS >= previous.ServerTimeMS) {
		state.snapshot.Ladder = &e
		if state.snapshot.Battle.Active && e.Snapshot.Phase == "battle" && e.Snapshot.Match != nil {
			state.snapshot.Battle.LadderID = e.Snapshot.Match.ID
		}
	}
	if e.Sequence == 0 || (len(state.ladderEvents) > 0 && e.Sequence <= state.ladderEvents[len(state.ladderEvents)-1].Sequence) {
		return
	}
	if len(state.ladderEvents) == 0 {
		// Nothing before the first received snapshot is known to this session.
		state.ladderFloor = e.Sequence
	}
	state.ladderEvents = append(state.ladderEvents, e)
	if len(state.ladderEvents) > 128 {
		state.ladderFloor = state.ladderEvents[0].Sequence
		state.ladderEvents = state.ladderEvents[1:]
	}
}

// LadderEvents copies the available replay and full projection without
// waiting or consuming any events. Closed sessions retain their last state.
func (session *Session) LadderEvents(stream string, cursor uint64) ladder.Events {
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	state := &session.state
	out := ladder.Events{Stream: state.sessionToken, Events: []ladder.Envelope{}}
	if state.snapshot.Ladder == nil {
		return out
	}
	e := state.snapshot.Ladder.Clone()
	out.Snapshot, out.Cursor = &e, e.Sequence
	out.Gap = cursor > 0 && (stream != out.Stream || cursor < state.ladderFloor || cursor > out.Cursor)
	if !out.Gap {
		for _, event := range state.ladderEvents {
			if event.Sequence > cursor {
				out.Events = append(out.Events, event.Clone())
			}
		}
	}
	return out
}

// WaitLadderEvents never consumes the socket event channel. It works for both
// native sessions and the Web observer, and returns the latest snapshot even
// on a deadline. Cancellation/closed connections remain errors, not timeouts.
// Callers acquire an authoritative status first when attaching a new session.
func (session *Session) WaitLadderEvents(ctx context.Context, stream string, cursor uint64) (ladder.Events, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		out := session.LadderEvents(stream, cursor)
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				out.TimedOut = true
				return out, nil
			}
			return out, err
		}
		if err := session.ensureOpen(); err != nil {
			return out, err
		}
		if out.Snapshot != nil && (cursor == 0 || out.Gap || len(out.Events) > 0) {
			return out, nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// WaitLadderReply observes an authoritative reply, not a successful socket
// write. On timeout the caller retains the request ID and its original
// revision to reconcile/retry that exact request without a second mutation.
func (session *Session) WaitLadderReply(ctx context.Context, request ladder.Request) (ladder.Envelope, error) {
	wire, err := request.Wire()
	if err != nil {
		return ladder.Envelope{}, err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		session.stateMu.RLock()
		var reply *ladder.Envelope
		for i := len(session.state.ladderReplies) - 1; i >= 0; i-- {
			if session.state.ladderReplies[i].RequestID == request.ID && session.state.ladderReplies[i].RequestWire == wire {
				e := session.state.ladderReplies[i].Clone()
				reply = &e
				break
			}
		}
		session.stateMu.RUnlock()
		if reply != nil {
			return *reply, nil
		}
		if err := session.ensureOpen(); err != nil {
			return ladder.Envelope{}, err
		}
		select {
		case <-ctx.Done():
			return ladder.Envelope{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (session *Session) RequestLadder(ctx context.Context, request ladder.Request) (ladder.Envelope, error) {
	if err := session.Do(ctx, Ladder(request)); err != nil {
		return ladder.Envelope{}, err
	}
	return session.WaitLadderReply(ctx, request)
}
