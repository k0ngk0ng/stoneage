package aigame

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mapEventAck(sequence, result int32) Event {
	return Event{Function: "EV", Fields: []Field{
		{Kind: FieldInt, Int: sequence},
		{Kind: FieldInt, Int: result},
	}, At: time.Now()}
}

func TestWaitForMapEventAcceptsEarlyAcknowledgement(t *testing.T) {
	session := NewSession(nil, Config{})
	defer session.Close()

	const sequence int32 = 17
	session.applyEvent(mapEventAck(sequence, 1))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ack, err := session.WaitForMapEvent(ctx, sequence)
	if err != nil {
		t.Fatalf("early EV ACK: %v", err)
	}
	if ack.Function != "EV" || ack.Fields[0].IntValue(0) != sequence || ack.Fields[1].IntValue(-1) != 1 {
		t.Fatalf("unexpected early ACK: %+v", ack)
	}
	if len(session.mapEventAcks) != 0 || len(session.mapEventOrder) != 0 {
		t.Fatalf("consumed ACK left residual state: acks=%d order=%d", len(session.mapEventAcks), len(session.mapEventOrder))
	}
}

func TestWaitForMapEventBroadcastsConcurrentSequences(t *testing.T) {
	session := NewSession(nil, Config{})
	defer session.Close()

	type result struct {
		sequence int32
		err      error
	}
	results := make(chan result, 2)
	for _, sequence := range []int32{21, 22} {
		go func(sequence int32) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ack, err := session.WaitForMapEvent(ctx, sequence)
			if err == nil {
				sequence = ack.Fields[0].IntValue(0)
			}
			results <- result{sequence: sequence, err: err}
		}(sequence)
	}
	// Let both waiters capture the current broadcast channel before emitting
	// either ACK. A single-token wake channel would leave one waiter asleep
	// when the other sequence consumed that token.
	time.Sleep(20 * time.Millisecond)
	session.applyEvent(mapEventAck(22, 1))
	session.applyEvent(mapEventAck(21, 1))

	seen := map[int32]bool{}
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("concurrent EV ACK: %v", result.err)
			}
			seen[result.sequence] = true
		case <-time.After(time.Second):
			t.Fatal("concurrent EV ACK waiter was not awakened")
		}
	}
	if !seen[21] || !seen[22] {
		t.Fatalf("concurrent ACK sequences = %v", seen)
	}
}

func TestMapEventAcknowledgementsAreBoundedAndClearedOnClose(t *testing.T) {
	session := NewSession(nil, Config{})
	for sequence := int32(1); sequence <= 100; sequence++ {
		session.applyEvent(mapEventAck(sequence, 1))
	}
	if len(session.mapEventAcks) > 64 || len(session.mapEventOrder) > 64 {
		t.Fatalf("EV ACK cache is unbounded: acks=%d order=%d", len(session.mapEventAcks), len(session.mapEventOrder))
	}
	if _, ok := session.mapEventAcks[1]; ok {
		t.Fatal("old EV ACK was retained past the bounded cache")
	}
	if _, ok := session.mapEventAcks[100]; !ok {
		t.Fatal("newest EV ACK was evicted from the bounded cache")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(session.mapEventAcks) != 0 || len(session.mapEventOrder) != 0 || session.mapEventWake != nil {
		t.Fatalf("EV ACK cache survived session close: acks=%d order=%d wake=%v", len(session.mapEventAcks), len(session.mapEventOrder), session.mapEventWake)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := session.WaitForMapEvent(ctx, 100); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed EV waiter error = %v, want ErrClosed", err)
	}
}

func TestZeroValueSessionCanConsumeEarlyMapEventAcknowledgement(t *testing.T) {
	var session Session
	session.applyEvent(mapEventAck(31, 1))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ack, err := session.WaitForMapEvent(ctx, 31)
	if err != nil || ack.Fields[0].IntValue(0) != 31 {
		t.Fatalf("zero-value EV ACK = %+v, err=%v", ack, err)
	}
}
