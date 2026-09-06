// stoneage-gateway lets the preserved function-name protocol client talk to
// the available numeric-protocol StoneAge 2.5 Linux GMSV.
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
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/gameservers"
	"github.com/k0ngk0ng/stoneage/server/go/bridge"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
	"github.com/pelletier/go-toml/v2"
)

const maximumPacketSize = 4 * 1024 * 1024

const legacyStateTransitionDelay = 100 * time.Millisecond

type options struct {
	catalogAddress  string
	servers         []gameservers.Server
	listenAddress   string
	upstreamAddress string
	routes          string
	trace           bool
	traceBattle     bool
	stateDelay      time.Duration
	authDB          string
	authRequired    bool
}

type gatewayRoute struct {
	listenAddress   string
	upstreamAddress string
}

// gatewayConfigFile is intentionally limited to process routing and tracing.
// Authentication stays in the deployment environment so the gateway and the
// admin service continue to share the same account database configuration.
// Pointer fields preserve the distinction between an omitted TOML value and an
// explicit false/empty value when command-line flags are merged below.
type gatewayConfigFile struct {
	CatalogListenAddress *string              `toml:"catalog_listen_address"`
	Servers              []gameservers.Server `toml:"servers"`
	ListenAddress        *string              `toml:"listen_address"`
	UpstreamAddress      *string              `toml:"upstream_address"`
	Trace                *bool                `toml:"trace"`
	TraceBattle          *bool                `toml:"trace_battle"`
}

func main() {
	opts, configPath, err := configFromCommandLine(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	if configPath != "" {
		log.Printf("gateway configuration loaded from %s", configPath)
	}
	if opts.authDB != "" {
		opts.authRequired = true
	}

	logger := log.New(os.Stdout, "stoneage-gateway: ", log.LstdFlags|log.Lmicroseconds)
	if err := serve(opts, logger); err != nil {
		logger.Fatal(err)
	}
}

func configFromCommandLine(arguments []string) (options, string, error) {
	opts := options{
		listenAddress:   "127.0.0.1:9065",
		upstreamAddress: "127.0.0.1:19065",
		routes:          os.Getenv("STONEAGE_GATEWAY_ROUTES"),
		authDB:          os.Getenv("STONEAGE_AUTH_DB"),
		authRequired:    envBool("STONEAGE_AUTH_REQUIRED", false),
		stateDelay:      legacyStateTransitionDelay,
	}
	var configPath string
	flags := flag.NewFlagSet("stoneage-gateway", flag.ContinueOnError)
	flags.StringVar(&configPath, "config", "", "path to gateway TOML configuration")
	flags.StringVar(&opts.listenAddress, "listen", opts.listenAddress, "client-facing listen address")
	flags.StringVar(&opts.upstreamAddress, "upstream", opts.upstreamAddress, "numeric GMSV address")
	flags.StringVar(&opts.catalogAddress, "catalog-listen", "", "HTTP server directory listen address (empty disables)")
	flags.StringVar(&opts.routes, "routes", opts.routes, "semicolon-separated listener=GMSV route list; overrides -listen/-upstream")
	flags.BoolVar(&opts.trace, "trace", opts.trace, "log translated function names (never logs password fields)")
	flags.BoolVar(&opts.traceBattle, "trace-battle", opts.traceBattle, "log server battle command bytes as hex (never logs password fields)")
	flags.StringVar(&opts.authDB, "auth-db", opts.authDB, "SQLite account database (enables login authentication)")
	flags.BoolVar(&opts.authRequired, "auth-required", opts.authRequired, "reject game logins not accepted by the account database")
	if err := flags.Parse(arguments); err != nil {
		return options{}, "", fmt.Errorf("parse gateway arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, configPath, fmt.Errorf("unexpected gateway arguments: %s", strings.Join(flags.Args(), " "))
	}
	configPath = strings.TrimSpace(configPath)
	if configPath != "" {
		fileConfig, err := loadGatewayConfigFile(configPath)
		if err != nil {
			return options{}, configPath, err
		}
		if !flagWasSet(flags, "catalog-listen") && fileConfig.CatalogListenAddress != nil {
			opts.catalogAddress = *fileConfig.CatalogListenAddress
		}
		opts.servers = fileConfig.Servers
		if len(opts.servers) > 0 && (flagWasSet(flags, "listen") || flagWasSet(flags, "upstream")) {
			return options{}, configPath, fmt.Errorf("servers configuration cannot be combined with -listen/-upstream")
		}
		if !flagWasSet(flags, "listen") && fileConfig.ListenAddress != nil {
			opts.listenAddress = *fileConfig.ListenAddress
		}
		if !flagWasSet(flags, "upstream") && fileConfig.UpstreamAddress != nil {
			opts.upstreamAddress = *fileConfig.UpstreamAddress
		}
		if !flagWasSet(flags, "trace") && fileConfig.Trace != nil {
			opts.trace = *fileConfig.Trace
		}
		if !flagWasSet(flags, "trace-battle") && fileConfig.TraceBattle != nil {
			opts.traceBattle = *fileConfig.TraceBattle
		}
	}
	return opts, configPath, nil
}

func loadGatewayConfigFile(filename string) (gatewayConfigFile, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return gatewayConfigFile{}, fmt.Errorf("read gateway TOML config %q: %w", filename, err)
	}
	var config gatewayConfigFile
	decoder := toml.NewDecoder(bytes.NewReader(content)).DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return gatewayConfigFile{}, fmt.Errorf("decode gateway TOML config %q: %w", filename, err)
	}
	return config, nil
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			set = true
		}
	})
	return set
}

