// Package playerdata reads the file-backed 2.5 character format without losing
// fields unknown to the admin UI. Persistence and online coordination belong to
// the game/account service, not to this codec.
package playerdata

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const MaxSaveSize = 65535

// Document is the three-field SAAC envelope: name|options|escaped character.
// Names and options remain byte-for-byte intact when editing character data.
type Document struct {
	envelope  [][]byte
	Character *Record
	Revision  string
}

type Record struct {
	parts     [][]byte
	delimiter byte
	separator byte
}

func ParseSave(data []byte) (*Document, error) {
	if len(data) == 0 || len(data) > MaxSaveSize || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("invalid character save size or NUL byte")
	}
	parts, err := splitDBCS(data, '|')
	if err != nil || len(parts) != 3 {
		return nil, fmt.Errorf("invalid SAAC character envelope")
	}
	character, err := unescape(parts[2])
	if err != nil {
		return nil, err
	}
	record, err := ParseCharacter(character)
	if err != nil {
		return nil, err
	}
	return &Document{envelope: parts, Character: record, Revision: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

func ParseCharacter(data []byte) (*Record, error) {
	r, err := parseRecord(data, '\n', '=')
	if err != nil {
		return nil, err
	}
	// This build logs a missing DATAEND but does not emit it consistently.
	// Require the real name field instead of rejecting otherwise valid saves.
	if _, ok := r.Raw("name"); !ok {
		return nil, fmt.Errorf("character save has no name field")
	}
	return r, nil
}

// ParsePet and ParseItem intentionally differ: legacy pets use ':' while
// inventory and warehouse items use '=' inside their pipe-delimited records.
func ParsePet(data []byte) (*Record, error)  { return parseRecord(data, '|', ':') }
func ParseItem(data []byte) (*Record, error) { return parseRecord(data, '|', '=') }

func parseRecord(data []byte, delimiter, separator byte) (*Record, error) {
	if len(data) > MaxSaveSize || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("invalid record")
	}
	parts, err := splitDBCS(data, delimiter)
	if err != nil {
		return nil, err
	}
	r := &Record{parts: parts, delimiter: delimiter, separator: separator}
	seen := map[string]bool{}
	for _, part := range parts {
		if len(part) == 0 || part[0] == '#' {
			continue
		}
		key, _, ok := bytes.Cut(part, []byte{separator})
		if !ok || !validKey(string(key)) {
			return nil, fmt.Errorf("invalid record field")
		}
		if seen[string(key)] {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		seen[string(key)] = true
	}
	return r, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (r *Record) Raw(key string) ([]byte, bool) {
	prefix := []byte(key + string(r.separator))
	for _, part := range r.parts {
		if bytes.HasPrefix(part, prefix) {
			return bytes.Clone(part[len(prefix):]), true
		}
	}
	return nil, false
}

func (r *Record) Integer(key string) (int64, error) {
	v, ok := r.Raw(key)
	if !ok {
		return 0, fmt.Errorf("missing field %s", key)
	}
	n, err := strconv.ParseInt(string(v), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %s: %w", key, err)
	}
	return n, nil
}

func (r *Record) Text(key string) (string, error) {
	v, ok := r.Raw(key)
	if !ok {
		return "", fmt.Errorf("missing field %s", key)
	}
	v, err := unescape(v)
	if err != nil {
		return "", err
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(v)
	if err != nil || strings.ContainsRune(string(decoded), '\uFFFD') {
		return "", fmt.Errorf("invalid CP936 field %s", key)
	}
	return string(decoded), nil
}

// SetRaw accepts an already encoded value, including a nested pet/item record.
// It validates delimiters so a value cannot introduce additional parent fields.
func (r *Record) SetRaw(key string, value []byte) error {
	if !validKey(key) || bytes.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("invalid field")
	}
	parts, err := splitDBCS(value, r.delimiter)
	if err != nil || len(parts) != 1 {
		return fmt.Errorf("value contains record delimiter")
	}
	part := append([]byte(key+string(r.separator)), value...)
	prefix := []byte(key + string(r.separator))
	for i, old := range r.parts {
		if bytes.HasPrefix(old, prefix) {
			r.parts[i] = part
			return nil
		}
	}
	// Keep DATAEND last and preserve the existing final delimiter.
	i := len(r.parts)
	for j, old := range r.parts {
		if bytes.HasPrefix(old, []byte("DATAEND=")) || j == len(r.parts)-1 && len(old) == 0 {
			i = j
			break
		}
	}
	r.parts = append(r.parts, nil)
	copy(r.parts[i+1:], r.parts[i:])
	r.parts[i] = part
	return nil
}

func (r *Record) SetInteger(key string, value int64) error {
	if value < -2147483648 || value > 2147483647 {
		return fmt.Errorf("integer exceeds legacy range")
	}
	return r.SetRaw(key, []byte(strconv.FormatInt(value, 10)))
}

func (r *Record) SetText(key, value string, maxBytes int) error {
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("text contains a control character")
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	if err != nil {
		return fmt.Errorf("text cannot be encoded as CP936: %w", err)
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("text exceeds %d legacy bytes", maxBytes)
	}
	return r.SetRaw(key, escape(encoded))
}

func (r *Record) Delete(key string) {
	prefix := []byte(key + string(r.separator))
	for i, part := range r.parts {
		if bytes.HasPrefix(part, prefix) {
			r.parts = append(r.parts[:i], r.parts[i+1:]...)
			return
		}
	}
}

func (r *Record) Bytes() []byte { return bytes.Join(r.parts, []byte{r.delimiter}) }

func (d *Document) Bytes() ([]byte, error) {
	parts := [][]byte{d.envelope[0], d.envelope[1], escape(d.Character.Bytes())}
	data := bytes.Join(parts, []byte{'|'})
	if len(data) > MaxSaveSize {
		return nil, fmt.Errorf("character exceeds legacy save capacity")
	}
	return data, nil
}

// CP936 trail bytes can equal '|', '=' and '\\'. Inspect only single-byte
// characters when handling separators or escapes, as the legacy engine does.
func splitDBCS(data []byte, separator byte) ([][]byte, error) {
	var parts [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] >= 0x80 {
			if i+1 == len(data) {
				return nil, fmt.Errorf("truncated CP936 character")
			}
			i++
			continue
		}
		if data[i] == separator {
			parts = append(parts, bytes.Clone(data[start:i]))
			start = i + 1
		}
	}
	return append(parts, bytes.Clone(data[start:])), nil
}

func escape(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] >= 0x80 && i+1 < len(data) {
			out = append(out, data[i], data[i+1])
			i++
			continue
		}
		switch data[i] {
		case '\n':
			out = append(out, '\\', 'n')
		case ',':
			out = append(out, '\\', 'c')
		case '|':
			out = append(out, '\\', 'z')
		case '\\':
			out = append(out, '\\', 'y')
		default:
			out = append(out, data[i])
		}
	}
	return out
}

func unescape(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] >= 0x80 {
			if i+1 == len(data) {
				return nil, fmt.Errorf("truncated CP936 character")
			}
			out = append(out, data[i], data[i+1])
			i++
			continue
		}
		if data[i] != '\\' {
			out = append(out, data[i])
			continue
		}
		i++
		if i == len(data) {
			return nil, fmt.Errorf("truncated escape")
		}
		switch data[i] {
		case 'n':
			out = append(out, '\n')
		case 'c':
			out = append(out, ',')
		case 'z':
			out = append(out, '|')
		case 'y':
			out = append(out, '\\')
		default:
			return nil, fmt.Errorf("unknown legacy escape")
		}
	}
	return out, nil
}
