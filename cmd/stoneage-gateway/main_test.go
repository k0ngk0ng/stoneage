package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"github.com/k0ngk0ng/stoneage/server/go/protocol"
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
	t.Setenv("STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS", "")
	opts, configPath, err := configFromCommandLine(nil)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != "" {
		t.Fatalf("config path = %q, want empty", configPath)
	}
	if opts.listenAddress != "127.0.0.1:9065" || opts.upstreamAddress != "127.0.0.1:19065" || opts.trace || opts.traceBattle || opts.battleRideFields || opts.trustedProxyHosts != "" {
		t.Fatalf("legacy defaults = %#v", opts)
	}
}

func TestGatewayBattleRideFieldsFlag(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "default", want: false},
		{name: "explicit", args: []string{"-battle-ride-fields"}, want: true},
		{name: "explicit false", args: []string{"-battle-ride-fields=false"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, _, err := configFromCommandLine(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if opts.battleRideFields != test.want {
				t.Fatalf("battle ride fields = %v, want %v", opts.battleRideFields, test.want)
			}
		})
	}
}

func TestGatewayTrustedProxyHostsFromEnvironmentAndFlag(t *testing.T) {
	t.Setenv("STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS", "web,127.0.0.1")
	opts, _, err := configFromCommandLine([]string{"-trusted-proxy-hosts", "gateway"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.trustedProxyHosts != "gateway" {
		t.Fatalf("trusted proxy hosts = %q, want gateway", opts.trustedProxyHosts)
	}
	t.Setenv("STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS", "web,127.0.0.1")
	opts, _, err = configFromCommandLine(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.trustedProxyHosts != "web,127.0.0.1" {
		t.Fatalf("trusted proxy hosts from environment = %q", opts.trustedProxyHosts)
	}
}

type gatewayTestConn struct {
	net.Conn
	remote net.Addr
}

func (connection *gatewayTestConn) RemoteAddr() net.Addr {
	return connection.remote
}

type gatewayUpstreamRead struct {
	packet []byte
	err    error
}

func startGatewayTestUpstream(t *testing.T) (string, <-chan gatewayUpstreamRead) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan gatewayUpstreamRead, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			result <- gatewayUpstreamRead{err: acceptErr}
			return
		}
		defer connection.Close()
		if _, writeErr := connection.Write([]byte{'L', 0}); writeErr != nil {
			result <- gatewayUpstreamRead{err: writeErr}
			return
		}
		packet, readErr := bufio.NewReader(connection).ReadBytes('\n')
		result <- gatewayUpstreamRead{packet: packet, err: readErr}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), result
}

func startGatewayTestBattleUpstream(t *testing.T, response []byte) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			result <- acceptErr
			return
		}
		defer connection.Close()
		if _, writeErr := connection.Write([]byte{'L', 0}); writeErr != nil {
			result <- writeErr
			return
		}
		if _, readErr := bufio.NewReader(connection).ReadBytes('\n'); readErr != nil {
			result <- readErr
			return
		}
		_, writeErr := connection.Write(response)
		result <- writeErr
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), result
}

