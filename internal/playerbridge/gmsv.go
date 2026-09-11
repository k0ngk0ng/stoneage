package playerbridge

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type GMSV struct{ Queue Queue }

// Values are raw protocol bytes, including CP936 strings. Path escaping keeps
// plus signs literal and never treats them as HTML-form spaces.
func percentEncode(value string) string {
	const digits = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", rune(c)) {
			out.WriteByte(c)
		} else {
			out.WriteByte('%')
			out.WriteByte(digits[c>>4])
			out.WriteByte(digits[c&15])
		}
	}
	return out.String()
}
func percentDecode(value string) (string, error) { return url.PathUnescape(value) }

func (g GMSV) Call(ctx context.Context, values map[string]string) (map[string]string, error) {
	response, id, err := g.Queue.Exchange(ctx, func(id string) ([]byte, error) {
		var out bytes.Buffer
		fmt.Fprintf(&out, "protocol=1\nid=%s\n", id)
		keys := make([]string, 0, len(values))
		for key := range values {
			if key == "id" || key == "protocol" || strings.ContainsAny(key, "\n\r=") {
				return nil, fmt.Errorf("invalid bridge request key")
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&out, "%s=%s\n", key, percentEncode(values[key]))
		}
		return out.Bytes(), nil
	})
	if err != nil {
		return nil, err
	}
	fields, err := parseLines(response, true)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", playerdata.ErrUnavailable, err)
	}
	if fields["id"] != id || fields["protocol"] != "1" {
		return nil, fmt.Errorf("%w: 游戏响应身份不匹配", playerdata.ErrUnavailable)
	}
	if fields["ok"] != "1" {
		return nil, bridgeError(fields["code"])
	}
	return fields, nil
}
