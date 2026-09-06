package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gameservers"
	"github.com/k0ngk0ng/stoneage/server/go/protocol"
)

type gameServer struct {
	ID       string `json:"id,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Name     string `json:"name"`
	Address  string `json:"address"`
}

type gameServerStatus struct {
	ID        string `json:"id,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	Online    *int32 `json:"online"`
	Error     string `json:"error,omitempty"`
	CheckedAt string `json:"checked_at"`
}

func parseGameServers(raw, upstream string) ([]gameServer, error) {
	servers := []gameServer{{Name: "默认游戏服务器", Address: upstream}}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &servers); err != nil {
			return nil, fmt.Errorf("game servers: %w", err)
		}
	}
	if len(servers) == 0 || len(servers) > 32 {
		return nil, fmt.Errorf("game servers must contain 1..32 entries")
	}
	seen := make(map[string]bool)
	for i := range servers {
		server := &servers[i]
		server.Name = strings.TrimSpace(server.Name)
		server.Address = strings.TrimSpace(server.Address)
		host, port, err := net.SplitHostPort(server.Address)
		if err != nil || host == "" || port == "" || server.Name == "" {
			return nil, fmt.Errorf("game server %d requires a name and host:port address", i+1)
		}
		if seen[server.Address] {
			return nil, fmt.Errorf("duplicate game server address %s", server.Address)
		}
		seen[server.Address] = true
	}
	return servers, nil
}

// Query the GMSV directly: PlayerNumGet's second field counts characters
// actually in the game. The first field counts sockets (including this probe)
// and must never be presented as the online player count. No login is sent.
func queryOnlinePlayers(ctx context.Context, address string) (int32, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return 0, err
	}
	if greeting[0] != protocol.Version || greeting[1] != 0 {
		return 0, fmt.Errorf("unexpected game server greeting")
	}
	raw, err := protocol.NewFieldEncoder(protocol.DefaultKey).Finish(85)
	if err != nil {
		return 0, err
	}
	packet, err := protocol.EncodeMessage(raw, 0)
	if err != nil {
		return 0, err
	}
	if _, err := conn.Write(packet); err != nil {
		return 0, err
	}
	// Bound malformed/untrusted responses rather than growing a line buffer.
	reader := bufio.NewReaderSize(conn, 4096)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return 0, err
	}
	message, _, err := protocol.DecodeResponse(line, 86)
	if err != nil {
		return 0, err
	}
	fields := protocol.NewFieldDecoder(message, protocol.DefaultKey)
	connections, err := fields.Int()
	if err != nil {
		return 0, err
	}
	players, err := fields.Int()
	if err != nil {
		return 0, err
	}
	if err := fields.VerifyChecksum(); err != nil {
		return 0, err
	}
	if players < 0 || connections < players {
		return 0, fmt.Errorf("invalid player count")
	}
	return players, nil
}

func (value *operator) gameServerList() ([]gameServerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var servers []gameServer
	if value.gatewayAPIURL != "" {
		catalog, err := gameservers.Fetch(ctx, value.gatewayAPIURL)
		if err != nil {
			return nil, err
		}
		for _, entry := range catalog {
			servers = append(servers, gameServer{ID: entry.ID, Name: entry.Name, Address: entry.UpstreamAddress, Disabled: entry.Disabled})
		}
	} else {
		var err error
		servers, err = parseGameServers(value.gameServersJSON, value.upstreamAddr)
		if err != nil {
			return nil, err
		}
	}
	// Query the same configured lines shown by Web, with one shared deadline.
	result := make([]gameServerStatus, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func(i int, server gameServer) {
			defer wg.Done()
			status := gameServerStatus{ID: server.ID, Name: server.Name, Address: server.Address, Disabled: server.Disabled}
			if server.Disabled {
				status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
				result[i] = status
				return
			}
			online, err := queryOnlinePlayers(ctx, server.Address)
			status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			if err != nil {
				status.Error = "无法获取在线人数"
			} else {
				status.Online = &online
			}
			result[i] = status
		}(i, server)
	}
	wg.Wait()
	return result, nil
}
