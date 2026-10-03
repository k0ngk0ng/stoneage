package sacli

import (
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleauto"
)

func TestAutoBattleStatusReportsWhyEncounterSeekingIsBlocked(t *testing.T) {
	for _, tc := range []struct{ blocked, want string }{
		{"window", "open dialog"},
		{"ladder", "arena preparation"},
		{"walk", "cannot walk"},
		{"unrecognized-state", "unrecognized-state"},
	} {
		line := describeAutoState(battleauto.State{Seeking: true, Blocked: tc.blocked})
		if !strings.Contains(line, tc.want) || strings.Contains(line, "walking in place") {
			t.Fatalf("blocked=%q status=%q", tc.blocked, line)
		}
	}
	if line := describeAutoState(battleauto.State{Seeking: true}); !strings.Contains(line, "walking in place") {
		t.Fatal(line)
	}
}
