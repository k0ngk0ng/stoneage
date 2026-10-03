package aigame

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func TestCharacterHeartbeatRejectsEchoOnlyAndStaleReplies(t *testing.T) {
	for _, mode := range []string{"valid", "echo-only", "stale", "changed-character", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			conn, peer := net.Pipe()
			s := NewSession(conn, Config{})
			s.state.snapshot.Phase = PhaseWorld
			s.state.snapshot.Ladder = &ladder.Envelope{Snapshot: ladder.Snapshot{Self: ladder.Player{ID: "pc1_test"}}}
			s.startReader()
			defer s.Close()
			defer peer.Close()
			done := make(chan error, 1)
			go func() {
				defer close(done)
				p, e := bufio.NewReader(peer).ReadBytes('\n')
				if e != nil {
					done <- e
					return
				}
				raw, e := namedproto.DecodePacket(p)
				if e != nil {
					done <- e
					return
				}
				msg, e := namedproto.ParseMessage(raw)
				if e != nil {
					done <- e
					return
				}
				if msg.Function != "S" {
					done <- errors.New("health check must be read-only status")
					return
				}
				text, e := namedproto.DecodeString(msg.Fields[0])
				if e != nil {
					done <- e
					return
				}
				fields := strings.Split(string(text), "|")
				if len(fields) != 6 || fields[4] != "status" {
					done <- errors.New("unexpected mutation in probe")
					return
				}
				fn, body := "Echo", "sactl"
				if mode != "echo-only" {
					id := fields[2]
					if mode == "stale" {
						id = "old-reply"
					}
					player := "pc1_test"
					if mode == "changed-character" {
						player = "pc1_other"
					}
					payload, _ := json.Marshal(map[string]any{"version": 1, "request_id": id, "request_wire": string(text), "ok": mode != "rejected", "snapshot": map[string]any{"self": map[string]any{"id": player}}})
					fn, body = "S", "LADDER|"+string(payload)
				}
				raw, e = namedproto.RawMessage(msg.ID, fn, []string{namedproto.EncodeString([]byte(body))})
				if e != nil {
					done <- e
					return
				}
				packet, e := namedproto.EncodePacket(raw)
				if e != nil {
					done <- e
					return
				}
				_, e = peer.Write(packet)
				done <- e
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			err := s.heartbeat(ctx)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}
