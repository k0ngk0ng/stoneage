package sacli

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// Exercise real protocol authentication, character entry and logout. The
// daemon must keep the socket alive after the login RPC context is cancelled.
func TestInteractiveLoginPersistsAndLogoutDoesNotReconnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted.Add(1)
		defer conn.Close()
		defer close(closed)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte{'L', 0})
		reader := bufio.NewReader(conn)
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
			var fields []string
			switch message.Function {
			case "ClientLogin":
				fields = []string{namedproto.EncodeString([]byte("ok"))}
			case "CharList":
				fields = []string{namedproto.EncodeString([]byte("successful")), namedproto.EncodeString(nil)}
			case "CharLogin", "CharLogout":
				fields = []string{namedproto.EncodeString([]byte("successful")), namedproto.EncodeString(nil)}
			default:
				return
			}
			raw, err = namedproto.RawMessage(message.ID, message.Function, fields)
			if err != nil {
				return
			}
			reply, err := namedproto.EncodePacket(raw)
			if err != nil {
				return
			}
			if _, err = conn.Write(reply); err != nil {
				return
			}
		}
	}()
	config := DefaultConfig()
	config.Address = listener.Addr().String()
	config.Character = "Hero"
	server := NewServer(config)
	defer server.shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	response := server.Dispatch(ctx, Request{Command: "login", Args: []string{"fixture", "test-secret"}})
	cancel()
	if !response.OK {
		t.Fatal(response.Error)
	}
	select {
	case <-closed:
		t.Fatal("login closed connection")
	case <-time.After(30 * time.Millisecond):
	}
	status := server.Dispatch(context.Background(), Request{Command: "status", JSON: true})
	if !status.OK || !strings.Contains(string(status.Data), `"Connected":true`) {
		t.Fatal("session lost after login command", status.Text)
	}
	response = server.Dispatch(context.Background(), Request{Command: "logout"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("logout did not close connection")
	}
	status = server.Dispatch(context.Background(), Request{Command: "status", JSON: true})
	if !status.OK || !strings.Contains(string(status.Data), `"Connected":false`) {
		t.Fatal("status logged back in")
	}
	if server.config.Password != "" || server.config.Account != "" || accepted.Load() != 1 {
		t.Fatal("logout retained login state")
	}
	if response = server.Dispatch(context.Background(), Request{Command: "chars"}); response.OK {
		t.Fatal("chars logged back in after logout")
	}
}
