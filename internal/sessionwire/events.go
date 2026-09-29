// Package sessionwire defines the public HTTP session envelope shared by the
// Web bridge and headless clients. Game packets retain their native encoding.
package sessionwire

import "encoding/json"

type Event struct {
	Ladder json.RawMessage `json:"ladder,omitempty"`
	Packet string          `json:"packet,omitempty"`
	Closed bool            `json:"closed,omitempty"`
	Error  string          `json:"error,omitempty"`
	Seq    uint64          `json:"seq,omitempty"`
}

type Events struct {
	Events       []Event         `json:"events"`
	Closed       bool            `json:"closed"`
	Control      json.RawMessage `json:"control"`
	Acknowledged bool            `json:"acknowledged,omitempty"`
}
