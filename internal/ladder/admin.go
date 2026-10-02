package ladder

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type AdminMember struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NameHex  string `json:"name_hex,omitempty"`
	Strategy string `json:"strategy"`
	Online   bool   `json:"online"`
}
type AdminTeam struct {
	Members []AdminMember `json:"members"`
}
type AdminQueue struct {
	ID      string        `json:"id"`
	Mode    int           `json:"mode"`
	WaitMS  int64         `json:"wait_ms"`
	Members []AdminMember `json:"members"`
}
type AdminMatch struct {
	ID          string      `json:"id"`
	Mode        int         `json:"mode"`
	Phase       string      `json:"phase"`
	Turn        int         `json:"turn"`
	ElapsedMS   int64       `json:"elapsed_ms"`
	CountdownMS int64       `json:"countdown_ms"`
	Teams       []AdminTeam `json:"teams"`
}
type AdminSnapshot struct {
	Schema      int          `json:"schema_version"`
	AtMS        int64        `json:"at_ms"`
	Offset      int          `json:"offset"`
	PageSize    int          `json:"page_size"`
	QueuedTotal int          `json:"queued_total"`
	ActiveTotal int          `json:"active_total"`
	Queues      []AdminQueue `json:"queues"`
	Matches     []AdminMatch `json:"matches"`
}

func decodeName(encoded string) (string, error) {
	b, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid arena name encoding: %w", err)
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	return string(decoded), err
}
func DecodeAdminSnapshot(raw []byte) (AdminSnapshot, error) {
	var s AdminSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.Schema != 1 || s.PageSize != 8 || s.Offset < 0 || s.Offset > 256 || s.QueuedTotal < 0 || s.ActiveTotal < 0 || len(s.Queues) > 8 || len(s.Matches) > 8 {
		return s, fmt.Errorf("invalid arena snapshot")
	}
	convert := func(members []AdminMember) error {
		for i := range members {
			name, e := decodeName(members[i].NameHex)
			if e != nil {
				return e
			}
			members[i].Name = name
			members[i].NameHex = ""
		}
		return nil
	}
	for i := range s.Queues {
		if err := convert(s.Queues[i].Members); err != nil {
			return s, err
		}
	}
	for i := range s.Matches {
		for j := range s.Matches[i].Teams {
			if err := convert(s.Matches[i].Teams[j].Members); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}
