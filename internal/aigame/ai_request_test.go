package aigame

import "testing"

func TestAIRequestCorrelationIsValidatedAndNotRetainedByLegacyResponse(t *testing.T) {
	state := newGameState(true)
	base := "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|items=none"
	state.applySystem(base + "|request=0123456789abcdef")
	if state.snapshot.AI.RequestID != "0123456789abcdef" {
		t.Fatal("correlation not parsed")
	}
	for _, tail := range []string{"|request=short", "|request=0123456789abcdeg", "|request=0123456789abcdef|request=0000000000000000"} {
		state.applySystem(base + tail)
		if state.snapshot.AI.RequestID != "0123456789abcdef" {
			t.Fatal("malformed request overwrote state")
		}
	}
	state.applySystem(base)
	if state.snapshot.AI.RequestID != "" {
		t.Fatal("legacy response retained old correlation")
	}
	for _, q := range []string{"AI", "AI:0123456789abcdef"} {
		if !validStatusRequest(q) {
			t.Fatalf("valid query rejected %q", q)
		}
	}
	for _, q := range []string{"AI:short", "AI:0123456789abcdef|", "AI:0123456789abcdeg"} {
		if validStatusRequest(q) {
			t.Fatalf("invalid query accepted %q", q)
		}
	}
}
