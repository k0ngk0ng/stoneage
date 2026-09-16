package aiknowledge

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type decodedLine struct {
	Number   int
	Text     string
	Encoding string
}

// decodeLegacyLine follows the encoding used by the deployed data: UTF-8 is
// accepted for operator fixtures, then GBK/CP936, and finally Shift-JIS for
// old Japanese NPC comments which are present in the historical tree.  It
// never replaces malformed bytes with U+FFFD; callers decide whether a
// malformed line is a warning or a fatal error.
func decodeLegacyLine(raw []byte, allowLatin1 bool) (string, string, error) {
	raw = bytes.TrimSuffix(raw, []byte{'\r'})
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if len(raw) == 0 {
		return "", "utf-8", nil
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", "", fmt.Errorf("binary/NUL byte in line")
	}
	if utf8.Valid(raw) {
		return string(raw), "utf-8", nil
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw); err == nil && utf8.Valid(decoded) {
		return string(decoded), "gbk", nil
	}
	if decoded, err := japanese.ShiftJIS.NewDecoder().Bytes(raw); err == nil && utf8.Valid(decoded) {
		return string(decoded), "shift-jis", nil
	}
	if allowLatin1 {
		// A few legacy generator files contain opaque bytes in comments.  Keep
		// their one-byte values visible to the parser while marking the file as
		// only partially supported in the resulting knowledge snapshot.
		return string(bytes.Runes(raw)), "latin1", nil
	}
	return "", "", fmt.Errorf("unsupported legacy text encoding")
}

func decodeLines(raw []byte, allowLatin1 bool) ([]decodedLine, []Issue) {
	parts := bytes.Split(raw, []byte{'\n'})
	lines := make([]decodedLine, 0, len(parts))
	issues := make([]Issue, 0)
	for index, part := range parts {
		lineNo := index + 1
		text, encoding, err := decodeLegacyLine(part, allowLatin1)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Code:     "decode_line",
				Message:  fmt.Sprintf("line %d: %v", lineNo, err),
				Source:   &SourceRef{Line: lineNo},
			})
			continue
		}
		lines = append(lines, decodedLine{Number: lineNo, Text: text, Encoding: encoding})
	}
	return lines, issues
}

func meaningful(text string) bool {
	return strings.TrimSpace(text) != ""
}

func commentOrBlank(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

func normalizeText(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\t", " "))
}
