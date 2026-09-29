package aigame

import (
	"bufio"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func identityRefreshFixture(t *testing.T, payload string) (*Session, *atomic.Int32) {
	t.Helper()
	conn, peer := net.Pipe()
	session := NewSession(conn, Config{AutoIdentityRefresh: true})
	session.applyEvent(stringEvent("CharLogin", "successful"))
	session.state.inventory[3] = InventoryItem{Index: 3, Name: "fixture equipment"}
	var queries atomic.Int32
	go func() {
		reader := bufio.NewReader(peer)
		for {
			packet, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			raw, err := namedproto.DecodePacket(packet)
			if err != nil {
				return
			}
			message, err := namedproto.ParseMessage(raw)
			if err != nil {
				return
			}
			if message.Function != "S" || len(message.Fields) != 1 {
				t.Error("unexpected automatic action", message.Function)
				return
			}
			query, err := namedproto.DecodeString(message.Fields[0])
			if err != nil || string(query) != "AI" {
				t.Error("not a read-only AI query")
				return
			}
			queries.Add(1)
			raw, err = namedproto.RawMessage(message.ID, "S", []string{namedproto.EncodeString([]byte(payload))})
			if err != nil {
				return
			}
			reply, err := namedproto.EncodePacket(raw)
			if err != nil {
				return
			}
			if _, err = peer.Write(reply); err != nil {
				return
			}
		}
	}()
	session.startReader()
	session.startIdentityRefresh()
	t.Cleanup(func() { session.Close(); peer.Close() })
	return session, &queries
}

func TestAutomaticIdentityRefreshOnEntryAndChanges(t *testing.T) {
	s, queries := identityRefreshFixture(t, "AI|v=1|chara=1|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|items=none|equipment=3,701|pet=0,fixture-pet,8")
	check := func() {
		t.Helper()
		snapshot, err := s.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Inventory) != 1 || !snapshot.Inventory[0].TemplateIDKnown || snapshot.Inventory[0].TemplateID != 701 {
			t.Fatal("equipment identity missing", snapshot.Inventory)
		}
		if len(snapshot.Pets) != 1 || !snapshot.Pets[0].IdentityKnown || snapshot.Pets[0].StableID != "fixture-pet" {
			t.Fatal("pet identity missing", snapshot.Pets)
		}
	}
	check()
	s.stateMu.Lock()
	s.state.invalidateAIInventory()
	s.stateMu.Unlock()
	check()
	s.applyEvent(stringEvent("S", "K0|1|100001"))
	check()
	if queries.Load() != 3 {
		t.Fatal("refresh did not follow entry, inventory change and pet replacement", queries.Load())
	}
	// Ordinary observations must use the now-valid projection without querying again.
	for i := 0; i < 5; i++ {
		check()
	}
	if queries.Load() != 3 {
		t.Fatal("unnecessary refreshes", queries.Load())
	}
}

func TestIdentityRefreshLegacyAndMalformedResponsesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"legacy", "AI|v=1|chara=1|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|pet=0,unknown,8", true},
		{"malformed", "AI|v=1|items=none|equipment=3,0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, queries := identityRefreshFixture(t, tc.payload)
			start := time.Now()
			snapshot, err := s.Observe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("observation blocked by optional metadata")
			}
			if snapshot.Inventory[0].TemplateIDKnown {
				t.Fatal("fabricated identity")
			}
			if snapshot.AI.Received != tc.valid {
				t.Fatal("invalid response accepted")
			}
			time.Sleep(300 * time.Millisecond)
			if queries.Load() != 1 {
				t.Fatal("unsupported response caused query flood", queries.Load())
			}
		})
	}
}
