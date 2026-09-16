package aigame

import (
	"encoding/hex"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"strings"
	"testing"
	"time"
)

func TestChatIdentityBindsOnlyExactNextPacket(t *testing.T) {
	raw := cp936TestBytes(t, "P|小明：你好\\|朋友")
	metadata := "AICHAT|1|42|3|" + testPersistentCharacterID + "|" + hex.EncodeToString(raw)
	chat := Event{Function: "TK", At: time.Now(), Fields: []Field{
		{Kind: FieldInt, Int: 42}, {Kind: FieldString, Text: raw, Raw: namedproto.EncodeString(raw)}, {Kind: FieldInt, Int: 3},
	}}
	for _, tc := range []struct {
		name        string
		metadata    string
		intervening *Event
		change      func(*Event)
		want        bool
	}{
		{name: "exact CP936 bytes", metadata: metadata, want: true},
		{name: "intervening packet", metadata: metadata, intervening: &Event{Function: "XYD"}},
		{name: "logout", metadata: metadata, intervening: eventPointer(stringEvent("CharLogout", "successful"))},
		{name: "login", metadata: metadata, intervening: eventPointer(stringEvent("CharLogin", "successful"))},
		{name: "different object", metadata: metadata, change: func(e *Event) { e.Fields[0].Int++ }},
		{name: "different color", metadata: metadata, change: func(e *Event) { e.Fields[2].Int++ }},
		{name: "different text", metadata: metadata, change: func(e *Event) { e.Fields[1].Text = []byte("P|other") }},
		{name: "invalid identity", metadata: strings.Replace(metadata, "pc1_", "bad_", 1)},
		{name: "invalid hex", metadata: metadata + "z"},
		{name: "unknown version", metadata: strings.Replace(metadata, "AICHAT|1|", "AICHAT|2|", 1)},
		{name: "system sender", metadata: strings.Replace(metadata, "|42|", "|-1|", 1)},
		{name: "oversize", metadata: metadata + strings.Repeat("00", 2048)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newGameState(true)
			applyEventLocked(&state, stringEvent("S", tc.metadata))
			if len(state.chat) != 0 {
				t.Fatal("metadata created a visible chat")
			}
			if tc.intervening != nil {
				applyEventLocked(&state, *tc.intervening)
			}
			event := chat
			event.Fields = append([]Field(nil), chat.Fields...)
			if tc.change != nil {
				tc.change(&event)
			}
			applyEventLocked(&state, event)
			if len(state.chat) != 1 {
				t.Fatal("original TK lost")
			}
			if tc.want && !strings.Contains(state.chat[0].Text, "小明") {
				t.Fatal("CP936 chat rendering changed")
			}
			if got := state.chat[0].SpeakerCharacterID; (got == testPersistentCharacterID) != tc.want {
				t.Fatalf("speaker=%q want attributed=%v", got, tc.want)
			}
			applyEventLocked(&state, chat)
			if state.chat[1].SpeakerCharacterID != "" {
				t.Fatal("metadata reused for later chat")
			}
		})
	}
}

func eventPointer(event Event) *Event { return &event }
