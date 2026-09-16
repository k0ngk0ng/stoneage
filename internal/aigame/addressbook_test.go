package aigame

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func addressBookRecord(fields ...string) string {
	return strings.Join(fields, "|")
}

func addressBookEvent(value string) Event {
	return Event{Function: "AB", At: time.Now(), Fields: []Field{{Kind: FieldString, Text: []byte(value)}}}
}

func addressBookItemEvent(index int32, value string) Event {
	return Event{Function: "ABI", At: time.Now(), Fields: []Field{{Kind: FieldInt, Int: index}, {Kind: FieldString, Text: []byte(value)}}}
}

func TestParseAddressBookTableFormatsAndEscapes(t *testing.T) {
	old := strings.Join([]string{
		addressBookRecord("1", `A\zB`, "10", "20", "1", "30", "2"),
		strings.Repeat("|", addressBookOldFields-1),
	}, "|")
	entries, ok := parseAddressBookTable(old)
	if !ok || len(entries) != 2 {
		t.Fatalf("old address book parse = %v, %d entries", ok, len(entries))
	}
	if entries[0].Index != 0 || !entries[0].Use || entries[0].Name != "A|B" || entries[0].Level != 10 || !entries[0].Online {
		t.Fatalf("old address book entry = %+v", entries[0])
	}
	if entries[1].Use || entries[1].Name != "" {
		t.Fatalf("empty old address book entry = %+v", entries[1])
	}

	newFormat := strings.Join([]string{
		addressBookRecord("1", "New", "11", "21", "1", "31", "3", "0"),
		strings.Repeat("|", addressBookNewFields-1),
	}, "|")
	entries, ok = parseAddressBookTable(newFormat + "|")
	if !ok || len(entries) != 2 || entries[0].Name != "New" || entries[0].Level != 11 || entries[1].Use {
		t.Fatalf("new address book parse = %v, %+v", ok, entries)
	}
}

func TestAddressBookLifecycleAndIncrementalUpdates(t *testing.T) {
	state := newGameState(true)
	initialSessionToken := state.snapshot.SessionToken
	if state.snapshot.AddressBookRevision != 0 {
		t.Fatalf("initial address-book revision = %d", state.snapshot.AddressBookRevision)
	}
	applyEventLocked(&state, addressBookItemEvent(0, "1|Before|1|2|1|3|0"))
	if state.addressBookKnown || len(state.addressBook) != 0 || state.snapshot.AddressBookRevision != 0 {
		t.Fatalf("ABI populated an unknown address book: known=%v entries=%v", state.addressBookKnown, state.addressBook)
	}

	full := addressBookRecord("1", "Alice", "10", "20", "1", "30", "2")
	applyEventLocked(&state, addressBookEvent(full))
	if !state.addressBookKnown || len(state.addressBook) != 1 || state.snapshot.AddressBookRevision != 1 {
		t.Fatalf("AB did not establish address book: known=%v entries=%v", state.addressBookKnown, state.addressBook)
	}
	revision := state.snapshot.AddressBookRevision
	applyEventLocked(&state, addressBookItemEvent(0, "1|Alice|12|22|0|30|2"))
	entry, ok := state.addressBook[0]
	if !ok || entry.Level != 12 || entry.Online || state.snapshot.AddressBookRevision != revision {
		t.Fatalf("ABI did not update known slot: %+v", entry)
	}

	applyEventLocked(&state, stringEvent("CharLogout", "successful"))
	if state.addressBookKnown || len(state.addressBook) != 0 || state.snapshot.AddressBookRevision != 0 {
		t.Fatalf("logout retained address book: known=%v entries=%v", state.addressBookKnown, state.addressBook)
	}
	if state.snapshot.SessionToken == initialSessionToken {
		t.Fatal("character logout retained session token")
	}
	state.snapshot.AddressBookRevision = 9
	state.addressBookRevision = 9
	applyEventLocked(&state, addressBookEvent("malformed"))
	if state.addressBookKnown || len(state.addressBook) != 0 || state.snapshot.AddressBookRevision != 9 {
		t.Fatalf("malformed AB was accepted: known=%v entries=%v", state.addressBookKnown, state.addressBook)
	}
}

func TestAddressBookSessionTokensAreDistinct(t *testing.T) {
	first, second := newGameState(true), newGameState(true)
	if first.snapshot.SessionToken == "" || second.snapshot.SessionToken == "" || first.snapshot.SessionToken == second.snapshot.SessionToken {
		t.Fatalf("session tokens are not distinct: %q %q", first.snapshot.SessionToken, second.snapshot.SessionToken)
	}
}

func TestMailActionValidation(t *testing.T) {
	state := newGameState(true)
	state.snapshot.Phase = PhaseWorld
	state.snapshot.Position = Point{Floor: 100, X: 4, Y: 5}
	state.addressBookKnown = true
	state.addressBook[3] = AddressBookEntry{Index: 3, Use: true, Name: "Alice"}

	values, function, err := validateActionLocked(&state, Mailbox())
	if err != nil || function != "AB" || len(values) != 0 {
		t.Fatalf("mail list = values=%v function=%q error=%v", values, function, err)
	}
	values, function, err = validateActionLocked(&state, AddMailContact(4, 5))
	if err != nil || function != "AAB" || len(values) != 2 || values[0].integer != 4 || values[1].integer != 5 {
		t.Fatalf("mail add = values=%v function=%q error=%v", values, function, err)
	}
	values, function, err = validateActionLocked(&state, Mail(3, "hello", 2))
	if err != nil || function != "MSG" || len(values) != 3 || values[0].integer != 3 || string(values[1].text) != "hello" || values[2].integer != 2 {
		t.Fatalf("mail send = values=%v function=%q error=%v", values, function, err)
	}
	if _, _, err := validateActionLocked(&state, Mail(4, "hello", 2)); err == nil {
		t.Fatal("mail send to an unused slot was accepted")
	}
	if _, _, err := validateActionLocked(&state, Mail(80, "hello", 2)); err == nil {
		t.Fatal("mail send to an out-of-range slot was accepted")
	}
}

func TestMailWireAndIncomingMessagesUseTypedChatChannels(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	outgoing, err := session.buildPacket("MSG", []wireValue{{kind: wireInt, integer: 3}, {kind: wireString, text: []byte("hello")}, {kind: wireInt, integer: 2}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeEvent(outgoing)
	if err != nil || decoded.Function != "MSG" || len(decoded.Fields) != 3 || decoded.Fields[0].Int != 3 || eventText(decoded, 1) != "hello" || decoded.Fields[2].Int != 2 {
		t.Fatalf("outgoing MSG = %+v, error=%v", decoded, err)
	}

	makePacket := func(function string, fields ...string) Event {
		raw, err := namedproto.RawMessage(1, function, fields)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := namedproto.EncodePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		event, err := decodeEvent(packet)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	applyEventLocked(&session.state, makePacket("MSG", namedproto.EncodeInt(3), namedproto.EncodeString([]byte("hello")), namedproto.EncodeInt(2)))
	applyEventLocked(&session.state, makePacket("PMSG", namedproto.EncodeInt(3), namedproto.EncodeInt(0), namedproto.EncodeInt(4), namedproto.EncodeString([]byte("pet hello")), namedproto.EncodeInt(1)))
	snapshot := session.Snapshot()
	if len(snapshot.Chat) != 2 || snapshot.Chat[0].Channel != "msg" || snapshot.Chat[0].Text != "hello" || snapshot.Chat[1].Channel != "pmsg" || snapshot.Chat[1].Text != "pet hello" {
		t.Fatalf("incoming mail channels = %+v", snapshot.Chat)
	}
}
