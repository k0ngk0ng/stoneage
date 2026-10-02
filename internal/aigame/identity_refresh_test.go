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

func TestBattleObservationDoesNotWaitForOptionalIdentities(t *testing.T) {
	s, queries := identityRefreshFixture(t, "AI|v=1|items=none|equipment=3,0")
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 218}}})
	needed, _ := s.identityRefreshState()
	if !needed {
		t.Fatal("fixture must have pending identities in battle")
	}
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		snapshot, err := s.Observe(ctx)
		cancel()
		if err != nil || snapshot.Phase != PhaseBattle || len(snapshot.Inventory) != 1 || snapshot.Inventory[0].TemplateIDKnown {
			t.Fatal("optional metadata delayed battle or invented an identifier", err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for queries.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if queries.Load() != 1 {
		t.Fatal("battle observation disabled background refresh", queries.Load())
	}
}

func TestBattleBackgroundIdentityRefreshStillAppliesResponse(t *testing.T) {
	s, queries := identityRefreshFixture(t, "AI|v=1|chara=1|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|items=none|equipment=3,701|pet=0,fixture-pet,8")
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 218}}})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := s.Snapshot()
		if len(snapshot.Inventory) == 1 && snapshot.Inventory[0].TemplateIDKnown && snapshot.Inventory[0].TemplateID == 701 && len(snapshot.Pets) == 1 && snapshot.Pets[0].IdentityKnown && snapshot.Pets[0].StableID == "fixture-pet" {
			if queries.Load() != 1 || snapshot.Phase != PhaseBattle {
				t.Fatal("unexpected background refresh or phase change")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("battle background refresh never applied valid identities")
}

func TestEnteringBattleReleasesPendingWorldIdentityWait(t *testing.T) {
	s, _ := identityRefreshFixture(t, "AI|v=1|items=none|equipment=3,0")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Observe(ctx); done <- err }()
	select {
	case err := <-done:
		t.Fatal("world observation did not wait for pending identities", err)
	case <-time.After(30 * time.Millisecond):
	}
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}, {Kind: FieldInt, Int: 218}}})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("entering battle exhausted observation deadline", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("world identity wait continued after entering battle")
	}
}

func TestBattleIdentityQueriesKeepStatusValidation(t *testing.T) {
	s := gameState{snapshot: decisionFixture()}
	s.snapshot.Connected = true
	for _, command := range []string{"AI", "AI:0123456789abcdef"} {
		values, function, err := validateActionLocked(&s, Action{Kind: ActionStatus, Command: command})
		if err != nil || function != "S" || len(values) != 1 || string(values[0].text) != command {
			t.Fatal("read-only identity query unavailable during combat", command, err)
		}
	}
	for _, command := range []string{"AI:", "AI:not-an-id", "AI:0123456789ABCDEf", "AI:0123456789abcdef|i", "i", "k0"} {
		if _, _, err := validateActionLocked(&s, Action{Kind: ActionStatus, Command: command}); err == nil {
			t.Fatal("malformed or world-only status admitted during combat", command)
		}
	}
}
