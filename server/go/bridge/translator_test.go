package bridge

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"github.com/k0ngk0ng/stoneage/server/go/protocol"
)

func TestCapturedLoginTranslation(t *testing.T) {
	translator := NewTranslator()
	packet := []byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")
	numeric, function, err := translator.ClientToServer(packet)
	if err != nil {
		t.Fatal(err)
	}
	if function != "ClientLogin" || translator.Account() != "probe" {
		t.Fatalf("function/account = %q/%q", function, translator.Account())
	}
	message, _, err := protocol.DecodeResponse(numeric, protocol.FunctionClientLoginRequest)
	if err != nil {
		t.Fatal(err)
	}
	fields := protocol.NewFieldDecoder(message, protocol.DefaultKey)
	account, err := fields.String()
	if err != nil {
		t.Fatal(err)
	}
	password, err := fields.String()
	if err != nil {
		t.Fatal(err)
	}
	if err := fields.VerifyChecksum(); err != nil {
		t.Fatal(err)
	}
	if string(account) != "probe" || string(password) != "local" {
		t.Fatalf("credentials = %q/%q", account, password)
	}
}

func TestLoginResponseTranslation(t *testing.T) {
	translator := NewTranslator()
	if _, _, err := translator.ClientToServer([]byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")); err != nil {
		t.Fatal(err)
	}
	fields := protocol.NewFieldEncoder("probe" + protocol.RunningKey)
	fields.String([]byte("ok"))
	raw, err := fields.Finish(protocol.FunctionClientLoginResponse)
	if err != nil {
		t.Fatal(err)
	}
	numeric, err := protocol.EncodeMessage(raw, 37)
	if err != nil {
		t.Fatal(err)
	}
	named, function, err := translator.ServerToClient(numeric)
	if err != nil {
		t.Fatal(err)
	}
	if function != "ClientLogin" {
		t.Fatalf("function = %q", function)
	}
	decoded, err := namedproto.DecodePacket(named)
	if err != nil {
		t.Fatal(err)
	}
	message, err := namedproto.ParseMessage(decoded)
	if err != nil {
		t.Fatal(err)
	}
	value, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(value, []byte("ok")) {
		t.Fatalf("result = %q", value)
	}
}

func TestCapturedBattleCharacterTranslation(t *testing.T) {
	translator := NewTranslator()
	if _, _, err := translator.ClientToServer([]byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")); err != nil {
		t.Fatal(err)
	}

	// Captured from the local Linux 2.5 GMSV. The two non-ASCII names remain
	// their original CP936 bytes throughout the translation.
	captured, err := hex.DecodeString("42437c307c307c50726f62654865726f7c7c31383641307c317c32337c32337c357c307c7c307c307c307c467cc2ccb9ea7c7c31383742307c367c33307c33307c317c307c7c307c307c307c31307cc2ccb9ea7c7c31383742307c367c33347c33347c317c307c7c307c307c307c")
	if err != nil {
		t.Fatal(err)
	}
	fields := protocol.NewFieldEncoder("probe" + protocol.RunningKey)
	fields.String(captured)
	raw, err := fields.Finish(15)
	if err != nil {
		t.Fatal(err)
	}
	numeric, err := protocol.EncodeMessage(raw, 17)
	if err != nil {
		t.Fatal(err)
	}

	named, function, err := translator.ServerToClient(numeric)
	if err != nil {
		t.Fatal(err)
	}
	if function != "B" {
		t.Fatalf("function = %q", function)
	}
	decoded, err := namedproto.DecodePacket(named)
	if err != nil {
		t.Fatal(err)
	}
	message, err := namedproto.ParseMessage(decoded)
	if err != nil {
		t.Fatal(err)
	}
	command, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString("42437c307c307c50726f62654865726f7c7c31383641307c317c32337c32337c357c467cc2ccb9ea7c7c31383742307c367c33307c33307c317c31307cc2ccb9ea7c7c31383742307c367c33347c33347c317c")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(command, want) {
		t.Fatalf("battle command = %x, want %x", command, want)
	}
}

func TestLegacyBattleCommandLeavesOtherCommandsUntouched(t *testing.T) {
	input := []byte("BA|18000|0|")
	got, err := legacyBattleCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("command = %q, want %q", got, input)
	}
}

func TestLegacyBattleCommandRejectsMalformedCharacterFields(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("BC|0|0|name|"),
		[]byte("BC|0|0|name||186A0|1|23|23|5|0||0|0|0"),
	} {
		if _, err := legacyBattleCommand(input); err == nil {
			t.Fatalf("legacyBattleCommand(%q) succeeded", input)
		}
	}
}
