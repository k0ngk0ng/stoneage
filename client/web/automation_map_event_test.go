package main

import (
	"context"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"testing"
	"time"
)

func TestAutomationSessionCorrelatesMapEventsAndFencesOldLeases(t *testing.T) {
	f := newAutomationExecutorFixture(t, 1, 100)
	emit := func(seq, result int32) {
		raw, err := namedproto.RawMessage(uint32(seq), "EV", []string{namedproto.EncodeInt(seq), namedproto.EncodeInt(result)})
		if err != nil {
			t.Fatal(err)
		}
		packet, err := namedproto.EncodePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		f.tcp.applyAuthoritativePacket(packet)
	}
	emit(42, 1)
	emit(43, 0)
	ctx, cancel := context.WithTimeout(f.lease, time.Second)
	defer cancel()
	for _, seq := range []int32{43, 42} {
		ack, err := f.session.WaitForMapEvent(ctx, seq)
		if err != nil || ack.Function != "EV" || ack.Fields[0].IntValue(0) != seq {
			t.Fatal(ack, err)
		}
	}
	short, stop := context.WithTimeout(f.lease, time.Millisecond)
	defer stop()
	if _, err := f.session.WaitForMapEvent(short, 42); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("consumed acknowledgement replayed", err)
	}
	emit(44, 1)
	if _, err := f.tcp.gate.Takeover("manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.session.WaitForMapEvent(context.Background(), 44); !errors.Is(err, aicontrol.ErrStale) {
		t.Fatal("old automation lease consumed acknowledgement", err)
	}
}
