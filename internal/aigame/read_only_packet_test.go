package aigame

import (
	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"testing"
)

func readPacket(t *testing.T, function string, fields ...string) []byte {
	t.Helper()
	encoded := make([]string, len(fields))
	for i, f := range fields {
		encoded[i] = namedproto.EncodeString([]byte(f))
	}
	raw, err := namedproto.RawMessage(1, function, encoded)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := namedproto.EncodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
func TestReadOnlyClientPacketDoesNotAuthorizeMutations(t *testing.T) {
	status, _ := (ladder.Request{ID: "health", Operation: "status"}).Wire()
	queue, _ := (ladder.Request{ID: "health", Revision: 1, Operation: "queue"}).Wire()
	for _, test := range []struct {
		function, value string
		want            bool
	}{
		{"Echo", "sactl", true}, {"S", "AI", true}, {"S", "AI:0123456789abcdef", true}, {"S", "BCAP:0123456789abcdef", true}, {"S", "c", true}, {"S", status, true},
		{"S", queue, false}, {"S", "LADDER|1|health|0|status|extra", false}, {"S", "AI:bad", false}, {"S", "unknown", false}, {"W", "a", false}, {"B", "H|1", false}, {"EV", "1", false}, {"CharLogout", "", false},
	} {
		t.Run(test.function+test.value, func(t *testing.T) {
			if got := ReadOnlyClientPacket(readPacket(t, test.function, test.value)); got != test.want {
				t.Fatal(got)
			}
		})
	}
	packet := readPacket(t, "S", "AI")
	for _, bad := range [][]byte{nil, packet[:len(packet)-1], append(append([]byte{}, packet...), readPacket(t, "W", "a")...), readPacket(t, "S", "AI", "extra")} {
		if ReadOnlyClientPacket(bad) {
			t.Fatal("malformed or multiple frames accepted")
		}
	}
}
