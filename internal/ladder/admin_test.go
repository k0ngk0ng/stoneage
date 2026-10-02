package ladder

import (
	"strings"
	"testing"
)

func TestAdminSnapshotDecodesSharedLegacyNames(t *testing.T) {
	raw := `{"schema_version":1,"page_size":8,"queues":[{"members":[{"name_hex":"b2e2cad4"}]}],"matches":[{"teams":[{"members":[{"name_hex":"41"}]}]}]}`
	s, e := DecodeAdminSnapshot([]byte(raw))
	if e != nil || s.Queues[0].Members[0].Name != "测试" || s.Matches[0].Teams[0].Members[0].Name != "A" || s.Queues[0].Members[0].NameHex != "" {
		t.Fatal(s, e)
	}
	for _, v := range []string{strings.Replace(raw, "b2e2cad4", "zz", 1), strings.Replace(raw, `"schema_version":1`, `"schema_version":2`, 1)} {
		if _, e := DecodeAdminSnapshot([]byte(v)); e == nil {
			t.Fatal("accepted invalid snapshot")
		}
	}
}
