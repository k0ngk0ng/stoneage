// Package protocol implements the legacy StoneAge 2.5 LSSPROTO wire codec.
//
// The protocol is intentionally kept byte-oriented. Text in the original
// client is CP936/GBK rather than UTF-8, so callers must not assume that a
// decoded string contains UTF-8.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// DefaultKey is used by the login request before the server knows the
	// account name.
	DefaultKey = "cary"
	// RunningKey is appended to the account name for packets after login.
	RunningKey = "cary"
	// Version is the two-byte server greeting's first byte. The second byte
	// is NUL.
	Version = byte('L')

	encodingTable = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz{}"
	seedSize      = 6 // ceil(sizeof(int32) * 8 / 6), as in the C server
)

var (
	ErrMalformedEncoding = errors.New("malformed StoneAge encoding")
	ErrChecksum          = errors.New("StoneAge field checksum mismatch")
)

type fieldShift int

const (
	shiftNone fieldShift = iota
	// The historic names are from autil.c: shl subtracts key bytes while
	// encoding strings; shr adds key bytes while encoding integers.
	shiftLeft
	shiftRight
)

// EncodeMessage applies the outer packet transform used by 2.5. rotation
// must be in the same 0..98 range produced by the original rand() % 99.
// The returned byte slice includes the newline network delimiter.
func EncodeMessage(raw []byte, rotation int) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty message", ErrMalformedEncoding)
	}
	if rotation < 0 || rotation > 98 {
		return nil, fmt.Errorf("%w: rotation %d is outside 0..98", ErrMalformedEncoding, rotation)
	}

	seedBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(seedBytes, uint32(rotation))
	seedBytes = swap(seedBytes, "2413")
	for i := range seedBytes {
		seedBytes[i] ^= 0xff
	}
	seed := encode64(seedBytes, nil, shiftNone)
	if len(seed) != seedSize {
		return nil, fmt.Errorf("%w: encoded seed has length %d", ErrMalformedEncoding, len(seed))
	}

	rotated := rotateLeft(raw, rotation)
	plain := make([]byte, 0, len(seed)+len(rotated))
	plain = append(plain, seed...)
	plain = append(plain, rotated...)

	encoded := make([]byte, len(plain)+1)
	for i, value := range plain {
		encoded[i] = value ^ 0xff
		if encoded[i] == '\n' {
			return nil, fmt.Errorf("%w: encoded payload contains a newline", ErrMalformedEncoding)
		}
	}
	encoded[len(plain)] = '\n'
	return encoded, nil
}

// DecodeMessage reverses the outer packet transform. encoded may include the
// trailing newline read from the TCP stream. It returns the raw function and
// the rotation embedded in the packet.
func DecodeMessage(encoded []byte) ([]byte, int, error) {
	if len(encoded) > 0 && encoded[len(encoded)-1] == '\n' {
		encoded = encoded[:len(encoded)-1]
	}
	if len(encoded) < seedSize+1 {
		return nil, 0, fmt.Errorf("%w: packet is too short", ErrMalformedEncoding)
	}

	plain := make([]byte, len(encoded))
	for i, value := range encoded {
		plain[i] = value ^ 0xff
	}

	seedBytes, err := decode64(plain[:seedSize], nil, shiftNone)
	if err != nil {
		return nil, 0, fmt.Errorf("decode rotation seed: %w", err)
	}
	if len(seedBytes) != 4 {
		return nil, 0, fmt.Errorf("%w: rotation seed decoded to %d bytes", ErrMalformedEncoding, len(seedBytes))
	}
	for i := range seedBytes {
		seedBytes[i] ^= 0xff
	}
	seedBytes = swap(seedBytes, "3142")
	rotation := int(binary.LittleEndian.Uint32(seedBytes))
	if rotation < 0 || rotation > 98 {
		return nil, 0, fmt.Errorf("%w: decoded rotation %d is outside 0..98", ErrMalformedEncoding, rotation)
	}

	raw := rotateRight(plain[seedSize:], rotation)
	return raw, rotation, nil
}

// EncodeString encodes legacy text bytes with util_256to64_shl.
func EncodeString(value []byte, key string) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: empty field key", ErrMalformedEncoding)
	}
	return string(encode64(value, []byte(key), shiftLeft)), nil
}

// DecodeString decodes legacy text bytes with util_shr_64to256.
func DecodeString(encoded, key string) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: empty field key", ErrMalformedEncoding)
	}
	return decode64([]byte(encoded), []byte(key), shiftLeft)
}

// EncodeInt encodes a signed 32-bit integer exactly like util_mkint on the
// little-endian Linux 2.5 server.
func EncodeInt(value int32, key string) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: empty field key", ErrMalformedEncoding)
	}
	bytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(bytes, uint32(value))
	bytes = swap(bytes, "3142")
	for i := range bytes {
		bytes[i] ^= 0xff
	}
	return string(encode64(bytes, []byte(key), shiftRight)), nil
}

