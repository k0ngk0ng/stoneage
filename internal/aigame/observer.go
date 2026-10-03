package aigame

import (
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// ApplyServerPacket feeds one already framed server packet into this
// connection-local projection. It is intended for protocol bridges which
// already own the TCP reader: the bridge can share the parser/state machine
// without opening a second socket or competing for reads. The packet is not
// published on Events; callers that need the event stream can use a normal
// Session reader instead.
func (session *Session) ApplyServerPacket(packet []byte) error {
	_, err := session.applyServerPacket(packet, false)
	return err
}

// ApplyServerPacketWithLadderView captures the ladder presentation at this
// exact packet boundary. Bridges must queue the returned view with its packet,
// rather than sampling a newer observation when an HTTP poll is delivered.
func (session *Session) ApplyServerPacketWithLadderView(packet []byte) (*LadderPacketView, error) {
	return session.applyServerPacket(packet, true)
}

func (session *Session) applyServerPacket(packet []byte, ladderView bool) (*LadderPacketView, error) {
	if err := session.ensureOpen(); err != nil {
		return nil, err
	}
	event, err := decodeEvent(packet)
	if err != nil {
		return nil, fmt.Errorf("apply server packet: %w", err)
	}
	return session.applyEventWithLadderView(event, ladderView), nil
}

// ApplyClientPacket records identity and local submission state from one
// successfully written client packet. A Web bridge owns the client write and
// the server reader, so this hook keeps its shared observer in sync without
// opening a second login connection. Terminal EO uses the same exit guard as
// typed actions; it cannot establish an unobserved battle result. It does not
// advance the observation revision: only server packets are authoritative
// observations.
func (session *Session) ApplyClientPacket(packet []byte) error {
	if err := session.ensureOpen(); err != nil {
		return err
	}
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return fmt.Errorf("apply client packet: decode packet: %w", err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return fmt.Errorf("apply client packet: parse packet: %w", err)
	}
	switch message.Function {
	case "EO":
		if len(message.Fields) != 1 {
			return fmt.Errorf("apply client packet: malformed EO")
		}
		value, err := namedproto.DecodeInt(message.Fields[0])
		if err != nil || value != 0 {
			return fmt.Errorf("apply client packet: malformed EO")
		}
		session.stateMu.Lock()
		defer session.stateMu.Unlock()
		// EO has no dedicated server acknowledgement. Share the typed
		// action's terminal-state guard and exit transition, so Web observes
		// the same completed battle as the CLI that sent this packet.
		action := Action{Kind: ActionBattleEnd}
		if _, function, err := validateActionLocked(&session.state, action); err == nil {
			applyActionLocked(&session.state, action, function, session.state.snapshot.Revision)
		}
		return nil
	case "TD", "FS", "SKUP", "PR":
		session.stateMu.Lock()
		switch message.Function {
		case "TD":
			invalidateTradeForManualPacket(&session.state)
		case "FS":
			session.state.snapshot.Player.SocialFlagsKnown = false
		case "SKUP":
			session.state.snapshot.Player.StatPointsKnown = false
		case "PR":
			session.state.snapshot.AI.PartyModeKnown = false
		}
		session.stateMu.Unlock()
		return nil
	case "WN":
		// The native client destroys a server window the moment its answer is
		// queued, and the server is under no obligation to send a replacement.
		// Without this the projection keeps the window open long after the
		// player dealt with it, and anything waiting for a free character --
		// the auto battle walk is one -- waits forever.
		session.stateMu.Lock()
		answerWindowPacketLocked(&session.state, message)
		session.stateMu.Unlock()
		return nil
	}
	if message.Function != "ClientLogin" && message.Function != "CharLogin" {
		return nil
	}
	if len(message.Fields) == 0 {
		return fmt.Errorf("apply client packet: %s has no identity field", message.Function)
	}
	value, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		return fmt.Errorf("apply client packet: %s identity: %w", message.Function, err)
	}
	identity := decodeLegacyBytes(value)
	if message.Function == "CharLogin" {
		identity = legacyText(identity)
	}
	if strings.TrimSpace(identity) == "" {
		return nil
	}
	session.stateMu.Lock()
	if message.Function == "ClientLogin" {
		session.state.snapshot.Account = identity
		session.state.snapshot.Connected = true
	} else {
		session.state.pendingChatIdentity = nil
		session.state.snapshot.Character = identity
		session.state.snapshot.AI.PersistentCharacterID = ""
	}
	session.stateMu.Unlock()
	return nil
}

// answerWindowPacketLocked records a client WN response in the projection. The
// wire form is WN|x|y|seqno|objindex|select|data, so the third field names the
// window the player answered; the rest belongs to the server's bookkeeping.
func answerWindowPacketLocked(state *gameState, message namedproto.Message) {
	if len(message.Fields) < 3 {
		return
	}
	sequence, err := namedproto.DecodeInt(message.Fields[2])
	if err != nil {
		return
	}
	// A sequence that matches nothing is an answer to a window this projection
	// never saw; that is not a state change worth inventing.
	if window := state.activeWindow; window != nil && window.Open && window.Sequence == sequence {
		markWindowSubmittedLocked(state, window)
		for index := range state.windows {
			if state.windows[index].Sequence == sequence {
				state.windows[index].Submitted = true
			}
		}
		return
	}
	for index := range state.windows {
		if state.windows[index].Open && state.windows[index].Sequence == sequence {
			state.windows[index].Submitted = true
		}
	}
}
