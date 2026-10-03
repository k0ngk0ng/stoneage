package aigame

import "testing"

func TestActorGraphicDistinguishesHiddenFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		wire  string
		known bool
		value int32
	}{
		{"0", true, 0},
		{"1", true, 1},
		{"", false, 0},
		{"?", false, 0},
	} {
		a, ok := parseActorRecord("20|1|2|3|4|" + tc.wire + "|1|0|guard||||||0|0")
		if !ok || a.CharType != 20 || a.GraphicKnown != tc.known || a.Graphic != tc.value {
			t.Fatalf("graphic %q: %+v ok=%v", tc.wire, a, ok)
		}
	}
}
