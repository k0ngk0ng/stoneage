package aigame

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// decodeLegacyBytes converts the byte strings carried by the preserved
// named protocol into valid UTF-8 for Snapshot and JSON consumers. The
// protocol does not tag its text fields, so callers only use this for fields
// that arrived from a real wire packet; synthetic test events can retain
// their already-UTF-8 text.
func decodeLegacyBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	decoded, _ := simplifiedchinese.GBK.NewDecoder().Bytes(value)
	// A malformed legacy byte sequence must never make a public Snapshot an
	// invalid UTF-8 string. Bytes.ToValidUTF8 preserves valid decoded text and
	// uses one explicit replacement for each malformed run.
	return string(bytes.ToValidUTF8(decoded, []byte("\uFFFD")))
}

// encodeLegacyUTF8 converts public UTF-8 text to the CP936/GBK bytes expected
// by the preserved client. Unrepresentable characters are rejected so a
// command cannot silently change meaning at the wire boundary.
func encodeLegacyUTF8(value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, fmt.Errorf("text is not valid UTF-8")
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