func serve(opts options, logger *log.Logger) error {
	servers, err := configuredServers(opts)
	if err != nil {
		return err
	}
	routes, err := configuredRoutes(opts)
	if err != nil {
		return err
	}
	var accountStore *auth.Store
	if opts.authRequired {
		if opts.authDB == "" {
			return fmt.Errorf("authentication is required but -auth-db is empty")
		}
		var err error
		accountStore, err = auth.Open(opts.authDB)
		if err != nil {
			return err
		}
		defer accountStore.Close()
		if err := accountStore.Migrate(context.Background()); err != nil {
			return err
		}
		logger.Printf("game authentication required; database=%s", opts.authDB)
	} else {
		logger.Printf("WARNING: game authentication disabled; use -auth-required for deployment")
	}
	listeners := make([]net.Listener, 0, len(routes))
	for _, route := range routes {
		listener, listenErr := net.Listen("tcp", route.listenAddress)
		if listenErr != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return fmt.Errorf("listen on %s: %w", route.listenAddress, listenErr)
		}
		listeners = append(listeners, listener)
		logger.Printf("listening on %s -> numeric GMSV %s", route.listenAddress, route.upstreamAddress)
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()

	errorsCh := make(chan error, len(listeners)+1)
	if opts.catalogAddress != "" {
		listener, err := net.Listen("tcp", opts.catalogAddress)
		if err != nil {
			return fmt.Errorf("listen server directory: %w", err)
		}
		server := &http.Server{Handler: catalogHandler(servers), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute}
		defer server.Close()
		go func() { errorsCh <- server.Serve(listener) }()
		logger.Printf("server directory listening on %s", opts.catalogAddress)
	} else if len(listeners) == 0 {
		return fmt.Errorf("no enabled game listeners or catalog listener")
	}

	for index, listener := range listeners {
		route := routes[index]
		go acceptConnections(listener, route, opts, accountStore, logger, errorsCh)
	}
	return <-errorsCh
}

func acceptConnections(
	listener net.Listener,
	route gatewayRoute,
	opts options,
	accountStore *auth.Store,
	logger *log.Logger,
	errorsCh chan<- error,
) {
	for {
		client, err := listener.Accept()
		if err != nil {
			errorsCh <- fmt.Errorf("accept on %s: %w", route.listenAddress, err)
			return
		}
		go func() {
			connectionOptions := opts
			connectionOptions.upstreamAddress = route.upstreamAddress
			if err := handleConnection(client, connectionOptions, accountStore, logger); err != nil && !errors.Is(err, io.EOF) {
				logger.Printf("connection %s ended: %v", client.RemoteAddr(), err)
			}
		}()
	}
}

