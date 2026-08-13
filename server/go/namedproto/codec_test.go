package namedproto

import (
	"bytes"
	"testing"
)

func TestCapturedClientLoginVector(t *testing.T) {
	packet := []byte("HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n")
	want := []byte("1 ClientLogin probe local ")
	raw, err := DecodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("decoded = %q, want %q", raw, want)
	}
	encoded, err := EncodePacket(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, packet) {
		t.Fatalf("encoded = %q, want %q", encoded, packet)
	}
}

func TestReferenceClientLoginResponseVector(t *testing.T) {
	// Produced directly by the archived generated C++ implementation in
	// SYSTEM/LSSPROTO_UTIL.CPP for "1 ClientLogin ok ".
	packet := []byte("EqP1vEFrmmZJs0RtaWbV9eQ+iVQ\n")
	want := []byte("1 ClientLogin ok ")
	encoded, err := EncodePacket(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, packet) {
		t.Fatalf("encoded = %q, want %q", encoded, packet)
	}
	decoded, err := DecodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, want) {
		t.Fatalf("decoded = %q, want %q", decoded, want)
	}
}

func TestPacketRoundTripShortAndCompressed(t *testing.T) {
	inputs := [][]byte{
		[]byte("1 CharList "),
		[]byte("42 MSG 0 hello\\Sworld 0 "),
		bytes.Repeat([]byte("StoneAge named protocol map payload | "), 64),
	}
	for _, input := range inputs {
		encoded, err := EncodePacket(input)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodePacket(encoded)
		if err != nil {
			t.Fatalf("input length %d: %v", len(input), err)
		}
		if !bytes.Equal(decoded, input) {
			t.Fatalf("input length %d: round trip mismatch", len(input))
		}
	}
}

func TestMessageAndFieldRoundTrips(t *testing.T) {
	strings := [][]byte{nil, []byte("plain"), []byte("space newline\nslash\\"), {0xce, 0xde, 0xb7, 0xa8}}
	for _, input := range strings {
		decoded, err := DecodeString(EncodeString(input))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, input) {
			t.Fatalf("string: got %x, want %x", decoded, input)
		}
	}
	integers := []int32{0, 1, -1, 61, 62, 100000, 1<<31 - 1, -1 << 31}
	for _, input := range integers {
		decoded, err := DecodeInt(EncodeInt(input))
		if err != nil {
			t.Fatal(err)
		}
		if decoded != input {
			t.Fatalf("integer: got %d, want %d", decoded, input)
		}
	}

	raw, err := RawMessage(7, "CharList", []string{"", EncodeString([]byte("a b"))})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.ID != 7 || message.Function != "CharList" || len(message.Fields) != 2 || message.Fields[0] != "" {
		t.Fatalf("unexpected message: %#v", message)
	}
}
