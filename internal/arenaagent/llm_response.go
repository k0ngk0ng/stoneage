package arenaagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type llmFailure struct {
	code, detail string
	cause        error
}

func (e *llmFailure) Error() string                   { return "llm: " + e.detail }
func (e *llmFailure) Unwrap() error                   { return e.cause }
func llmError(code, detail string, cause error) error { return &llmFailure{code, detail, cause} }

// encoding/json otherwise silently accepts the last occurrence of a key.
// Check recursively, including escaped equivalents and nested provider fields.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return fmt.Errorf("JSON nesting too deep")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate JSON key")
				}
				seen[key] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = value(depth + 1); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