func configuredRoutes(opts options) ([]gatewayRoute, error) {
	if len(opts.servers) > 0 {
		servers, err := configuredServers(opts)
		if err != nil {
			return nil, err
		}
		routes := make([]gatewayRoute, 0, len(servers))
		for _, s := range servers {
			if !s.Disabled {
				routes = append(routes, gatewayRoute{listenAddress: s.ListenAddress, upstreamAddress: s.UpstreamAddress})
			}
		}
		return routes, nil
	}

	if strings.TrimSpace(opts.routes) == "" {
		if err := validateTCPAddress(opts.listenAddress, "listen address"); err != nil {
			return nil, err
		}
		if err := validateTCPAddress(opts.upstreamAddress, "upstream address"); err != nil {
			return nil, err
		}
		return []gatewayRoute{{listenAddress: opts.listenAddress, upstreamAddress: opts.upstreamAddress}}, nil
	}
	var routes []gatewayRoute
	for index, raw := range strings.Split(opts.routes, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("gateway route %d is empty", index+1)
		}
		listenAddress, upstreamAddress, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(listenAddress) == "" || strings.TrimSpace(upstreamAddress) == "" {
			return nil, fmt.Errorf("invalid gateway route %q (want listen-address=upstream-address)", raw)
		}
		listenAddress = strings.TrimSpace(listenAddress)
		upstreamAddress = strings.TrimSpace(upstreamAddress)
		if err := validateTCPAddress(listenAddress, "listen address"); err != nil {
			return nil, fmt.Errorf("gateway route %d: %w", index+1, err)
		}
		if err := validateTCPAddress(upstreamAddress, "upstream address"); err != nil {
			return nil, fmt.Errorf("gateway route %d: %w", index+1, err)
		}
		routes = append(routes, gatewayRoute{listenAddress: listenAddress, upstreamAddress: upstreamAddress})
	}
	return routes, nil
}

func validateTCPAddress(address, label string) error {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, address, err)
	}
	return nil
}

func handleConnection(client net.Conn, opts options, accountStore *auth.Store, logger *log.Logger) error {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp", opts.upstreamAddress, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to numeric GMSV %s: %w", opts.upstreamAddress, err)
	}
	defer upstream.Close()

	greeting := make([]byte, 2)
	if _, err := io.ReadFull(upstream, greeting); err != nil {
		return fmt.Errorf("read numeric GMSV greeting: %w", err)
	}
	if greeting[0] != 'L' || greeting[1] != 0 {
		return fmt.Errorf("unexpected numeric GMSV greeting %x", greeting)
	}
	if _, err := client.Write(greeting); err != nil {
		return fmt.Errorf("write client greeting: %w", err)
	}
	logger.Printf("client %s connected", client.RemoteAddr())

	translator := bridge.NewTranslator()
	var clientReader *bufio.Reader
	var firstClientPacket []byte
	if opts.authRequired {
		clientReader = bufio.NewReaderSize(client, 128*1024)
		_ = client.SetReadDeadline(time.Now().Add(15 * time.Second))
		firstClientPacket, err = readPacket(clientReader)
		_ = client.SetReadDeadline(time.Time{})
		if err != nil {
			return fmt.Errorf("read login packet: %w", err)
		}
		accepted, account, err := authenticateClientLogin(firstClientPacket, accountStore, remoteHost(client.RemoteAddr()))
		if err != nil {
			return err
		}
		if !accepted {
			logger.Printf("game login rejected account=%q source=%s", account, remoteHost(client.RemoteAddr()))
			response, responseErr := clientLoginResponse("no")
			if responseErr != nil {
				return responseErr
			}
			if err := writeAll(client, response); err != nil {
				return fmt.Errorf("write rejected login response: %w", err)
			}
			return nil
		}
		logger.Printf("game login accepted account=%q source=%s", account, remoteHost(client.RemoteAddr()))
	}
	var workers sync.WaitGroup
	workers.Add(2)
	errChannel := make(chan error, 2)
	go translateLoop(&workers, errChannel, client, upstream, "client->GMSV", translator.ClientToServer, opts.trace, opts.traceBattle, opts.stateDelay, logger, clientReader, firstClientPacket)
	go translateLoop(&workers, errChannel, upstream, client, "GMSV->client", translator.ServerToClient, opts.trace, opts.traceBattle, opts.stateDelay, logger, nil, nil)
	workers.Wait()
	close(errChannel)
	for workerError := range errChannel {
		if workerError != nil && !errors.Is(workerError, io.EOF) {
			return workerError
		}
	}
	return nil
}

type translationFunction func([]byte) ([]byte, string, error)

