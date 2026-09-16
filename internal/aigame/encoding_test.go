package aigame

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func cp936TestBytes(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := encodeLegacyUTF8(value)
	if err != nil {
		t.Fatalf("encode %q as CP936: %v", value, err)
	}
	return encoded
}

func cp936StringEvent(t *testing.T, function, value string) Event {
	t.Helper()
	return cp936RawEvent(t, function, cp936TestBytes(t, value))
}

func cp936RawEvent(t *testing.T, function string, bytes []byte) Event {
	t.Helper()
	return Event{Function: function, Fields: []Field{{Kind: FieldString, Raw: namedproto.EncodeString(bytes), Text: bytes}}, At: time.Now()}
}

func TestSnapshotDecodesCP936TextAsUTF8(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	player := append([]byte("P1|10|20|3|4|5|6|7|8|9|10|11|12|13|14|15|16|17|18|19|20|1000|2|3|4|5|6|7|8|"), cp936TestBytes(t, "玩家")...)
	player = append(player, '|')
	player = append(player, cp936TestBytes(t, "勇士")...)
	session.applyEvent(cp936RawEvent(t, "S", player))

	pet := append([]byte("K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|"), cp936TestBytes(t, "宠物")...)
	pet = append(pet, '|')
	pet = append(pet, cp936TestBytes(t, "小宠")...)
	session.applyEvent(cp936RawEvent(t, "S", pet))

	chatBytes := cp936TestBytes(t, "你好")
	session.applyEvent(Event{Function: "TK", Fields: []Field{
		{Kind: FieldInt, Int: 7, Raw: namedproto.EncodeInt(7)},
		{Kind: FieldString, Raw: namedproto.EncodeString(append([]byte("P|"), chatBytes...)), Text: append([]byte("P|"), chatBytes...)},
		{Kind: FieldInt, Int: 3, Raw: namedproto.EncodeInt(3)},
	}, At: time.Now()})
	session.applyEvent(Event{Function: "WN", Fields: []Field{
		{Kind: FieldInt, Int: 1, Raw: namedproto.EncodeInt(1)},
		{Kind: FieldInt, Int: 2, Raw: namedproto.EncodeInt(2)},
		{Kind: FieldInt, Int: 3, Raw: namedproto.EncodeInt(3)},
		{Kind: FieldInt, Int: 4, Raw: namedproto.EncodeInt(4)},
		{Kind: FieldString, Raw: namedproto.EncodeString(cp936TestBytes(t, "商店窗口")), Text: cp936TestBytes(t, "商店窗口")},
	}, At: time.Now()})

	snapshot := session.Snapshot()
	if snapshot.Player.Name != "玩家" || snapshot.Player.Title != "勇士" {
		t.Fatalf("CP936 player fields: %+v", snapshot.Player)
	}
	if len(snapshot.Pets) != 1 || snapshot.Pets[0].Name != "宠物" || snapshot.Pets[0].FreeName != "小宠" {
		t.Fatalf("CP936 pet fields: %+v", snapshot.Pets)
	}
	if len(snapshot.Chat) != 1 || snapshot.Chat[0].Text != "你好" {
		t.Fatalf("CP936 chat fields: %+v", snapshot.Chat)
	}
	if snapshot.ActiveWindow == nil || snapshot.ActiveWindow.Data != "商店窗口" {
		t.Fatalf("CP936 window field: %+v", snapshot.ActiveWindow)
	}
	if !utf8.ValidString(snapshot.Player.Name) || !utf8.ValidString(snapshot.Pets[0].Name) || !utf8.ValidString(snapshot.Chat[0].Text) || !utf8.ValidString(snapshot.ActiveWindow.Data) {
		t.Fatal("snapshot contains invalid UTF-8")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || strings.Contains(string(encoded), "�") {
		t.Fatalf("snapshot JSON contains replacement text: %s", encoded)
	}
}

func TestMalformedCP936StillProducesValidUTF8(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()

	malformed := []byte{0x81}
	event := Event{Function: "S", Fields: []Field{{Kind: FieldString, Raw: "malformed", Text: malformed}}, At: time.Now()}
	decoded := eventText(event, 0)
	if !utf8.ValidString(decoded) {
		t.Fatalf("malformed field leaked invalid UTF-8: %q", decoded)
	}
}

func TestActionTextUTF8EncodesCP936OnWire(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 100, X: 2, Y: 3}
	session.stateMu.Unlock()

	result := make(chan error, 1)
	go func() {
		result <- session.Do(context.Background(), Action{Kind: ActionChat, TextUTF8: "你好", Color: 0, Range: 3})
	}()
	packet := make([]byte, 4096)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := namedproto.DecodePacket(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.Function != "TK" || len(message.Fields) != 5 {
		t.Fatalf("wire chat envelope: %+v", message)
	}
	body, err := namedproto.DecodeString(message.Fields[2])
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("P|"), cp936TestBytes(t, "你好")...)
	if string(body) != string(want) {
		t.Fatalf("wire chat encoding: got %x want %x", body, want)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestActionRejectsUnrepresentableUTF8(t *testing.T) {
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	defer session.Close()
	defer peer.Close()
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 100, X: 2, Y: 3}
	session.stateMu.Unlock()

	err := session.Do(context.Background(), Action{Kind: ActionChat, TextUTF8: "🙂", Color: 0, Range: 3})
	if !errors.Is(err, ErrTextEncoding) {
		t.Fatalf("unrepresentable text error = %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(make([]byte, 4096)); !isTimeout(err) {
		t.Fatalf("unrepresentable text wrote packet: %v", err)
	}
}
