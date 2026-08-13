// Package namedproto implements the older, function-name based LSSPROTO
// dialect used by the preserved 2006 StoneAge 2.5 Windows client.
//
// This is deliberately separate from protocol, which implements the newer
// numeric, checksum-bearing dialect used by the available Linux GMSV.  The
// local compatibility gateway translates between the two without changing
// game data or gameplay semantics.
package namedproto

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	jencodeKey = 51526931
	charset    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-"
	base62     = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var ErrMalformed = errors.New("malformed named LSSPROTO packet")

// Message is the decoded "message-id function-name fields..." envelope.
// Fields remain escaped/base-62 tokens until interpreted with DecodeString or
// DecodeInt.
type Message struct {
	ID       uint32
	Function string
	Fields   []string
}

// DecodePacket reverses the base64, JEncode and optional Ringo compression
// layers used by the old generated LSSPROTO implementation.
func DecodePacket(packet []byte) ([]byte, error) {
	packet = bytes.TrimSuffix(packet, []byte{'\n'})
	packet = bytes.TrimSuffix(packet, []byte{'\r'})
	if len(packet) == 0 {
		return nil, fmt.Errorf("%w: empty packet", ErrMalformed)
	}

	jencoded, err := decode64(packet)
	if err != nil {
		return nil, err
	}
	compressed, err := jdecode(jencoded)
	if err != nil {
		return nil, err
	}
	if len(compressed) < 2 {
		return nil, fmt.Errorf("%w: compressed payload is too short", ErrMalformed)
	}

	var raw []byte
	if compressed[0]%2 == 0 {
		raw = append([]byte(nil), compressed[1:]...)
		if terminator := bytes.IndexByte(raw, 0); terminator >= 0 {
			raw = raw[:terminator]
		} else {
			return nil, fmt.Errorf("%w: uncompressed payload has no terminator", ErrMalformed)
		}
	} else {
		raw, err = ringoDecompress(compressed[1:])
		if err != nil {
			return nil, err
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: decoded payload is empty", ErrMalformed)
	}
	return raw, nil
}

// EncodePacket applies the exact old-client framing. The returned packet
// includes its newline TCP delimiter.
func EncodePacket(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrMalformed)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("%w: payload contains NUL", ErrMalformed)
	}

	stringLength := len(raw) + 1 // the C encoder includes strlen's NUL
	var compressed []byte
	if stringLength < 100 {
		flag := stringLength
		if flag%2 == 1 {
			flag++
		}
		compressed = make([]byte, 0, len(raw)+2)
		compressed = append(compressed, byte(flag))
		compressed = append(compressed, raw...)
		compressed = append(compressed, 0)
	} else {
		flag := stringLength
		if flag%2 == 0 {
			flag++
		}
		ringo, err := ringoCompress(raw)
		if err != nil {
			return nil, err
		}
		compressed = make([]byte, 0, len(ringo)+1)
		compressed = append(compressed, byte(flag))
		compressed = append(compressed, ringo...)
	}

	encoded := encode64(jencode(compressed))
	encoded = append(encoded, '\n')
	return encoded, nil
}

// ParseMessage parses a decoded function-name envelope while preserving empty
// string arguments between consecutive spaces.
func ParseMessage(raw []byte) (Message, error) {
	parts := bytes.Split(raw, []byte{' '})
	if len(parts) < 3 || len(parts[len(parts)-1]) != 0 {
		return Message{}, fmt.Errorf("%w: invalid function envelope %q", ErrMalformed, raw)
	}
	id, err := strconv.ParseUint(string(parts[0]), 10, 32)
	if err != nil || id == 0 {
		return Message{}, fmt.Errorf("%w: invalid message ID %q", ErrMalformed, parts[0])
	}
	if len(parts[1]) == 0 {
		return Message{}, fmt.Errorf("%w: empty function name", ErrMalformed)
	}
	fields := make([]string, 0, len(parts)-3)
	for _, field := range parts[2 : len(parts)-1] {
		fields = append(fields, string(field))
	}
	return Message{ID: uint32(id), Function: string(parts[1]), Fields: fields}, nil
}

// RawMessage builds the plaintext function-name envelope expected by
// EncodePacket. Fields must already be escaped or base-62 encoded.
func RawMessage(id uint32, function string, fields []string) ([]byte, error) {
	if id == 0 || function == "" || strings.ContainsAny(function, " \r\n\x00") {
		return nil, fmt.Errorf("%w: invalid envelope header", ErrMalformed)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d %s ", id, function)
	for _, field := range fields {
		if strings.ContainsAny(field, " \r\n\x00") {
			return nil, fmt.Errorf("%w: field contains an unescaped delimiter", ErrMalformed)
		}
		builder.WriteString(field)
		builder.WriteByte(' ')
	}
	return []byte(builder.String()), nil
}

// EncodeString escapes a byte string using the old LSSPROTO rules. Text stays
// byte-oriented so CP936 game data is never accidentally converted to UTF-8.
func EncodeString(value []byte) string {
	var builder strings.Builder
	for _, character := range value {
		switch character {
		case '\\':
			builder.WriteString("\\\\")
		case ' ':
			builder.WriteString("\\S")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		default:
			builder.WriteByte(character)
		}
	}
	return builder.String()
}

// DecodeString reverses EncodeString.
func DecodeString(value string) ([]byte, error) {
	result := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result = append(result, value[index])
			continue
		}
		if index+1 >= len(value) {
			return nil, fmt.Errorf("%w: trailing string escape", ErrMalformed)
		}
		index++
		switch value[index] {
		case 'S':
			result = append(result, ' ')
		case 'n':
			result = append(result, '\n')
		case 'r':
			result = append(result, '\r')
		case '\\':
			result = append(result, '\\')
		default:
			// This matches the generated client's descapeString: an unknown
			// escape loses the slash but retains the following byte.
			result = append(result, value[index])
		}
	}
	return result, nil
}

