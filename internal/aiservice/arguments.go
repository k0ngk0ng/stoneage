package aiservice

import (
	"bytes"
	"encoding/json"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"io"
)

func decodeArguments(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return aimcp.ErrInvalidParams
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return aimcp.ErrInvalidParams
	}
	if d.Decode(new(any)) != io.EOF {
		return aimcp.ErrInvalidParams
	}
	return nil
}
