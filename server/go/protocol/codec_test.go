package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func TestMessageRoundTripEveryRotation(t *testing.T) {
	raw := []byte("&;71;example;packet;checksum;#;")
	for rotation := 0; rotation <= 98; rotation++ {
		encoded, err := EncodeMessage(raw, rotation)
		if err != nil {
			t.Fatalf("rotation %d: EncodeMessage: %v", rotation, err)
		}
		decoded, gotRotation, err := DecodeMessage(encoded)
		if err != nil {
			t.Fatalf("rotation %d: DecodeMessage: %v", rotation, err)
		}
		if gotRotation != rotation {
			t.Errorf("rotation: got %d, want %d", gotRotation, rotation)
		}
		if !bytes.Equal(decoded, raw) {
			t.Errorf("rotation %d: got %q, want %q", rotation, decoded, raw)
		}
	}
}

func TestFieldRoundTrips(t *testing.T) {
	keys := []string{DefaultKey, "probe" + RunningKey}
	strings := [][]byte{nil, []byte("a"), []byte("stoneage"), {0xd6, 0xd0, 0xce, 0xc4}}
	integers := []int32{0, 1, -1, 10, 2147483647, -2147483648}
	for _, key := range keys {
		for _, input := range strings {
			encoded, err := EncodeString(input, key)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeString(encoded, key)
			if err != nil {
				t.Fatalf("DecodeString(%q): %v", encoded, err)
			}
			if !bytes.Equal(decoded, input) {
				t.Errorf("string with key %q: got %x, want %x", key, decoded, input)
			}
		}
		for _, input := range integers {
			encoded, err := EncodeInt(input, key)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeInt(encoded, key)
			if err != nil {
				t.Fatalf("DecodeInt(%q): %v", encoded, err)
			}
			if decoded != input {
				t.Errorf("integer with key %q: got %d, want %d", key, decoded, input)
			}
		}
	}
}

func TestCheckedFields(t *testing.T) {
	encoder := NewFieldEncoder("account" + RunningKey)
	encoder.String([]byte("successful"))
	encoder.String([]byte(""))
	raw, err := encoder.Finish(FunctionCharListResponse)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoder := NewFieldDecoder(message, "account"+RunningKey)
	if _, err := decoder.String(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.String(); err != nil {
		t.Fatal(err)
	}
	if err := decoder.VerifyChecksum(); err != nil {
		t.Fatal(err)
	}

	message.Fields[len(message.Fields)-1], err = EncodeInt(11, "account"+RunningKey)
	if err != nil {
		t.Fatal(err)
	}
	decoder = NewFieldDecoder(message, "account"+RunningKey)
	_, _ = decoder.String()
	_, _ = decoder.String()
	if err := decoder.VerifyChecksum(); !errors.Is(err, ErrChecksum) {
		t.Fatalf("VerifyChecksum error = %v, want ErrChecksum", err)
	}
}

func TestCreateCharRequestFields(t *testing.T) {
	account := []byte("probe")
	options := CreateCharOptions{
		DataPlace: 0,
		Name:      []byte("ProbeHero"),
		Image:     100000,
		FaceImage: 30000,
		Vital:     5,
		Strength:  5,
		Toughness: 5,
		Dexterity: 5,
		Earth:     10,
		Hometown:  0,
	}
	packet, err := CreateCharRequest(account, options, 37)
	if err != nil {
		t.Fatal(err)
	}
	raw, rotation, err := DecodeMessage(packet)
	if err != nil {
		t.Fatal(err)
	}
	if rotation != 37 {
		t.Fatalf("rotation: got %d, want 37", rotation)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.Function != FunctionCreateCharRequest {
		t.Fatalf("function: got %d, want %d", message.Function, FunctionCreateCharRequest)
	}
	decoder := NewFieldDecoder(message, string(account)+RunningKey)
	wantInts := []int32{0, 100000, 30000, 5, 5, 5, 5, 10, 0, 0, 0, 0}
	gotDataPlace, err := decoder.Int()
	if err != nil || gotDataPlace != wantInts[0] {
		t.Fatalf("data place: got %d, err %v", gotDataPlace, err)
	}
	name, err := decoder.String()
	if err != nil || !bytes.Equal(name, options.Name) {
		t.Fatalf("name: got %q, err %v", name, err)
	}
	for index, want := range wantInts[1:] {
		got, err := decoder.Int()
		if err != nil || got != want {
			t.Fatalf("integer field %d: got %d, want %d, err %v", index+2, got, want, err)
		}
	}
	if err := decoder.VerifyChecksum(); err != nil {
		t.Fatal(err)
	}
}

func TestCharLoginRequestFields(t *testing.T) {
	packet, err := CharLoginRequest([]byte("probe"), []byte("ProbeHero"), 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := DecodeMessage(packet)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.Function != FunctionCharLoginRequest {
		t.Fatalf("function: got %d, want %d", message.Function, FunctionCharLoginRequest)
	}
	decoder := NewFieldDecoder(message, "probe"+RunningKey)
	name, err := decoder.String()
	if err != nil || !bytes.Equal(name, []byte("ProbeHero")) {
		t.Fatalf("name: got %q, err %v", name, err)
	}
	if err := decoder.VerifyChecksum(); err != nil {
		t.Fatal(err)
	}
}

func TestClientLoginEnvelope(t *testing.T) {
	packet, err := ClientLoginRequest([]byte("probe"), []byte("local"), 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, rotation, err := DecodeMessage(packet)
	if err != nil {
		t.Fatal(err)
	}
	if rotation != 0 {
		t.Fatalf("rotation = %d, want 0", rotation)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.Function != FunctionClientLoginRequest {
		t.Fatalf("function = %d", message.Function)
	}
	decoder := NewFieldDecoder(message, DefaultKey)
	account, err := decoder.String()
	if err != nil {
		t.Fatal(err)
	}
	password, err := decoder.String()
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.VerifyChecksum(); err != nil {
		t.Fatal(err)
	}
	if string(account) != "probe" || string(password) != "local" {
		t.Fatalf("credentials = %q/%q", account, password)
	}
}
