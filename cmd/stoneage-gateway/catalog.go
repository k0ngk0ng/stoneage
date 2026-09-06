package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/k0ngk0ng/stoneage/internal/gameservers"
)

func configuredServers(opts options) ([]gameservers.Server, error) {
	if len(opts.servers) > 0 {
		if opts.routes != "" {
			return nil, fmt.Errorf("servers configuration cannot be combined with -routes/STONEAGE_GATEWAY_ROUTES")
		}
		if err := gameservers.Validate(opts.servers); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, s := range opts.servers {
			if err := validateTCPAddress(s.ListenAddress, "server listen address"); err != nil {
				return nil, err
			}
			if seen[s.ListenAddress] {
				return nil, fmt.Errorf("duplicate server listener %q", s.ListenAddress)
			}
			seen[s.ListenAddress] = true
		}
		return opts.servers, nil
	}
	// Preserve existing single/multi-listener deployments. Production config
	// should declare stable IDs, labels and backend-reachable addresses.
	routes, err := configuredRoutes(opts)
	if err != nil {
		return nil, err
	}
	servers := make([]gameservers.Server, 0, len(routes))
	for i, r := range routes {
		host, port, _ := net.SplitHostPort(r.listenAddress)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		servers = append(servers, gameservers.Server{ID: fmt.Sprintf("line-%d", i+1), Name: fmt.Sprintf("游戏服务器 %d", i+1), Address: net.JoinHostPort(host, port), ListenAddress: r.listenAddress, UpstreamAddress: r.upstreamAddress})
	}
	return servers, nil
}

func catalogHandler(servers []gameservers.Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/servers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(gameservers.Catalog{Servers: servers})
	})
	return mux
}
