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
	if err := session.ensureOpen(); err != nil {
		return err
	}
	event, err := decodeEvent(packet)
	if err != nil {
		return fmt.Errorf("apply server packet: %w", err)
	}
	session.applyEvent(event)
	return nil
}

// ApplyClientPacket records the identity fields from one already framed
// client packet.  A Web bridge owns the client write and the server reader, so
// this small hook lets its shared observer bind the authenticated account and
// selected character without opening a second login connection.  It does not
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
