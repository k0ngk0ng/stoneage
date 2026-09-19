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
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

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
	switch os.Args[1] {
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
	var configPath string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config", "-config":
			if index+1 >= len(args) {
				return errors.New("--config requires a path")
			}
			configPath = args[index+1]
			index++
		default:
			return fmt.Errorf("unknown serve argument %q", args[index])
		}
	}
	config, usedPath, err := sacli.LoadConfigPath(configPath)
	if err != nil {
		return err
	}
	if usedPath == "" {
		fmt.Fprintf(os.Stderr, "sactl: no config file found (searched %s); using defaults — set account/password before starting\n",
			strings.Join(sacli.ConfigSearchPaths(), ", "))
	} else {
		fmt.Printf("sactl: using config %s\n", usedPath)
	}
	server := sacli.NewServer(config)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("sactl: serving on %s (gateway %s, account %s, character %s)\n",
		config.SocketPath, config.Address, config.Account, config.Character)
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
	options := clientOptions{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h" || (arg == "help" && command == ""):
			// Help must work wherever the flags sit: a wrapper script may put
			// its own flags before the command name.
			usage()
			return nil
		case arg == "--json":
			options.json = true
		case arg == "--socket" || arg == "-socket":
			if index+1 >= len(args) {
				return errors.New("--socket requires a path")
			}
			options.socket = args[index+1]
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
		case strings.HasPrefix(arg, "--"):
			return fmt.Errorf("unknown flag %q", arg)
		case command == "":
			command = arg
		default:
			commandArgs = append(commandArgs, arg)
		}
	}
	if command == "" {
		usage()
		os.Exit(exitUsage)
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
		fmt.Fprintf(os.Stderr, "sactl: is the daemon running? start it with `sactl serve --config <file>`\n")
		os.Exit(exitNoDaemon)
	}
	printResponse(response, options.json)
	os.Exit(exitCodeFor(response))
	return nil
}

type clientOptions struct {
	socket  string
	config  string
	json    bool
	timeout time.Duration
}

func (options clientOptions) socketPath() (string, error) {
	if options.socket != "" {
		return options.socket, nil
	}
	if env := os.Getenv("STONEAGE_SACTL_SOCKET"); env != "" {
		return env, nil
	}
	config, _, err := sacli.LoadConfigPath(options.config)
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
  sactl serve --config <file>            hold one game session and serve the CLI
%s

global flags:
  --socket <path>   daemon socket (default %s)
  --config <file>   read socket_path from a sactl config file
  --timeout <dur>   per-command timeout (default %s)
  --json            print the structured result

environment:
  STONEAGE_SACTL_SOCKET   daemon socket path
`, sacli.CommandHelp, sacli.DefaultConfig().SocketPath, sacli.DefaultRequestTimeout)
}
