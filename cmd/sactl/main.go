// Command sactl is the headless StoneAge game client used by AI players and
// by a human at a terminal.
//
//	sactl serve --config config/sactl/sactl.toml   # long-running session holder
//	sactl status                                    # one-shot commands
//	sactl goto 15 22
//	sactl say "hello"
//
// Every one-shot command talks to the daemon over a private Unix socket, so
// a terminal agent can drive the game with short, stateless shell commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/arenaagent"
	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

// version is set at build time with
// -ldflags "-X main.version=<release tag>"; release archives carry the tag,
// local builds report "dev".
var version = "dev"

// Exit codes. A terminal agent distinguishes "the game refused this action"
// from "the client is not set up", so they must not collapse into one value.
const (
	exitOK       = 0
	exitAction   = 1
	exitUsage    = 2
	exitNoDaemon = 3
	exitFailed   = 4
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	sacli.BuildVersion = version
	switch os.Args[1] {
	case "init":
		if err := initConfig(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "sactl init: %v\n", err)
			os.Exit(exitFailed)
		}
		return
	case "completion":
		if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			fmt.Println("usage: sactl completion <bash|zsh>")
			return
		}
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: sactl completion <bash|zsh>")
			os.Exit(exitUsage)
		}
		script, err := sacli.CompletionScript(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitUsage)
		}
		fmt.Print(script)
		return
	case "__complete":
		for _, candidate := range sacli.Complete(os.Args[2:]) {
			fmt.Println(candidate)
		}
		return
	case "ai":
		if err := arenaagent.Main(context.Background(), os.Args[2:], version, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "sactl ai: %v\n", err)
			os.Exit(exitFailed)
		}
		return
	case "version", "--version", "-v":
		fmt.Printf("sactl %s\n", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "sactl: %v\n", err)
			os.Exit(exitFailed)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "sactl: %v\n", err)
		os.Exit(exitFailed)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("sactl serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "custom configuration file")
	profile := fs.String("profile", "", "named player profile")
	foreground := fs.Bool("foreground", false, "keep daemon in foreground (for supervisors/debugging)")
	interactive := fs.Bool("interactive", false, "ignore legacy stored credentials (used by login)")
	socket := fs.String("socket", "", "local session socket")
	connection := connectionOptions{}
	connection.bind(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	if *profile == "" && *configPath == "" && *socket == "" {
		options := clientOptions{}
		if err := options.resolveProfile(); err != nil {
			return err
		}
		*profile = options.profile
	}
	loader := sacli.LoadProfileConfig
	if *interactive {
		loader = sacli.LoadClientProfileConfig
	}
	config, usedPath, err := loader(*profile, *configPath)
	if err != nil {
		return err
	}
	if err := connection.apply(&config); err != nil {
		return err
	}
	if *socket != "" {
		config.SocketPath = *socket
	}
	if !*foreground {
		return startBackground(config, usedPath, *profile)
	}
	fmt.Printf("sactl: using config %s\n", usedPath)
	server := sacli.NewServer(config)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("sactl: serving on %s (%s %s)\n", config.SocketPath, config.Transport, config.Endpoint())
	fmt.Printf("sactl: run `sactl status` in another terminal; stop with `sactl stop` or Ctrl-C\n")
	if err := server.Serve(ctx); err != nil {
		return err
	}
	fmt.Println("sactl: stopped")
	return nil
}

// run executes one command against the daemon.
func run(args []string) error {
	command := ""
	commandArgs := make([]string, 0, len(args))
	options := clientOptions{connection: connectionOptions{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--version" || arg == "-v":
			fmt.Printf("sactl %s\n", version)
			return nil
		case arg == "--help" || arg == "-h" || (arg == "help" && command == ""):
			// Help must work wherever the flags sit: a wrapper script may put
			// its own flags before the command name.
			if command == "arena" || command == "ladder" {
				if err := sacli.ValidateArenaCommand(commandArgs); err != nil {
					return err
				}
				fmt.Println(sacli.ArenaHelp)
				return nil
			}
			usage()
			return nil
		case arg == "--json":
			options.json = true
		case isConnectionFlag(arg):
			if index+1 >= len(args) {
				return fmt.Errorf("%s requires a value", arg)
			}
			options.connection[strings.TrimPrefix(arg, "--")] = args[index+1]
			index++
		case arg == "--socket" || arg == "-socket":
			if index+1 >= len(args) {
				return errors.New("--socket requires a path")
			}
			options.socket = args[index+1]
			index++
		case arg == "--profile":
			if index+1 >= len(args) {
				return errors.New("--profile requires a name")
			}
			options.profile = args[index+1]
			index++
		case arg == "--config" || arg == "-config":
			if index+1 >= len(args) {
				return errors.New("--config requires a path")
			}
			options.config = args[index+1]
			index++
		case arg == "--timeout" || arg == "-timeout":
			if index+1 >= len(args) {
				return errors.New("--timeout requires a duration")
			}
			parsed, err := time.ParseDuration(args[index+1])
			if err != nil {
				return fmt.Errorf("invalid --timeout: %w", err)
			}
			options.timeout = parsed
			index++
		case strings.HasPrefix(arg, "--") && command == "":
			return fmt.Errorf("unknown flag %q", arg)
		case command == "":
			command = arg
		default:
			// Subcommand flags (including ladder cursors and retry IDs)
			// belong to the daemon's command parser.
			commandArgs = append(commandArgs, arg)
		}
	}
	if command == "" {
		usage()
		os.Exit(exitUsage)
	}
	if command == "serve" || command == "init" {
		localArgs := append([]string{}, commandArgs...)
		localArgs = append(localArgs, options.connection.args()...)
		if options.profile != "" {
			localArgs = append(localArgs, "--profile", options.profile)
		}
		if options.config != "" {
			localArgs = append(localArgs, "--config", options.config)
		}
		if options.socket != "" || options.json || options.timeout != 0 {
			return errors.New("unsupported local command flags")
		}
		if command == "serve" {
			return serve(localArgs)
		}
		return initConfig(localArgs)
	}
	if command == "login" {
		if err := options.resolveProfile(); err != nil {
			return err
		}
		return login(options, commandArgs)
	}
	if command == "sessions" || command == "use" {
		return sessionCommand(command, commandArgs, options.json)
	}
	if err := options.resolveProfile(); err != nil {
		return err
	}
	if len(options.connection) != 0 {
		return errors.New("connection options apply to login or serve; game commands use the active session")
	}
	if command == "arena" || command == "ladder" {
		if err := sacli.ValidateArenaCommand(commandArgs); err != nil {
			return err
		}
		if len(commandArgs) > 0 && (commandArgs[0] == "--help" || commandArgs[0] == "-h" || commandArgs[0] == "help") {
			fmt.Println(sacli.ArenaHelp)
			return nil
		}
		// Keep the local RPC name compatible with already running daemons.
		command = "ladder"
	}
	socketPath, err := options.socketPath()
	if err != nil {
		return err
	}
	timeout := options.timeout
	if timeout <= 0 {
		timeout = sacli.DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	response, err := sacli.Call(ctx, socketPath, sacli.Request{
		Command: command,
		Args:    commandArgs,
		JSON:    options.json,
		Timeout: timeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sactl: %v\n", err)
		fmt.Fprintf(os.Stderr, "sactl: no available session; run `sactl login` using the same --profile or --config\n")
		os.Exit(exitNoDaemon)
	}
	if command == "status" {
		response.ClientVersion = version
		// Older daemons used an ambiguous version label.
		if strings.HasPrefix(response.Text, "version: ") {
			response.Text = "daemon " + response.Text
		}
		response.Text = fmt.Sprintf("client version: %s\n%s", version, response.Text)
	}
	printResponse(response, options.json)
	os.Exit(exitCodeFor(response))
	return nil
}

type clientOptions struct {
	connection connectionOptions
	profile    string
	socket     string
	config     string
	json       bool
	timeout    time.Duration
}

func (options clientOptions) socketPath() (string, error) {
	if options.profile != "" {
		if options.socket != "" {
			return "", errors.New("--profile and --socket cannot be combined")
		}
		config, _, err := sacli.LoadClientProfileConfig(options.profile, options.config)
		return config.SocketPath, err
	}
	if options.socket != "" {
		return options.socket, nil
	}
	if env := os.Getenv("STONEAGE_SACTL_SOCKET"); env != "" {
		return env, nil
	}
	config, _, err := sacli.LoadClientProfileConfig("", options.config)
	if err != nil {
		return "", err
	}
	return config.SocketPath, nil
}

func printResponse(response sacli.Response, asJSON bool) {
	if asJSON {
		payload := response
		encoded, err := json.Marshal(payload)
		if err == nil {
			fmt.Println(string(encoded))
			return
		}
	}
	if response.Text != "" {
		fmt.Println(response.Text)
	}
	if !response.OK && response.Error != "" {
		if response.Text == "" {
			fmt.Fprintln(os.Stderr, "sactl: "+response.Error)
		}
	}
}

func exitCodeFor(response sacli.Response) int {
	if response.OK {
		return exitOK
	}
	switch response.Kind {
	case sacli.KindUsage:
		return exitUsage
	case sacli.KindSession:
		return exitNoDaemon
	default:
		return exitAction
	}
}

func usage() {
	fmt.Fprintf(os.Stdout, `sactl - headless StoneAge client

usage:
  sactl init [--profile <name>]         optionally save connection preferences
  sactl sessions                       list local sessions (* = active)
  sactl use <profile|default>           select the session used by subsequent commands
  sactl completion <bash|zsh>           print shell completion script
  sactl ai <init|check|run|train|evaluate|simulate> [options]  local squad commander
  sactl serve [--profile <name>]        start a background game session
%s

global flags:
  --version         print the client version
  --socket <path>   daemon socket (default %s)
  --profile <name>  select an isolated player profile
  --config <file>   read socket_path from a sactl config file
  --timeout <dur>   per-command timeout (default %s)
  --json            print the structured result

login/serve connection flags (CLI overrides optional config):
  --web-base-url <url>  Web endpoint (default https://sa.ichenj.com)
  --transport <http|tcp>  connection transport (default http)
  --address <host:port>  direct gateway; implies tcp unless transport is explicit
  --server-id <id>      game line (default first available)
  --character <name>    enter after login; failed entry keeps account logged in
  --map-directory <dir> optional 2.5 navigation data

environment:
  STONEAGE_SACTL_SOCKET   daemon socket path
`, sacli.CommandHelp, sacli.DefaultConfig().SocketPath, sacli.DefaultRequestTimeout)
}
