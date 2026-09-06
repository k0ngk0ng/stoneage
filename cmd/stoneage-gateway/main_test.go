package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func TestGatewayConfigHelp(t *testing.T) {
	if _, _, err := configFromCommandLine([]string{"-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v, want flag.ErrHelp", err)
	}
}

func TestGatewayConfigFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(filename, []byte(`listen_address = "0.0.0.0:9065"
upstream_address = "gmsv:9065"
trace = true
trace_battle = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, configPath, err := configFromCommandLine([]string{"-config", filename})
	if err != nil {
		t.Fatal(err)
	}
	if configPath != filename {
		t.Fatalf("config path = %q, want %q", configPath, filename)
	}
	if opts.listenAddress != "0.0.0.0:9065" || opts.upstreamAddress != "gmsv:9065" || !opts.trace || !opts.traceBattle {
		t.Fatalf("loaded options = %#v", opts)
	}
}

func TestGatewayConfigExplicitFlagsOverrideFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(filename, []byte(`listen_address = "0.0.0.0:9065"
upstream_address = "gmsv:9065"
trace = true
trace_battle = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	opts, _, err := configFromCommandLine([]string{
		"-config", filename,
		"-listen", "127.0.0.1:19065",
		"-upstream", "gmsv2:19065",
		"-trace=false",
		"-trace-battle=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.listenAddress != "127.0.0.1:19065" || opts.upstreamAddress != "gmsv2:19065" || opts.trace || opts.traceBattle {
		t.Fatalf("explicit options = %#v", opts)
	}
}

func TestGatewayConfigRejectsUnknownField(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(filename, []byte("listen_adress = \"0.0.0.0:9065\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configFromCommandLine([]string{"-config", filename}); err == nil {
		t.Fatal("unknown gateway config field accepted")
	}
}

func TestGatewayConfigWithoutFileKeepsLegacyDefaults(t *testing.T) {
	t.Setenv("STONEAGE_GATEWAY_ROUTES", "")
	t.Setenv("STONEAGE_AUTH_DB", "")
	t.Setenv("STONEAGE_AUTH_REQUIRED", "")
	opts, configPath, err := configFromCommandLine(nil)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != "" {
		t.Fatalf("config path = %q, want empty", configPath)
	}
	if opts.listenAddress != "127.0.0.1:9065" || opts.upstreamAddress != "127.0.0.1:19065" || opts.trace || opts.traceBattle {
		t.Fatalf("legacy defaults = %#v", opts)
	}
}

func TestNamedBattleCommand(t *testing.T) {
	want := []byte("BC|0|0|ProbeHero||1234|1|35|35|5|")
	raw, err := namedproto.RawMessage(1, "B", []string{namedproto.EncodeString(want)})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := namedproto.EncodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := namedBattleCommand(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestConfiguredRoutes(t *testing.T) {
	routes, err := configuredRoutes(options{
		routes: "0.0.0.0:9065=gmsv:9065;0.0.0.0:9066=gmsv2:9065",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []gatewayRoute{
		{listenAddress: "0.0.0.0:9065", upstreamAddress: "gmsv:9065"},
		{listenAddress: "0.0.0.0:9066", upstreamAddress: "gmsv2:9065"},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}

	routes, err = configuredRoutes(options{listenAddress: "127.0.0.1:9065", upstreamAddress: "127.0.0.1:19065"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(routes, []gatewayRoute{{listenAddress: "127.0.0.1:9065", upstreamAddress: "127.0.0.1:19065"}}) {
		t.Fatalf("legacy route fallback = %#v", routes)
	}

	for _, value := range []string{"", "0.0.0.0:9065", "0.0.0.0:9065=", "bad=gmsv:9065"} {
		if value == "" {
			continue
		}
		if _, err := configuredRoutes(options{routes: value}); err == nil {
			t.Fatalf("configuredRoutes(%q) unexpectedly succeeded", value)
		}
	}
}

func TestLegacyStateTransitionDelay(t *testing.T) {
	delay := 100 * time.Millisecond
	tests := []struct {
		name          string
		direction     string
		function      string
		packetWritten bool
		want          time.Duration
	}{
		{"client login before reply", "GMSV->client", "ClientLogin", false, delay},
		{"client login after reply", "GMSV->client", "ClientLogin", true, 0},
		{"character login before reply", "GMSV->client", "CharLogin", false, 0},
		{"character login after reply", "GMSV->client", "CharLogin", true, delay},
		{"encounter before reply", "GMSV->client", "EN", false, 0},
		{"encounter after reply", "GMSV->client", "EN", true, delay},
		{"gameplay response", "GMSV->client", "MC", true, 0},
		{"client request", "client->GMSV", "CharLogin", true, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stateTransitionDelay(test.direction, test.function, test.packetWritten, delay)
			if got != test.want {
				t.Fatalf("delay = %s, want %s", got, test.want)
			}
		})
	}
}

func TestAuthenticateClientLogin(t *testing.T) {
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAccount(context.Background(), "probe", []byte("local")); err != nil {
		t.Fatal(err)
	}
	packetFor := func(account, password string) []byte {
		raw, err := namedproto.RawMessage(1, "ClientLogin", []string{
			namedproto.EncodeString([]byte(account)),
			namedproto.EncodeString([]byte(password)),
		})
		if err != nil {
			t.Fatal(err)
		}
		packet, err := namedproto.EncodePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	accepted, account, err := authenticateClientLogin(packetFor("PROBE", "local"), store, "127.0.0.1")
	if err != nil || !accepted || account != "probe" {
		t.Fatalf("valid login = accepted:%v account:%q err:%v", accepted, account, err)
	}
	accepted, _, err = authenticateClientLogin(packetFor("PrObE", "wrong"), store, "127.0.0.1")
	if err != nil || accepted {
		t.Fatalf("invalid login = accepted:%v err:%v", accepted, err)
	}
	response, err := clientLoginResponse("no")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := namedproto.DecodePacket(response)
	if err != nil {
		t.Fatal(err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil || message.Function != "ClientLogin" || len(message.Fields) != 1 {
		t.Fatalf("rejection response = %#v, %v", message, err)
	}
	result, err := namedproto.DecodeString(message.Fields[0])
	if err != nil || string(result) != "no" {
		t.Fatalf("rejection result = %q, %v", result, err)
	}
}
