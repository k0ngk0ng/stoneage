package sacli

import (
	"context"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestQueryHintsAndValidationDoNotRequireSession(t *testing.T) {
	s := NewServer(DefaultConfig())
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		r := s.Dispatch(context.Background(), Request{Command: "query", Args: args})
		if !r.OK || !strings.Contains(r.Text, "k0..k4") {
			t.Fatalf("missing offline help: %+v", r)
		}
	}
	for _, code := range QueryCodes() {
		if err := ValidateQueryCommand([]string{code}); err != nil {
			t.Fatal(code, err)
		}
		if !aigame.ValidStatusRequest(code) {
			t.Fatal("CLI/protocol drift", code)
		}
	}
	for _, code := range []string{"g", "w", "j", "n", "k5", "w9", "j5", "n5", "i0", "ai", "AI:bad"} {
		r := s.Dispatch(context.Background(), Request{Command: "query", Args: []string{code}})
		if r.OK || r.Kind != KindUsage || !strings.Contains(r.Error, "query --help") {
			t.Fatalf("bad code did not fail before session access: %s %+v", code, r)
		}
	}
	if ValidateQueryCommand([]string{"AI:0123456789abcdef"}) != nil {
		t.Fatal("correlation query rejected")
	}
}
