package aigame

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func TestTradeSubmissionAndObservationWiring(t *testing.T) {
	s, peer := worldTestSession(t)
	s.stateMu.Lock()
	s.state.trade.snapshot = TradeSnapshot{Active: true, Phase: TradePhaseTrading, PeerID: 3, PeerFD: 7, PeerName: "Peer"}
	s.stateMu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- s.ExecuteExpected(context.Background(), s.Snapshot().Revision, Action{Kind: ActionTrade, Command: "lock"})
	}()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, 4096)
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil || event.Function != "TD" || eventText(event, 0) != "T|7|Peer|C|confirm" {
		t.Fatalf("lock: %+v %v", event, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Trade; !got.OwnLockSubmitted || got.PeerLocked {
		t.Fatalf("local write: %+v", got)
	}
	if err := s.Do(context.Background(), Action{Kind: ActionItem, Command: "move", Index: 5, Value: 6}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("inventory changed during trade: %v", err)
	}
	s.applyEvent(Event{Function: "TD", Fields: []Field{{Kind: FieldString, Text: []byte("T|7|Peer|C")}}})
	if !s.Snapshot().Trade.PeerLocked {
		t.Fatal("peer lock not observed")
	}
	raw, err := namedproto.RawMessage(1, "TD", []string{namedproto.EncodeString([]byte("T|7|Peer|K"))})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := namedproto.EncodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyClientPacket(manual); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Trade; !got.Manual || !got.Uncertain {
		t.Fatalf("human change not fenced: %+v", got)
	}
	if err := s.Do(context.Background(), Action{Kind: ActionTrade, Command: "confirm"}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("confirmed manual trade: %v", err)
	}
}

func TestTradeCannotSurviveBattleOrMapTransition(t *testing.T) {
	for _, event := range []Event{
		{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}},
		{Function: "S", Fields: []Field{{Kind: FieldString, Text: []byte("C99|0|0|1|1")}}},
	} {
		s, _ := worldTestSession(t)
		s.stateMu.Lock()
		s.state.trade.snapshot = TradeSnapshot{Active: true, PeerFD: 7, PeerName: "Peer", OwnLockSubmitted: true, PeerLocked: true}
		s.stateMu.Unlock()
		s.applyEvent(event)
		if s.Snapshot().Trade.Active {
			t.Fatalf("trade survived %s", event.Function)
		}
	}
}
