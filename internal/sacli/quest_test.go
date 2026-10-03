package sacli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type questGame struct {
	Game
	calls  []string
	body   map[string]any
	status string
	err    error
}

func (g *questGame) AutomationCall(_ context.Context, op string, body any) (json.RawMessage, error) {
	g.calls = append(g.calls, op)
	if op == "status" {
		return json.RawMessage(g.status), nil
	}
	if body != nil {
		g.body = body.(map[string]any)
	}
	if g.err != nil {
		return nil, g.err
	}
	return json.RawMessage(g.status), nil
}

func TestQuestStartUsesCurrentCharacterAndLimits(t *testing.T) {
	s := NewServer(DefaultConfig())
	g := &questGame{status: `{"control":{"mode":"manual","generation":7}}`}
	s.game = g
	r := s.Dispatch(context.Background(), Request{Command: "quest", Args: []string{"start", "axe-return", "--include-dependencies", "--maximum-spend", "100", "--reserve", "20", "--pet", "pet-identity"}})
	if !r.OK || strings.Join(g.calls, ",") != "status,start" || g.body["generation"] != uint64(7) || g.body["selected_pet_id"] != "pet-identity" || g.body["include_dependencies"] != true {
		t.Fatalf("request=%+v body=%v calls=%v", r, g.body, g.calls)
	}
	budget := g.body["budget"].(map[string]int64)
	if budget["maximum_spend"] != 100 || budget["reserve"] != 20 {
		t.Fatal(budget)
	}
}

func TestQuestRejectsBeforeMutationAndNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		args         []string
		running      bool
		err          error
		calls        string
	}{
		{name: "bad option", args: []string{"start", "task", "--force"}},
		{name: "negative budget", args: []string{"start", "task", "--maximum-spend", "-1"}},
		{name: "extra arg", args: []string{"cancel", "extra"}},
		{name: "autobattle", args: []string{"start", "task"}, running: true},
		{name: "old response", args: []string{"start", "task"}, status: `{}`, calls: "status"},
		{name: "other owner", args: []string{"cancel"}, status: `{"control":{"mode":"leveling","generation":7},"automation_active":true,"automation_mode":"leveling"}`, calls: "status"},
		{name: "uncertain write", args: []string{"start", "task"}, status: `{"control":{"mode":"manual","generation":7}}`, err: errors.New("response lost"), calls: "status,start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(DefaultConfig())
			g := &questGame{status: tc.status, err: tc.err}
			s.game = g
			s.autoRunning = tc.running
			r := s.Dispatch(context.Background(), Request{Command: "quest", Args: tc.args})
			if r.OK || strings.Join(g.calls, ",") != tc.calls {
				t.Fatalf("result=%+v calls=%v", r, g.calls)
			}
		})
	}
}

func TestQuestRecoveryUsesOnlyCurrentOffer(t *testing.T) {
	for _, op := range []string{"resume", "cancel"} {
		s := NewServer(DefaultConfig())
		g := &questGame{status: `{"control":{"mode":"manual","generation":9},"automation_recovery":{"handle":"saved-quest","mode":"quest"}}`}
		s.game = g
		r := s.Dispatch(context.Background(), Request{Command: "quest", Args: []string{op}})
		if !r.OK || g.body["recovery_handle"] != "saved-quest" || g.body["generation"] != uint64(9) {
			t.Fatalf("%+v %v", r, g.body)
		}
	}
}

func TestQuestProgressHelpAndTCPBoundary(t *testing.T) {
	s := NewServer(DefaultConfig())
	if r := s.Dispatch(context.Background(), Request{Command: "quest", Args: []string{"--help"}}); !r.OK || !strings.Contains(r.Text, "maximum-spend") {
		t.Fatal(r)
	}
	s.game = &logoutGame{}
	if r := s.Dispatch(context.Background(), Request{Command: "quest", Args: []string{"status"}}); r.OK || !strings.Contains(r.Error, "HTTP") {
		t.Fatal(r)
	}
	text, err := renderQuest("status", json.RawMessage(`{"control":{"generation":4,"mode":"manual"},"automation_task":{"handle":"q","status":"confirmed","state":"completed","progress":{"step":14,"steps":14,"stage":4,"stages":4,"deaths":0}}}`))
	if err != nil || !strings.Contains(text, "steps=14/14") || !strings.Contains(text, "status=confirmed") {
		t.Fatal(text, err)
	}
	if choices := Complete([]string{"quest", "res"}); strings.Join(choices, ",") != "resume" {
		t.Fatal(choices)
	}
}

func TestAutoBattleDoesNotCompeteWithRemoteQuest(t *testing.T) {
	s := NewServer(DefaultConfig())
	s.game = &questGame{status: `{"control":{"mode":"quest","generation":3},"automation_active":true,"automation_mode":"quest"}`}
	if _, err := s.startAutoBattle(false); err == nil {
		t.Fatal("auto battle started during a quest")
	}
	if s.autoRunning {
		t.Fatal("local loop was created")
	}
}

func TestQuestStartOldDaemonRejectsWithoutFallback(t *testing.T) {
	dir, err := os.MkdirTemp("../../build", "quest-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request Request
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		received <- request
		_ = json.NewEncoder(conn).Encode(failure(KindUsage, "unknown command %q; run help", request.Command))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := Call(ctx, socket, Request{Command: "quest", Args: []string{"start", "task", "--maximum-spend", "25"}})
	if err != nil || r.OK || r.Kind != KindUsage {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	select {
	case request := <-received:
		if request.Command != "quest" || strings.Join(request.Args, " ") != "start task --maximum-spend 25" {
			t.Fatal(request)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
