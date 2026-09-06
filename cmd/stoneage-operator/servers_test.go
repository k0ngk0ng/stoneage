package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gameservers"
	"github.com/k0ngk0ng/stoneage/server/go/protocol"
)

func servePlayerCount(t *testing.T, connections, players int32, corrupt bool) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		conn.Write([]byte{protocol.Version, 0})
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			t.Error(err)
			return
		}
		request, _, err := protocol.DecodeResponse(line, 85)
		if err != nil {
			t.Error(err)
			return
		}
		if err := protocol.NewFieldDecoder(request, protocol.DefaultKey).VerifyChecksum(); err != nil {
			t.Error(err)
			return
		}
		fields := protocol.NewFieldEncoder(protocol.DefaultKey)
		fields.Int(connections)
		fields.Int(players)
		raw, err := fields.Finish(86)
		if err != nil {
			t.Error(err)
			return
		}
		if corrupt {
			raw = []byte("&;86;invalid;#;")
		}
		packet, err := protocol.EncodeMessage(raw, 0)
		if err != nil {
			t.Error(err)
			return
		}
		conn.Write(packet)
	}()
	return listener.Addr().String()
}

func TestGameServersActualPlayerCountAndUnknown(t *testing.T) {
	first := servePlayerCount(t, 12, 7, false)
	second := servePlayerCount(t, 1, 0, false)
	third := servePlayerCount(t, 1, 0, true)
	raw, _ := json.Marshal([]gameServer{{Name: "一线", Address: first}, {Name: "二线", Address: second}, {Name: "异常线路", Address: third}})
	value := operator{gameServersJSON: string(raw)}
	servers, err := value.gameServerList()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 || servers[0].Online == nil || *servers[0].Online != 7 {
		t.Fatalf("servers=%+v", servers)
	}
	if servers[1].Online == nil || *servers[1].Online != 0 {
		t.Fatalf("empty server must report zero: %+v", servers[1])
	}
	if servers[2].Online != nil || servers[2].Error == "" {
		t.Fatalf("malformed response must remain unknown: %+v", servers[2])
	}
	for _, server := range servers {
		if _, err := time.Parse(time.RFC3339, server.CheckedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlayerCountProbeDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := queryOnlinePlayers(ctx, listener.Addr().String()); err == nil {
		t.Fatal("expected greeting timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe ignored deadline")
	}
}

func TestGameServerConfiguration(t *testing.T) {
	servers, err := parseGameServers("", "gmsv:9065")
	if err != nil || len(servers) != 1 || servers[0].Address != "gmsv:9065" {
		t.Fatalf("default=%+v err=%v", servers, err)
	}
	for _, raw := range []string{`[]`, `null`, `{}`, `[{"name":"one","address":"no-port"}]`, `[{"name":"one","address":"gmsv:9065"},{"name":"two","address":"gmsv:9065"}]`} {
		if _, err := parseGameServers(raw, ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestOperatorUsesGatewayDirectory(t *testing.T) {
	address := servePlayerCount(t, 9, 4, false)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gameservers.Catalog{Servers: []gameservers.Server{
			{ID: "shared-line", Name: "同一线路", Address: "gateway:9065", UpstreamAddress: address},
			{ID: "maintenance", Name: "维护线", Address: "gateway:9066", UpstreamAddress: "not-resolved.invalid:9065", Disabled: true},
		}})
	}))
	defer api.Close()
	value := operator{gatewayAPIURL: api.URL, upstreamAddr: "wrong-default.invalid:9065"}
	list, err := value.gameServerList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "shared-line" || list[0].Name != "同一线路" || list[0].Online == nil || *list[0].Online != 4 {
		t.Fatalf("list=%+v", list)
	}
	if !list[1].Disabled || list[1].Online != nil || list[1].Error != "" {
		t.Fatalf("disabled line probed: %+v", list[1])
	}
}
