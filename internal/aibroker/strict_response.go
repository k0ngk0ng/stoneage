package aibroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

// decodeStrictResponse keeps recovery from accepting an ambiguous or
// forward-incompatible log line as proof that the journaled turn completed.
// Response and all of its typed nested values reject unknown fields; duplicate
// keys are rejected before encoding/json's last-value-wins behavior runs.
func decodeStrictResponse(raw []byte) (airunner.Response, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return airunner.Response{}, errors.New("empty response")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return airunner.Response{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response airunner.Response
	if err := decoder.Decode(&response); err != nil {
		return airunner.Response{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return airunner.Response{}, errors.New("trailing JSON")
		}
		return airunner.Response{}, err
	}
	return response, nil
}

// rejectDuplicateJSONKeys walks nested JSON before a struct decoder applies
// its last-value-wins behavior. Runtime event payloads are included in this
// walk, which makes the complete persisted response unambiguous.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate object key")
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		close, err := decoder.Token()
		if err != nil {
			return err
		}
		if close != json.Delim('}') {
			return errors.New("object is not closed")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		close, err := decoder.Token()
		if err != nil {
			return err
		}
		if close != json.Delim(']') {
			return errors.New("array is not closed")
		}
		return nil
	default:
		return errors.New("invalid JSON delimiter")
	}
}
