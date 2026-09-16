// stoneage-game-mcp is the stdio MCP entry point used by one StoneAge AI
// player.  The process is intentionally bound to a role and control
// generation at startup; it does not accept credentials, a game address, or
// an account/character selector from MCP messages.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type options struct {
	characterID   string
	characterName string
	generation    uint64
	endpoint      string
	tokenFile     string
}

func main() {
	var opts options
	flags := flag.NewFlagSet("stoneage-game-mcp", flag.ExitOnError)
	flags.StringVar(&opts.characterID, "character-id", os.Getenv("STONEAGE_AI_CHARACTER_ID"), "fixed server character identity")
	flags.StringVar(&opts.characterName, "character-name", os.Getenv("STONEAGE_AI_CHARACTER_NAME"), "display name for the fixed character")
	generation := os.Getenv("STONEAGE_AI_CONTROL_GENERATION")
	if generation != "" {
		parsed, err := strconv.ParseUint(generation, 10, 64)
		if err != nil {
			fatalf("invalid STONEAGE_AI_CONTROL_GENERATION")
		}
		opts.generation = parsed
	}
	flags.Uint64Var(&opts.generation, "control-generation", opts.generation, "control fencing generation supplied by the owner")
	flags.StringVar(&opts.endpoint, "endpoint", os.Getenv("STONEAGE_AI_ENDPOINT"), "server-owned game gateway endpoint")
	flags.StringVar(&opts.tokenFile, "token-file", os.Getenv("STONEAGE_AI_TOKEN_FILE"), "file containing the game capability token")
	flags.Parse(os.Args[1:])
	if flags.NArg() > 1 || (flags.NArg() == 1 && flags.Arg(0) != "serve") {
		fatalf("unexpected arguments")
	}
	if strings.TrimSpace(opts.characterID) == "" || opts.generation == 0 {
		fatalf("character-id and non-zero control-generation are required")
	}
	token := strings.TrimSpace(os.Getenv("STONEAGE_AI_TOKEN"))
	if opts.tokenFile != "" {
		var err error
		token, err = readTokenFile(opts.tokenFile)
		if err != nil {
			fatalf("read game capability token")
		}
	}
	if strings.TrimSpace(opts.endpoint) == "" || token == "" {
		fatalf("endpoint and game capability token are required")
	}

	logger := log.New(os.Stderr, "stoneage-game-mcp: ", log.LstdFlags|log.Lmicroseconds)
	backend, err := aimcp.NewRemoteBackend(aimcp.RemoteBackendConfig{Endpoint: opts.endpoint, Token: token, RequestTimeout: 15 * time.Second})
	if err != nil {
		fatalf("create game backend")
	}
	server, err := aimcp.NewServer(backend, aimcp.Binding{
		CharacterID: opts.characterID, CharacterName: opts.characterName, Generation: opts.generation,
	}, aimcp.Config{Logger: logger})
	if err != nil {
		fatalf("create MCP server: %v", err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fatalf("serve MCP: %v", err)
	}
}

func readTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > 4096 {
		return "", fmt.Errorf("unsafe game capability token file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "stoneage-game-mcp: "+format+"\n", args...)
	os.Exit(2)
}
