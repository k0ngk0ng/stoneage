package sacli

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// decodeLegacy converts CP936/GBK bytes from a server data file into UTF-8.
// Malformed runs are replaced instead of producing invalid UTF-8 output.
func decodeLegacy(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(value)
	if err != nil {
		return string(bytes.ToValidUTF8(value, []byte("�")))
	}
	return string(bytes.ToValidUTF8(decoded, []byte("�")))
}

// encodeLegacy converts public UTF-8 text to the CP936/GBK bytes the
// preserved client puts on the wire. It mirrors internal/aigame's encoder so
// a length check in the CLI reports the same size the protocol layer will
// enforce.
func encodeLegacy(value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, fmt.Errorf("text is not valid UTF-8")
	}
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(value))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