// DecodeInt reverses util_mkint/util_deint for a signed 32-bit integer.
func DecodeInt(encoded, key string) (int32, error) {
	if len(key) == 0 {
		return 0, fmt.Errorf("%w: empty field key", ErrMalformedEncoding)
	}
	bytes, err := decode64([]byte(encoded), []byte(key), shiftRight)
	if err != nil {
		return 0, err
	}
	if len(bytes) > 4 {
		return 0, fmt.Errorf("%w: integer decoded to %d bytes", ErrMalformedEncoding, len(bytes))
	}
	// util_256to64_shr omits a zero residual group. util_64to256 then writes
	// its C-string terminator immediately after the decoded bytes, and the
	// integer decoder observes that terminator as the missing high zero byte.
	bytes = append(bytes, make([]byte, 4-len(bytes))...)
	for i := range bytes {
		bytes[i] ^= 0xff
	}
	bytes = swap(bytes, "2413")
	return int32(binary.LittleEndian.Uint32(bytes)), nil
}

func encode64(src, key []byte, shift fieldShift) []byte {
	if len(src) == 0 {
		return []byte{}
	}
	out := make([]byte, 0, (len(src)*8+5)/6)
	var accumulator uint32
	var keyIndex int
	for index, value := range src {
		accumulator |= uint32(value) << ((index % 3) * 2)
		out = append(out, shiftedTableByte(byte(accumulator&0x3f), key, &keyIndex, shift))
		accumulator >>= 6
		if index%3 == 2 {
			out = append(out, shiftedTableByte(byte(accumulator&0x3f), key, &keyIndex, shift))
			accumulator = 0
		}
	}
	if accumulator != 0 {
		out = append(out, shiftedTableByte(byte(accumulator), key, &keyIndex, shift))
	}
	return out
}

func decode64(src, key []byte, shift fieldShift) ([]byte, error) {
	if len(src) == 0 {
		return []byte{}, nil
	}

	out := make([]byte, 0, len(src)*6/8)
	var accumulator uint32
	var bitCount uint
	var keyIndex int
	for _, encoded := range src {
		index := tableIndex(encoded)
		if index < 0 {
			return nil, fmt.Errorf("%w: byte 0x%02x is not in the encoding table", ErrMalformedEncoding, encoded)
		}
		index = unshiftIndex(index, key, &keyIndex, shift)
		accumulator |= uint32(index) << bitCount
		bitCount += 6
		if bitCount >= 8 {
			out = append(out, byte(accumulator&0xff))
			accumulator >>= 8
			bitCount -= 8
		}
	}
	// Unlike standard base64, util_64to256 appends the residual byte when it
	// is non-zero. Its paired encoder omits a final all-zero 6-bit group, so
	// five encoded characters are a valid representation of a four-byte int.
	if accumulator != 0 {
		out = append(out, byte(accumulator&0xff))
	}
	return out, nil
}

func shiftedTableByte(index byte, key []byte, keyIndex *int, shift fieldShift) byte {
	if shift != shiftNone {
		keyByte := int(key[*keyIndex])
		if shift == shiftLeft {
			index = byte((int(index) + 64 - keyByte%64) % 64)
		} else {
			index = byte((int(index) + keyByte) % 64)
		}
		*keyIndex = (*keyIndex + 1) % len(key)
	}
	return encodingTable[index]
}

func unshiftIndex(index int, key []byte, keyIndex *int, shift fieldShift) int {
	if shift != shiftNone {
		keyByte := int(key[*keyIndex])
		if shift == shiftLeft {
			index = (index + keyByte) % 64
		} else {
			index = (index + 64 - keyByte%64) % 64
		}
		*keyIndex = (*keyIndex + 1) % len(key)
	}
	return index
}

func tableIndex(value byte) int {
	for index := 0; index < len(encodingTable); index++ {
		if encodingTable[index] == value {
			return index
		}
	}
	return -1
}

func swap(src []byte, rule string) []byte {
	if len(src) != 4 || len(rule) != 4 {
		panic("StoneAge integer swap requires four bytes and a four-byte rule")
	}
	dst := make([]byte, 4)
	for index := 0; index < 4; index++ {
		dst[rule[index]-'1'] = src[index]
	}
	return dst
}

func rotateLeft(src []byte, count int) []byte {
	count %= len(src)
	out := make([]byte, 0, len(src))
	out = append(out, src[count:]...)
	out = append(out, src[:count]...)
	return out
}

func rotateRight(src []byte, count int) []byte {
	count %= len(src)
	if count == 0 {
		return append([]byte(nil), src...)
	}
	return rotateLeft(src, len(src)-count)
}
