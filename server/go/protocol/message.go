package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	FunctionClientLoginRequest  = 71
	FunctionClientLoginResponse = 72
	FunctionCreateCharRequest   = 73
	FunctionCreateCharResponse  = 74
	FunctionCharLoginRequest    = 77
	FunctionCharLoginResponse   = 78
	FunctionCharListRequest     = 79
	FunctionCharListResponse    = 80
)

// CreateCharOptions contains the thirteen legacy fields accepted by function
// 73. Text is byte-oriented because 2.5 character names use the client's
// Windows code page rather than UTF-8.
type CreateCharOptions struct {
	DataPlace int32
	Name      []byte
	Image     int32
	FaceImage int32
	Vital     int32
	Strength  int32
	Toughness int32
	Dexterity int32
	Earth     int32
	Water     int32
	Fire      int32
	Wind      int32
	Hometown  int32
}

// Message is the decoded &;<function>;<fields>;#; envelope. Fields remain in
// their encoded form until a FieldDecoder is given the correct session key.
type Message struct {
	Function int
	Fields   []string
}

// ParseMessage validates and splits a decoded LSSPROTO function envelope.
func ParseMessage(raw []byte) (Message, error) {
	parts := strings.Split(string(raw), ";")
	if len(parts) < 4 || parts[0] != "&" || parts[len(parts)-2] != "#" || parts[len(parts)-1] != "" {
		return Message{}, fmt.Errorf("%w: invalid function envelope", ErrMalformedEncoding)
	}
	function, err := strconv.Atoi(parts[1])
	if err != nil || function < 0 {
		return Message{}, fmt.Errorf("%w: invalid function ID %q", ErrMalformedEncoding, parts[1])
	}
	fields := append([]string(nil), parts[2:len(parts)-2]...)
	return Message{Function: function, Fields: fields}, nil
}

// RawMessage creates the inner function envelope. The supplied fields must
// already be encoded.
func RawMessage(function int, fields []string) ([]byte, error) {
	if function < 0 {
		return nil, fmt.Errorf("%w: negative function ID", ErrMalformedEncoding)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "&;%d", function)
	for _, field := range fields {
		if strings.Contains(field, ";") {
			return nil, fmt.Errorf("%w: encoded field contains separator", ErrMalformedEncoding)
		}
		builder.WriteByte(';')
		builder.WriteString(field)
	}
	builder.WriteString(";#;")
	return []byte(builder.String()), nil
}

// FieldEncoder encodes fields while accumulating the legacy additive
// checksum. Call Finish to append that checksum and create the message.
type FieldEncoder struct {
	key      string
	fields   []string
	checksum int32
	err      error
}

func NewFieldEncoder(key string) *FieldEncoder {
	return &FieldEncoder{key: key}
}

func (encoder *FieldEncoder) String(value []byte) {
	if encoder.err != nil {
		return
	}
	encoded, err := EncodeString(value, encoder.key)
	if err != nil {
		encoder.err = err
		return
	}
	encoder.fields = append(encoder.fields, encoded)
	encoder.checksum += int32(len(value))
}

func (encoder *FieldEncoder) Int(value int32) {
	if encoder.err != nil {
		return
	}
	encoded, err := EncodeInt(value, encoder.key)
	if err != nil {
		encoder.err = err
		return
	}
	encoder.fields = append(encoder.fields, encoded)
	encoder.checksum += value
}

func (encoder *FieldEncoder) Finish(function int) ([]byte, error) {
	if encoder.err != nil {
		return nil, encoder.err
	}
	checksum, err := EncodeInt(encoder.checksum, encoder.key)
	if err != nil {
		return nil, err
	}
	fields := append(append([]string(nil), encoder.fields...), checksum)
	return RawMessage(function, fields)
}

// FieldDecoder consumes fields while independently accumulating their
// checksum. VerifyChecksum must be called after all data fields are read.
type FieldDecoder struct {
	key      string
	fields   []string
	next     int
	checksum int32
}

func NewFieldDecoder(message Message, key string) *FieldDecoder {
	return &FieldDecoder{key: key, fields: message.Fields}
}