// EncodeInt formats the signed base-62 representation used by the old
// generated protocol.
func EncodeInt(value int32) string {
	if value == 0 {
		return "0"
	}
	number := int64(value)
	negative := number < 0
	if negative {
		number = -number
	}
	var reversed [16]byte
	next := len(reversed)
	for number > 0 {
		next--
		reversed[next] = base62[number%62]
		number /= 62
	}
	if negative {
		next--
		reversed[next] = '-'
	}
	return string(reversed[next:])
}

// DecodeInt parses the signed base-62 representation used by the old client.
func DecodeInt(value string) (int32, error) {
	if value == "" {
		return 0, fmt.Errorf("%w: empty integer", ErrMalformed)
	}
	negative := value[0] == '-'
	if negative {
		value = value[1:]
		if value == "" {
			return 0, fmt.Errorf("%w: sign without integer", ErrMalformed)
		}
	}
	var result int64
	for index := 0; index < len(value); index++ {
		digit := strings.IndexByte(base62, value[index])
		if digit < 0 {
			return 0, fmt.Errorf("%w: invalid base-62 byte 0x%02x", ErrMalformed, value[index])
		}
		result = result*62 + int64(digit)
		if result > 1<<31 {
			return 0, fmt.Errorf("%w: integer overflow", ErrMalformed)
		}
	}
	if negative {
		result = -result
	}
	if result < -1<<31 || result > 1<<31-1 {
		return 0, fmt.Errorf("%w: integer overflow", ErrMalformed)
	}
	return int32(result), nil
}

func encode64(input []byte) []byte {
	result := make([]byte, 0, (len(input)*8+5)/6)
	for index := 0; index < len(input); index += 3 {
		remaining := len(input) - index
		first := input[index]
		var second, third byte
		if remaining > 1 {
			second = input[index+1]
		}
		if remaining > 2 {
			third = input[index+2]
		}
		result = append(result, charset[(first&0xfc)>>2])
		result = append(result, charset[((first&0x03)<<4)|((second&0xf0)>>4)])
		if remaining > 1 {
			result = append(result, charset[((second&0x0f)<<2)|((third&0xc0)>>6)])
		}
		if remaining > 2 {
			result = append(result, charset[third&0x3f])
		}
	}
	return result
}

func decode64(input []byte) ([]byte, error) {
	if len(input)%4 == 1 {
		return nil, fmt.Errorf("%w: invalid base64 length", ErrMalformed)
	}
	reverse := func(character byte) (byte, error) {
		index := strings.IndexByte(charset, character)
		if index < 0 {
			return 0, fmt.Errorf("%w: invalid base64 byte 0x%02x", ErrMalformed, character)
		}
		return byte(index), nil
	}
	result := make([]byte, 0, len(input)*3/4)
	for index := 0; index < len(input); index += 4 {
		remaining := len(input) - index
		values := [4]byte{}
		limit := remaining
		if limit > 4 {
			limit = 4
		}
		for offset := 0; offset < limit; offset++ {
			value, err := reverse(input[index+offset])
			if err != nil {
				return nil, err
			}
			values[offset] = value
		}
		if limit < 2 {
			return nil, fmt.Errorf("%w: truncated base64 quartet", ErrMalformed)
		}
		result = append(result, values[0]<<2|(values[1]&0x30)>>4)
		if limit >= 3 {
			result = append(result, (values[1]&0x0f)<<4|(values[2]&0x3c)>>2)
		}
		if limit >= 4 {
			result = append(result, (values[2]&0x03)<<6|values[3]&0x3f)
		}
	}
	return result, nil
}

func jencode(input []byte) []byte {
	source := append([]byte(nil), input...)
	var sum byte
	for index := range source {
		sum += source[index]
		if jencodeKey%7 == index%5 || jencodeKey%2 == index%2 {
			source[index] = ^source[index]
		}
	}
	position := jencodeKey % len(source)
	result := make([]byte, len(source)+1)
	for index := range result {
		switch {
		case position > index:
			result[index] = source[index] + sum*byte((index*index)%3)
		case position == index:
			result[index] = sum
		default:
			result[index] = source[index-1] + sum*byte((index*index)%7)
		}
	}
	return result
}

func jdecode(input []byte) ([]byte, error) {
	if len(input) < 2 {
		return nil, fmt.Errorf("%w: JEncode payload is too short", ErrMalformed)
	}
	decodedLength := len(input) - 1
	position := jencodeKey % decodedLength
	sum := input[position]
	result := make([]byte, decodedLength)
	for index := range input {
		switch {
		case position > index:
			result[index] = input[index] - sum*byte((index*index)%3)
		case position < index:
			result[index-1] = input[index] - sum*byte((index*index)%7)
		}
	}
	for index := range result {
		if jencodeKey%7 == index%5 || jencodeKey%2 == index%2 {
			result[index] = ^result[index]
		}
	}
	return result, nil
}
