package aigame

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// ReadOnlyClientPacket permits health/status observation while an automatic
// task owns gameplay. A packet must contain exactly one complete frame. S is
// not generally read-only: Arena mutations also use S and must stay fenced.
func ReadOnlyClientPacket(packet []byte) bool {
	if len(packet) == 0 || bytes.IndexByte(packet, '\n') != len(packet)-1 {
		return false
	}
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return false
	}
	m, err := namedproto.ParseMessage(raw)
	if err != nil || len(m.Fields) != 1 {
		return false
	}
	value, err := namedproto.DecodeString(m.Fields[0])
	if err != nil {
		return false
	}
	if m.Function == "Echo" {
		return len(value) <= 256
	}
	if m.Function != "S" {
		return false
	}
	text := string(value)
	if ValidStatusRequest(text) {
		return true
	}
	parts := strings.Split(text, "|")
	if len(parts) != 6 || parts[0] != "LADDER" || parts[1] != strconv.Itoa(ladder.Version) || parts[4] != "status" || parts[5] != "" {
		return false
	}
	revision, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil {
		return false
	}
	canonical, err := (ladder.Request{ID: parts[2], Revision: revision, Operation: "status"}).Wire()
	return err == nil && canonical == text
}