func gatewayTestLoginPacket(t *testing.T, account, password string) []byte {
	t.Helper()
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

func gatewayTestProxyPacket(t *testing.T, sourceIP, account, password string) []byte {
	t.Helper()
	header := fmt.Sprintf("PROXY TCP4 %s 127.0.0.1 12345 9065\r\n", sourceIP)
	return append([]byte(header), gatewayTestLoginPacket(t, account, password)...)
}

func runGatewayTestLogin(t *testing.T, store *auth.Store, remoteIP, trustedHosts string, payload []byte, wantResponse bool) (error, gatewayUpstreamRead) {
	t.Helper()
	upstreamAddress, upstreamResult := startGatewayTestUpstream(t)
	client, gateway := net.Pipe()
	defer client.Close()
	connection := &gatewayTestConn{Conn: gateway, remote: &net.TCPAddr{IP: net.ParseIP(remoteIP), Port: 49152}}
	errorsChannel := make(chan error, 1)
	go func() {
		errorsChannel <- handleConnection(connection, options{
			upstreamAddress:   upstreamAddress,
			authRequired:      true,
			trustedProxyHosts: trustedHosts,
			stateDelay:        0,
		}, store, log.New(io.Discard, "", 0))
	}()
	var greeting [2]byte
	if _, err := io.ReadFull(client, greeting[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(greeting[:], []byte{'L', 0}) {
		t.Fatalf("gateway greeting = %x, want L\\x00", greeting)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	if wantResponse {
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		response, err := bufio.NewReader(client).ReadBytes('\n')
		if err != nil {
			t.Fatalf("read rejected login response: %v", err)
		}
		raw, err := namedproto.DecodePacket(response)
		if err != nil {
			t.Fatalf("decode rejected login response: %v", err)
		}
		message, err := namedproto.ParseMessage(raw)
		if err != nil || message.Function != "ClientLogin" || len(message.Fields) != 1 {
			t.Fatalf("rejected login response = %#v, %v", message, err)
		}
		result, err := namedproto.DecodeString(message.Fields[0])
		if err != nil || string(result) != "no" {
			t.Fatalf("rejected login result = %q, %v", result, err)
		}
	}
	_ = client.Close()
	var connectionErr error
	select {
	case connectionErr = <-errorsChannel:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway connection did not finish")
	}
	var upstreamRead gatewayUpstreamRead
	select {
	case upstreamRead = <-upstreamResult:
	case <-time.After(3 * time.Second):
		t.Fatal("test upstream did not observe gateway connection")
	}
	return connectionErr, upstreamRead
}

func gatewayTestBattleResponse(t *testing.T, command []byte) []byte {
	t.Helper()
	fields := protocol.NewFieldEncoder("probe" + protocol.RunningKey)
	fields.String(command)
	raw, err := fields.Finish(15)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := protocol.EncodeMessage(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func runGatewayTestBattle(t *testing.T, store *auth.Store, remoteIP, trustedHosts string, payload []byte, battleRideFields bool) ([]byte, error) {
	t.Helper()
	const fullCommand = "BC|0|0|ProbeHero||1234|1|35|35|5|0||0|0|0|"
	upstreamAddress, upstreamResult := startGatewayTestBattleUpstream(t, gatewayTestBattleResponse(t, []byte(fullCommand)))
	client, gateway := net.Pipe()
	defer client.Close()
	connection := &gatewayTestConn{Conn: gateway, remote: &net.TCPAddr{IP: net.ParseIP(remoteIP), Port: 49152}}
	errorsChannel := make(chan error, 1)
	go func() {
		errorsChannel <- handleConnection(connection, options{
			upstreamAddress:   upstreamAddress,
			authRequired:      true,
			trustedProxyHosts: trustedHosts,
			battleRideFields:  battleRideFields,
			stateDelay:        0,
		}, store, log.New(io.Discard, "", 0))
	}()
	var greeting [2]byte
	if _, err := io.ReadFull(client, greeting[:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(greeting[:], []byte{'L', 0}) {
		t.Fatalf("gateway greeting = %x, want L\\x00", greeting)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	packet, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read translated battle packet: %v", err)
	}
	command, err := namedBattleCommand(packet)
	if err != nil {
		t.Fatalf("decode translated battle packet: %v", err)
	}
	_ = client.Close()
	select {
	case connectionErr := <-errorsChannel:
		if connectionErr != nil && !errors.Is(connectionErr, io.EOF) {
			return nil, connectionErr
		}
	case <-time.After(3 * time.Second):
		return nil, errors.New("gateway battle connection did not finish")
	}
	select {
	case upstreamErr := <-upstreamResult:
		if upstreamErr != nil {
			return nil, upstreamErr
		}
	case <-time.After(3 * time.Second):
		return nil, errors.New("battle test upstream did not finish")
	}
	return command, nil
}

func findGatewayAudit(t *testing.T, store *auth.Store, event string) (string, bool) {
	t.Helper()
	events, err := store.RecentAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range events {
		if item.Event == event {
			return item.SourceIP, true
		}
	}
	return "", false
}

func TestGatewayProxyProtocolAuditAndPeerTrust(t *testing.T) {
	tests := []struct {
		name               string
		remoteIP           string
		trustedHosts       string
		payload            func(*testing.T) []byte
		wantEvent          string
		wantSource         string
		wantAuthReply      bool
		wantUpstreamPacket bool
	}{
		{
			name:               "trusted proxy records forwarded source on success",
			remoteIP:           "127.0.0.1",
			trustedHosts:       "127.0.0.1",
			payload:            func(t *testing.T) []byte { return gatewayTestProxyPacket(t, "203.0.113.7", "probe", "local") },
			wantEvent:          "game_login_success",
			wantSource:         "203.0.113.7",
			wantUpstreamPacket: true,
		},
		{
			name:               "trusted service hostname records forwarded source",
			remoteIP:           "127.0.0.1",
			trustedHosts:       "localhost",
			payload:            func(t *testing.T) []byte { return gatewayTestProxyPacket(t, "203.0.113.9", "probe", "local") },
			wantEvent:          "game_login_success",
			wantSource:         "203.0.113.9",
			wantUpstreamPacket: true,
		},
		{
			name:               "native client keeps peer source",
			remoteIP:           "127.0.0.1",
			trustedHosts:       "127.0.0.1",
			payload:            func(t *testing.T) []byte { return gatewayTestLoginPacket(t, "probe", "local") },
			wantEvent:          "game_login_success",
			wantSource:         "127.0.0.1",
			wantAuthReply:      false,
			wantUpstreamPacket: true,
		},
		{
			name:               "trusted proxy records forwarded source on failure",
			remoteIP:           "127.0.0.1",
			trustedHosts:       "127.0.0.1",
			payload:            func(t *testing.T) []byte { return gatewayTestProxyPacket(t, "198.51.100.9", "probe", "wrong") },
			wantEvent:          "game_login_failed",
			wantSource:         "198.51.100.9",
			wantAuthReply:      true,
			wantUpstreamPacket: false,
		},
		{
			name:               "untrusted proxy cannot submit source",
			remoteIP:           "127.0.0.1",
			trustedHosts:       "127.0.0.2",
			payload:            func(t *testing.T) []byte { return gatewayTestProxyPacket(t, "203.0.113.8", "probe", "local") },
			wantSource:         "",
			wantUpstreamPacket: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			connectionErr, upstreamRead := runGatewayTestLogin(t, store, test.remoteIP, test.trustedHosts, test.payload(t), test.wantAuthReply)
			if test.wantEvent == "" {
				if connectionErr == nil || !strings.Contains(connectionErr.Error(), "untrusted peer") {
					t.Fatalf("untrusted PROXY connection error = %v", connectionErr)
				}
				if source, found := findGatewayAudit(t, store, "game_login_success"); found {
					t.Fatalf("untrusted PROXY unexpectedly recorded success source %q", source)
				}
				if source, found := findGatewayAudit(t, store, "game_login_failed"); found {
					t.Fatalf("untrusted PROXY unexpectedly recorded failure source %q", source)
				}
			} else {
				if connectionErr != nil {
					t.Fatalf("gateway connection error = %v", connectionErr)
				}
				source, found := findGatewayAudit(t, store, test.wantEvent)
				if !found || source != test.wantSource {
					t.Fatalf("audit %s source = %q found=%v, want %q", test.wantEvent, source, found, test.wantSource)
				}
			}
			if test.wantUpstreamPacket && len(upstreamRead.packet) == 0 {
				t.Fatalf("trusted login did not reach upstream: %v", upstreamRead.err)
			}
			if !test.wantUpstreamPacket && len(upstreamRead.packet) != 0 {
				t.Fatalf("login unexpectedly reached upstream: %q", upstreamRead.packet)
			}
		})
	}
}

func TestGatewayBattleRideFieldsSelection(t *testing.T) {
	const fullCommand = "BC|0|0|ProbeHero||1234|1|35|35|5|0||0|0|0|"
	const legacyCommand = "BC|0|0|ProbeHero||1234|1|35|35|5|"
	tests := []struct {
		name             string
		payload          func(*testing.T) []byte
		trustedHosts     string
		battleRideFields bool
		want             string
	}{
		{
			name:         "native client keeps legacy fields",
			payload:      func(t *testing.T) []byte { return gatewayTestLoginPacket(t, "probe", "local") },
			trustedHosts: "127.0.0.1",
			want:         legacyCommand,
		},
		{
			name:         "trusted Web proxy receives ride fields",
			payload:      func(t *testing.T) []byte { return gatewayTestProxyPacket(t, "203.0.113.7", "probe", "local") },
			trustedHosts: "127.0.0.1",
			want:         fullCommand,
		},
		{
			name:             "explicit flag enables ride fields",
			payload:          func(t *testing.T) []byte { return gatewayTestLoginPacket(t, "probe", "local") },
			trustedHosts:     "127.0.0.1",
			battleRideFields: true,
			want:             fullCommand,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			got, err := runGatewayTestBattle(t, store, "127.0.0.1", test.trustedHosts, test.payload(t), test.battleRideFields)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("battle command = %q, want %q", got, test.want)
			}
		})
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
