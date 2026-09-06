// Package gameservers is the shared, operator-configured game line directory.
package gameservers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	ID              string `json:"id" toml:"id"`
	Name            string `json:"name" toml:"name"`
	Address         string `json:"address" toml:"address"`
	UpstreamAddress string `json:"upstream_address" toml:"upstream_address"`
	ListenAddress   string `json:"-" toml:"listen_address"`
	Disabled        bool   `json:"disabled" toml:"disabled"`
}

type Catalog struct {
	Servers []Server `json:"servers"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func Validate(servers []Server) error {
	if len(servers) == 0 || len(servers) > 32 {
		return fmt.Errorf("server list must contain 1..32 entries")
	}
	ids, addresses := map[string]bool{}, map[string]bool{}
	for _, s := range servers {
		if !idPattern.MatchString(s.ID) || strings.TrimSpace(s.Name) == "" || len(s.Name) > 240 {
			return fmt.Errorf("server requires a unique ID and a display name")
		}
		if ids[s.ID] {
			return fmt.Errorf("duplicate server ID %q", s.ID)
		}
		ids[s.ID] = true
		if addresses[s.Address] {
			return fmt.Errorf("duplicate server address %q", s.Address)
		}
		addresses[s.Address] = true
		if err := validateAddress(s.Address); err != nil {
			return fmt.Errorf("server %s: %w", s.ID, err)
		}
		if err := validateAddress(s.UpstreamAddress); err != nil {
			return fmt.Errorf("server %s upstream: %w", s.ID, err)
		}
	}
	return nil
}
func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("invalid host:port address %q", address)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid port in %q", address)
	}
	return nil
}

// Fetch is for trusted backend callers. Browser responses must omit the
// internal TCP and GMSV addresses and use stable IDs when choosing a line.
func Fetch(ctx context.Context, base string) ([]Server, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("gateway API URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/servers"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch gateway server list: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway server list HTTP %d", response.StatusCode)
	}
	var catalog Catalog
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode gateway server list: %w", err)
	}
	if err := Validate(catalog.Servers); err != nil {
		return nil, err
	}
	return catalog.Servers, nil
}
