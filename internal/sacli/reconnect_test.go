package sacli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

type reconnectGame struct {
	selectionGame
	inPlace, closed int
	observeErr      error
}

func (g *reconnectGame) Observe(context.Context) (aigame.Snapshot, error) {
	return g.snapshot, g.observeErr
}
func (g *reconnectGame) LogoutInPlace(context.Context) error { g.inPlace++; return nil }
func (g *reconnectGame) Close() error                        { g.closed++; return nil }

func TestReconnectUsesMemoryAndSameCharacterWithoutRecordPointLogout(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "world", true: "closed-before-event-drain"}[closed], func(t *testing.T) {
			testReconnectSameCharacter(t, closed)
		})
	}
}

func testReconnectSameCharacter(t *testing.T, closed bool) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	got := make(chan namedproto.Message, 8)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(4 * time.Second))
		conn.Write([]byte{'L', 0})
		reader := bufio.NewReader(conn)
		for {
			p, e := reader.ReadBytes('\n')
			if e != nil {
				return
			}
			raw, e := namedproto.DecodePacket(p)
			if e != nil {
				return
			}
			m, e := namedproto.ParseMessage(raw)
			if e != nil {
				return
			}
			got <- m
			var fields []string
			switch m.Function {
			case "ClientLogin":
				fields = []string{namedproto.EncodeString([]byte("ok"))}
			case "CharList", "CharLogin":
				fields = []string{namedproto.EncodeString([]byte("successful")), namedproto.EncodeString(nil)}
			case "S":
				fields = []string{namedproto.EncodeString([]byte("AI|v=1|chara=0|items=none"))}
			default:
				return
			}
			raw, _ = namedproto.RawMessage(m.ID, m.Function, fields)
			reply, _ := namedproto.EncodePacket(raw)
			if _, e = conn.Write(reply); e != nil {
				return
			}
		}
	}()
	cfg := DefaultConfig()
	cfg.Transport = "tcp"
	cfg.Address = listener.Addr().String()
	cfg.Account = "fixture"
	cfg.Password = "memory-only"
	cfg.Character = "Hero"
	s := NewServer(cfg)
	defer s.shutdown()
	old := &reconnectGame{selectionGame: selectionGame{snapshot: aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld, Character: "Hero"}}}
	wantInPlace, wantClosed := 1, 0
	if closed {
		old.observeErr = aigame.ErrClosed
		wantInPlace, wantClosed = 0, 1
	}
	s.game = old
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := s.Dispatch(ctx, Request{Command: "reconnect"})
	if !r.OK {
		t.Fatal(r.Error)
	}
	if old.inPlace != wantInPlace || old.closed != wantClosed || s.desiredCharacter() != "Hero" || s.config.Password != "memory-only" {
		t.Fatal("lost identity or used unacknowledged close")
	}
	if strings.Contains(r.Text, "memory-only") || strings.Contains(string(r.Data), "memory-only") {
		t.Fatal("credential leaked")
	}
	for _, fn := range []string{"ClientLogin", "CharList", "CharLogin"} {
		m := <-got
		if m.Function != fn {
			t.Fatal(m.Function, fn)
		}
		if fn == "CharLogin" {
			name, _ := namedproto.DecodeString(m.Fields[0])
			if string(name) != "Hero" {
				t.Fatal("character changed")
			}
		}
	}
}

func TestReconnectDoesNotReplaceSessionOnObservationFailure(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("observation unavailable")} {
		cfg := DefaultConfig()
		cfg.Account, cfg.Password = "fixture", "memory-only"
		s := NewServer(cfg)
		g := &reconnectGame{observeErr: err}
		s.game = g
		if r := s.Dispatch(context.Background(), Request{Command: "reconnect"}); r.OK || g.closed != 0 || g.inPlace != 0 || s.game != g {
			t.Fatalf("replaced session on %v: %+v", err, r)
		}
	}
}

func TestReconnectHelpIsLocalAndCompletes(t *testing.T) {
	s := NewServer(DefaultConfig())
	r := s.Dispatch(context.Background(), Request{Command: "reconnect", Args: []string{"--help"}})
	if !r.OK || !strings.Contains(r.Text, "usage: sactl reconnect") || s.game != nil {
		t.Fatal(r)
	}
	if got := Complete([]string{"reconnect", "--h"}); len(got) != 1 || got[0] != "--help" {
		t.Fatal(got)
	}
}
func TestReconnectRefusesActiveWorkAndClearedCredentials(t *testing.T) {
	for _, state := range []string{"battle", "trade", "queue", "automation", "logged-out", "arguments"} {
		t.Run(state, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Account = "fixture"
			cfg.Password = "memory-only"
			s := NewServer(cfg)
			g := &reconnectGame{selectionGame: selectionGame{snapshot: aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld}}}
			s.game = g
			req := Request{Command: "reconnect"}
			switch state {
			case "battle":
				g.snapshot.Battle.Active = true
			case "trade":
				g.snapshot.Trade.Active = true
			case "queue":
				g.snapshot.Ladder = &ladder.Envelope{Snapshot: ladder.Snapshot{Phase: "queued"}}
			case "automation":
				s.autoRunning = true
			case "logged-out":
				s.config.Password = ""
			case "arguments":
				req.Args = []string{"--force"}
			}
			if r := s.Dispatch(context.Background(), req); r.OK {
				t.Fatal("reconnected active/invalid session")
			}
			if g.closed != 0 || g.inPlace != 0 || s.game != g {
				t.Fatal("rejected command changed session")
			}
		})
	}
}
func TestDisconnectedStatusOffersReconnectWithoutLoggingIn(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Account = "fixture"
	cfg.Password = "memory-only"
	s := NewServer(cfg)
	for _, want := range []bool{true, false} {
		if !want {
			s.config.Password = ""
		}
		r := s.Dispatch(context.Background(), Request{Command: "status", JSON: true})
		var data struct {
			CanReconnect bool
			Connected    bool
		}
		if json.Unmarshal(r.Data, &data) != nil || data.CanReconnect != want || data.Connected || s.game != nil {
			t.Fatal(r)
		}
		if want && !strings.Contains(r.Text, "sactl reconnect") {
			t.Fatal(r.Text)
		}
	}
}
