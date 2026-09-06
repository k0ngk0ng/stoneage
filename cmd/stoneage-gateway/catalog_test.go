package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/gameservers"
)

func TestConfiguredServerDirectoryAndRoutes(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gateway.toml")
	content := `catalog_listen_address = "127.0.0.1:9080"
[[servers]]
id="first"
name="主线"
listen_address="0.0.0.0:9065"
address="gateway:9065"
upstream_address="gmsv:9065"
[[servers]]
id="second"
name="维护线路"
listen_address="0.0.0.0:9066"
address="gateway:9066"
upstream_address="gmsv2:9065"
disabled=true
`
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	opts, _, err := configFromCommandLine([]string{"-config", config})
	if err != nil {
		t.Fatal(err)
	}
	if opts.catalogAddress != "127.0.0.1:9080" {
		t.Fatal(opts.catalogAddress)
	}
	routes, err := configuredRoutes(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].listenAddress != "0.0.0.0:9065" || routes[0].upstreamAddress != "gmsv:9065" {
		t.Fatalf("routes=%+v", routes)
	}
	servers, err := configuredServers(opts)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(catalogHandler(servers))
	defer api.Close()
	catalog, err := gameservers.Fetch(context.Background(), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog[0].ID != "first" || catalog[0].Name != "主线" || !catalog[1].Disabled {
		t.Fatalf("catalog=%+v", catalog)
	}
	if catalog[0].ListenAddress != "" {
		t.Fatal("listener bind address leaked into directory")
	}
	response, err := http.Post(api.URL+"/api/servers", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal(response.StatusCode)
	}
	if _, _, err := configFromCommandLine([]string{"-config", config, "-upstream", "elsewhere:9065"}); err == nil {
		t.Fatal("ambiguous override accepted")
	}
	opts.routes = "127.0.0.1:9067=gmsv:9065"
	if _, err := configuredRoutes(opts); err == nil {
		t.Fatal("ambiguous routes accepted")
	}
}

func TestDirectoryRejectsAmbiguousServers(t *testing.T) {
	one := gameservers.Server{ID: "first", Name: "主线", ListenAddress: "0.0.0.0:9065", Address: "gateway:9065", UpstreamAddress: "gmsv:9065"}
	two := one
	two.ID = "second"
	two.Address = "gateway:9066"
	if _, err := configuredServers(options{servers: []gameservers.Server{one, two}}); err == nil {
		t.Fatal("duplicate listeners accepted")
	}
	two.ListenAddress = "0.0.0.0:9066"
	two.ID = one.ID
	if _, err := configuredServers(options{servers: []gameservers.Server{one, two}}); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
