package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Exercise the real TCP reader -> shared projection -> reliable HTTP queue.
// The entire next turn/result arrives before the first browser poll, so a
// lazy observation at poll time would attach the wrong state to the old BA.
func TestLadderPacketViewsFollowNativeEventsAndReliableReplay(t *testing.T) {
	left, right := net.Pipe()
	s := newTCPSession("ladder-packet-view", left, 64*1024)
	defer s.close()
	defer right.Close()
	packets := [][]byte{
		webServerPacket(t, 1, "CharLogin", "successful", ""),
		webServerPacket(t, 2, "S", `LADDER|{"version":1,"ok":true,"revision":10,"sequence":10,"snapshot":{"phase":"battle","self":{"id":"hero","name_hex":"cde6bcd2"},"match":{"id":"match-one"}}}`),
		webServerIntPacket(t, 3, "EN", 2, 218),
		webServerPacket(t, 4, "B", "BP|A|0|64"),
		webServerPacket(t, 5, "B", "BA|8400|3"),
		webServerPacket(t, 6, "B", "BP|A|0|64"),
		webServerPacket(t, 7, "B", "BA|0|4"),
		webServerPacket(t, 8, "S", `LADDER|{"version":1,"ok":true,"revision":20,"sequence":20,"snapshot":{"phase":"result","result":{"id":"match-one"}}}`),
		webServerPacket(t, 9, "B", "BU"),
		webServerPacket(t, 10, "S", `LADDER|{"version":1,"ok":true,"revision":20,"event":"contacts_lookup","snapshot":{"self":{"id":"hero"}}}`),
		webServerPacket(t, 11, "S", `LADDER|{"version":1,"ok":true,"revision":19,"snapshot":{"phase":"battle"}}`),
		webServerPacket(t, 12, "S", `LADDER|{"version":1,"ok":true,"revision":20,"event":"result_lookup","snapshot":{"phase":"result","result":{"id":"older-match"}}}`),
	}
	done := make(chan struct{})
	go func() { s.readLoop(); close(done) }()
	_ = right.SetWriteDeadline(time.Now().Add(3 * time.Second))
	for _, packet := range packets {
		if _, err := right.Write(packet); err != nil {
			t.Fatal(err)
		}
	}
	_ = right.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TCP reader did not finish")
	}
	h := &Handler{config: Config{PollTimeout: time.Millisecond}}
	poll := func(query string) []eventResponse {
		t.Helper()
		w := httptest.NewRecorder()
		h.pollEvents(w, httptest.NewRequest("GET", "/events?"+query, nil), s)
		var body struct {
			Events []eventResponse `json:"events"`
		}
		if w.Code != 200 {
			t.Fatalf("poll %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Events
	}
	first := poll("ack=0")
	if len(first) != len(packets)+1 {
		t.Fatalf("events=%d", len(first))
	}
	view := func(index int) aigame.LadderPacketView {
		t.Helper()
		var v aigame.LadderPacketView
		if err := json.Unmarshal(first[index].Ladder, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(1); v.Envelope.Snapshot.Phase != "battle" || v.Envelope.Snapshot.Self.Name != "玩家" {
		t.Fatalf("initial canonical envelope=%+v", v.Envelope)
	}
	if v := view(2); v.MatchID != "match-one" || v.Commands != nil {
		t.Fatalf("EN=%+v", v)
	}
	if v := view(4); v.Commands == nil || v.Commands.Turn != 3 || v.Commands.MyNo != 10 || !v.Commands.PlayerSubmitted || !v.Commands.PetSubmitted {
		t.Fatalf("reconnect BA=%+v", v.Commands)
	}
	if v := view(6); v.Commands.Turn != 4 || v.Commands.PlayerSubmitted || v.Commands.PetSubmitted {
		t.Fatalf("new turn=%+v", v.Commands)
	}
	if v := view(7); v.Envelope.Snapshot.Result.ID != "match-one" {
		t.Fatalf("result=%+v", v)
	}
	for _, i := range []int{0, 3, 5, 8, 9, 10, 11, 12} {
		if len(first[i].Ladder) != 0 {
			t.Fatalf("unexpected projection at %d: %s", i, first[i].Ladder)
		}
	}
	replay := poll("ack=0")
	for i, event := range first {
		if event.Seq != replay[i].Seq || event.Packet != replay[i].Packet || !bytes.Equal(event.Ladder, replay[i].Ladder) {
			t.Fatalf("replay changed event %d", i)
		}
	}
	remaining := poll("ack=5")
	if len(remaining) != len(first)-5 || remaining[0].Seq != 6 {
		t.Fatal("partial ACK lost tail")
	}
	var queuedBytes int
	s.mu.Lock()
	for _, event := range s.events {
		queuedBytes += len(event.packet) + len(event.ladder)
	}
	actualBytes := s.eventBytes
	s.mu.Unlock()
	if actualBytes != queuedBytes {
		t.Fatalf("queue bytes=%d want %d", actualBytes, queuedBytes)
	}
}

func TestLadderPacketMetadataCountsTowardQueueLimit(t *testing.T) {
	left, right := net.Pipe()
	s := newTCPSession("ladder-queue-limit", left, 64*1024)
	defer s.close()
	defer right.Close()
	s.enqueue(packetEvent{packet: []byte("packet"), ladder: make([]byte, maxQueuedBytes)})
	if !s.isClosed() {
		t.Fatal("oversized projection bypassed the queue byte limit")
	}
}