func translateLoop(
	workers *sync.WaitGroup,
	errors chan<- error,
	source net.Conn,
	destination net.Conn,
	direction string,
	translate translationFunction,
	trace bool,
	traceBattle bool,
	stateDelay time.Duration,
	logger *log.Logger,
	reader *bufio.Reader,
	initialPacket []byte,
) {
	defer workers.Done()
	if reader == nil {
		reader = bufio.NewReaderSize(source, 128*1024)
	}
	for {
		packet := initialPacket
		initialPacket = nil
		var err error
		if packet == nil {
			packet, err = readPacket(reader)
		}
		if err != nil {
			if tcp, ok := destination.(*net.TCPConn); ok {
				_ = tcp.CloseWrite()
			}
			errors <- err
			return
		}
		translated, function, err := translate(packet)
		if err != nil {
			errors <- fmt.Errorf("%s %s: %w", direction, function, err)
			_ = destination.Close()
			_ = source.Close()
			return
		}
		if trace {
			logger.Printf("%s function=%s input=%d output=%d", direction, function, len(packet), len(translated))
		}
		if traceBattle && direction == "GMSV->client" && function == "B" {
			if command, decodeError := namedBattleCommand(translated); decodeError != nil {
				logger.Printf("%s function=B battle_command_decode_error=%v", direction, decodeError)
			} else {
				logger.Printf("%s function=B battle_command_hex=%x", direction, command)
			}
		}
		// The archived client sets its WGS callback-waiting flag just after
		// send(). A loopback GMSV can answer ClientLogin in roughly 5 ms, fast
		// enough for the response to be dispatched before that flag is set and
		// silently discarded. The original Internet gateway could never hit
		// this race. Preserve a small one-time WAN-like delay at login only;
		// gameplay packets remain latency-free.
		if delay := stateTransitionDelay(direction, function, false, stateDelay); delay > 0 {
			time.Sleep(delay)
		}
		if err := writeAll(destination, translated); err != nil {
			errors <- fmt.Errorf("%s write: %w", direction, err)
			return
		}
		// CharLogin changes the client from its character-select loop to the
		// gameplay loop on a later render tick. The local GMSV queues the entire
		// initial status/map burst in the same millisecond; without a small gap,
		// the preserved client dispatches those packets while its gameplay
		// globals are still uninitialised. EN has the same problem: the client
		// enters its battle loop on a later render tick and clears the BP/BC/BA
		// receive queues during that transition. A real WAN provided both gaps
		// naturally.
		if delay := stateTransitionDelay(direction, function, true, stateDelay); delay > 0 {
			time.Sleep(delay)
		}
	}
}

func authenticateClientLogin(packet []byte, accountStore *auth.Store, sourceIP string) (bool, string, error) {
	if accountStore == nil {
		return false, "", errors.New("authentication store is unavailable")
	}
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return false, "", fmt.Errorf("decode login packet: %w", err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return false, "", err
	}
	if message.Function != "ClientLogin" || len(message.Fields) != 2 {
		return false, "", fmt.Errorf("authentication requires ClientLogin as the first packet")
	}
	accountBytes, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		return false, "", fmt.Errorf("decode login account: %w", err)
	}
	passwordBytes, err := namedproto.DecodeString(message.Fields[1])
	if err != nil {
		return false, "", fmt.Errorf("decode login password: %w", err)
	}
	account := auth.CanonicalGameUsername(string(accountBytes))
	if _, err := accountStore.Authenticate(context.Background(), account, passwordBytes, sourceIP); err != nil {
		return false, account, nil
	}
	return true, account, nil
}

func clientLoginResponse(result string) ([]byte, error) {
	raw, err := namedproto.RawMessage(1, "ClientLogin", []string{result})
	if err != nil {
		return nil, err
	}
	return namedproto.EncodePacket(raw)
}

func remoteHost(address net.Addr) string {
	if address == nil {
		return ""
	}
	value := address.String()
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func envBool(name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func namedBattleCommand(packet []byte) ([]byte, error) {
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return nil, err
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return nil, err
	}
	if message.Function != "B" || len(message.Fields) != 1 {
		return nil, fmt.Errorf("expected one B field, got %s/%d", message.Function, len(message.Fields))
	}
	return namedproto.DecodeString(message.Fields[0])
}

func stateTransitionDelay(direction, function string, packetWritten bool, delay time.Duration) time.Duration {
	if direction != "GMSV->client" {
		return 0
	}
	if function == "ClientLogin" && !packetWritten {
		return delay
	}
	if function == "CharLogin" && packetWritten {
		return delay
	}
	if function == "EN" && packetWritten {
		return delay
	}
	return 0
}

func readPacket(reader *bufio.Reader) ([]byte, error) {
	packet, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(packet) > maximumPacketSize {
		return nil, fmt.Errorf("packet exceeds %d bytes", maximumPacketSize)
	}
	return packet, nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		value = value[written:]
	}
	return nil
}
