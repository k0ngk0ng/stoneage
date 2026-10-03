package aigame

import (
	"strings"
	"testing"
)

func TestGoldLimitUsesOptionalAuthoritativeResponse(t *testing.T) {
	s := &Session{state: newGameState(true)}
	s.applyEvent(stringEvent("S", partyObservationBase+"|gold_limit=2800000"))
	if a := s.Snapshot().AI; !a.GoldLimitKnown || a.GoldLimit != 2800000 {
		t.Fatal(a)
	}
	s.applyEvent(stringEvent("S", partyObservationBase))
	if s.Snapshot().AI.GoldLimitKnown {
		t.Fatal("old server inherited a stale gold capacity")
	}
	for _, value := range []string{"0", "-1", "2147483648", "unknown", "100|gold_limit=200"} {
		if _, ok := parseAIObservation(strings.Split(partyObservationBase+"|gold_limit="+value, "|")[1:]); ok {
			t.Fatal("invalid capacity accepted", value)
		}
	}
}
