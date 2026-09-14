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

func TestLoginAccountIsCanonicalisedByGateway(t *testing.T) {
	// The browser disables mobile auto-capitalisation, but the gateway must
	// enforce the same key for native/embedded clients and direct callers.
	translator := NewTranslator()
	raw, err := namedproto.RawMessage(1, "ClientLogin", []string{
		namedproto.EncodeString([]byte("ProBe")),
		namedproto.EncodeString([]byte("local")),
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := namedproto.EncodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	numeric, function, err := translator.ClientToServer(packet)
	if err != nil || function != "ClientLogin" {
		t.Fatalf("translation = function:%q err:%v", function, err)
	}
	if translator.Account() != "probe" {
		t.Fatalf("translator account = %q, want probe", translator.Account())
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
	if string(account) != "probe" {
		t.Fatalf("numeric account = %q, want probe", account)
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

func TestTradeResponseUsesTheSingle25MessageField(t *testing.T) {
	translator := NewTranslator()
	if _, _, err := translator.ClientToServer([]byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")); err != nil {
		t.Fatal(err)
	}
	const want = "C|77|TradeB28|1"
	fields := protocol.NewFieldEncoder("probe" + protocol.RunningKey)
	fields.String([]byte(want))
	raw, err := fields.Finish(92)
	if err != nil {
		t.Fatal(err)
	}
	numeric, err := protocol.EncodeMessage(raw, 41)
	if err != nil {
		t.Fatal(err)
	}
	named, function, err := translator.ServerToClient(numeric)
	if err != nil {
		t.Fatal(err)
	}
	if function != "TD" {
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
	if len(message.Fields) != 1 {
		t.Fatalf("TD fields = %q, want one 2.5 message field", message.Fields)
	}
	value, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != want {
		t.Fatalf("TD message = %q, want %q", value, want)
	}
}

func TestRejects85OnlyClientFunctions(t *testing.T) {
	for _, function := range []string{"SaMenu", "RideQuery", "SignDay", "STREET_VENDOR"} {
		raw, err := namedproto.RawMessage(1, function, nil)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := namedproto.EncodePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, got, err := NewTranslator().ClientToServer(packet); err == nil || got != function {
			t.Fatalf("ClientToServer(%s) = function %q, error %v; want a strict 2.5 rejection", function, got, err)
		}
	}
}

func TestRejectsClientFieldCountDrift(t *testing.T) {
	// CharLogin is a one-string 2.5 request.  Accepting a missing or extra
	// field would let the numeric dispatcher consume the checksum as a game
	// value and desynchronise the connection, which is exactly the failure
	// caused by copying an 8.5 schema into the 2.5 client.
	for _, fields := range [][]string{nil, {"Hero", "unexpected"}} {
		raw, err := namedproto.RawMessage(1, "CharLogin", fields)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := namedproto.EncodePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, function, err := NewTranslator().ClientToServer(packet); err == nil || function != "CharLogin" {
			t.Fatalf("CharLogin fields %q = function %q, error %v; want field-count rejection", fields, function, err)
		}
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

func TestBattleRideFieldsOptionPreservesCompleteBC(t *testing.T) {
	translator := NewTranslator(TranslatorOptions{BattleRideFields: true})
	if _, _, err := translator.ClientToServer([]byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")); err != nil {
		t.Fatal(err)
	}

	// This BC packet contains two characters and the Linux 2.5 five-field
	// riding-pet extension for each one. The explicit option must preserve all
	// thirteen fields while still passing the normal checksum/schema checks.
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
	if !bytes.Equal(command, captured) {
		t.Fatalf("complete battle command = %x, want %x", command, captured)
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

func TestBattleRideFieldsOptionStillRejectsMalformedCharacterFields(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("BC|0|0|name|"),
		[]byte("BC|0|0|name||186A0|1|23|23|5|0||0|0|0"),
	} {
		if _, err := translateBattleCommand(input, true); err == nil {
			t.Fatalf("translateBattleCommand(%q, true) succeeded", input)
		}
	}
}