func (decoder *FieldDecoder) String() ([]byte, error) {
	field, err := decoder.nextField()
	if err != nil {
		return nil, err
	}
	value, err := DecodeString(field, decoder.key)
	if err != nil {
		return nil, err
	}
	decoder.checksum += int32(len(value))
	return value, nil
}

func (decoder *FieldDecoder) Int() (int32, error) {
	field, err := decoder.nextField()
	if err != nil {
		return 0, err
	}
	value, err := DecodeInt(field, decoder.key)
	if err != nil {
		return 0, err
	}
	decoder.checksum += value
	return value, nil
}

func (decoder *FieldDecoder) VerifyChecksum() error {
	field, err := decoder.nextField()
	if err != nil {
		return err
	}
	actual, err := DecodeInt(field, decoder.key)
	if err != nil {
		return err
	}
	if actual != decoder.checksum {
		return fmt.Errorf("%w: got %d, want %d", ErrChecksum, actual, decoder.checksum)
	}
	if decoder.next != len(decoder.fields) {
		return fmt.Errorf("%w: %d unexpected trailing fields", ErrMalformedEncoding, len(decoder.fields)-decoder.next)
	}
	return nil
}

func (decoder *FieldDecoder) nextField() (string, error) {
	if decoder.next >= len(decoder.fields) {
		return "", fmt.Errorf("%w: missing field", ErrMalformedEncoding)
	}
	field := decoder.fields[decoder.next]
	decoder.next++
	return field, nil
}

// ClientLoginRequest builds function 71 using the pre-login default key.
func ClientLoginRequest(account, password []byte, rotation int) ([]byte, error) {
	fields := NewFieldEncoder(DefaultKey)
	fields.String(account)
	fields.String(password)
	raw, err := fields.Finish(FunctionClientLoginRequest)
	if err != nil {
		return nil, err
	}
	return EncodeMessage(raw, rotation)
}

// CharListRequest builds function 79 using the authenticated session key.
func CharListRequest(account []byte, rotation int) ([]byte, error) {
	fields := NewFieldEncoder(string(account) + RunningKey)
	raw, err := fields.Finish(FunctionCharListRequest)
	if err != nil {
		return nil, err
	}
	return EncodeMessage(raw, rotation)
}

// CreateCharRequest builds function 73 using the authenticated session key.
// The legacy server remains authoritative for validating image numbers,
// attributes, name bytes, hometown, and available character slots.
func CreateCharRequest(account []byte, options CreateCharOptions, rotation int) ([]byte, error) {
	fields := NewFieldEncoder(string(account) + RunningKey)
	fields.Int(options.DataPlace)
	fields.String(options.Name)
	fields.Int(options.Image)
	fields.Int(options.FaceImage)
	fields.Int(options.Vital)
	fields.Int(options.Strength)
	fields.Int(options.Toughness)
	fields.Int(options.Dexterity)
	fields.Int(options.Earth)
	fields.Int(options.Water)
	fields.Int(options.Fire)
	fields.Int(options.Wind)
	fields.Int(options.Hometown)
	raw, err := fields.Finish(FunctionCreateCharRequest)
	if err != nil {
		return nil, err
	}
	return EncodeMessage(raw, rotation)
}

// CharLoginRequest builds function 77 for the selected legacy character name.
func CharLoginRequest(account, character []byte, rotation int) ([]byte, error) {
	fields := NewFieldEncoder(string(account) + RunningKey)
	fields.String(character)
	raw, err := fields.Finish(FunctionCharLoginRequest)
	if err != nil {
		return nil, err
	}
	return EncodeMessage(raw, rotation)
}

// DecodeResponse decodes a framed packet and validates its expected function.
func DecodeResponse(line []byte, expectedFunction int) (Message, int, error) {
	raw, rotation, err := DecodeMessage(line)
	if err != nil {
		return Message{}, 0, err
	}
	message, err := ParseMessage(raw)
	if err != nil {
		return Message{}, 0, err
	}
	if message.Function != expectedFunction {
		return Message{}, 0, fmt.Errorf("unexpected function %d, want %d", message.Function, expectedFunction)
	}
	return message, rotation, nil
}
