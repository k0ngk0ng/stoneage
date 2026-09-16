package aiservice

import (
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"testing"
)

func TestSocialProgressRecognizesGameResultsWithoutCountingPacketTraffic(t *testing.T) {
	base := aimcp.Observation{Phase: "world"}
	key := progressKey(base)
	traffic := base
	traffic.Revision = 99
	traffic.Chat = []aimcp.ChatMessage{{Text: "hello"}}
	if progressKey(traffic) != key {
		t.Fatal("packet/chat traffic became game progress")
	}
	for _, change := range []func(*aimcp.Observation){
		func(o *aimcp.Observation) { o.Party = []aimcp.PartyMember{{ID: "peer", Level: 10}} },
		func(o *aimcp.Observation) { o.Flags = map[string]bool{"social:trade": true} },
		func(o *aimcp.Observation) { o.OwnProgress = map[string]int{"attribute_vital": 10} },
		func(o *aimcp.Observation) { o.Inventory = map[string]int{"item:123": 1} },
	} {
		o := base
		change(&o)
		if progressKey(o) == key {
			t.Fatal("game result omitted from supervisor progress")
		}
	}
}
