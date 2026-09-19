package sacli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/websession"
)

// Game is the narrow seam sactl depends on for the protocol session.
// *aigame.Session satisfies it today; replacing the protocol layer means
// replacing this adapter, not the command layer.
type Game interface {
	Observe(ctx context.Context) (aigame.Snapshot, error)
	Execute(ctx context.Context, action aigame.Action) error
	ExecuteExpected(ctx context.Context, expectedRevision uint64, action aigame.Action) error
	Characters() []aigame.Character
	RefreshCharacters(ctx context.Context) ([]aigame.Character, error)
	CreateCharacter(ctx context.Context, create aigame.CharacterCreate) error
	DeleteCharacter(ctx context.Context, name string) error
	// Logout leaves the world and closes the session (native CharLogout).
	Logout(ctx context.Context) error
	EnterCharacter(ctx context.Context, name string) error
	Events() <-chan aigame.Event
	// WaitForMapEvent correlates one EV acknowledgement. Only the native
	// session implements it today (internal/aigame/protocol.go:963), which is
	// what lets the CLI confirm warps.
	WaitForMapEvent(ctx context.Context, sequence int32) (aigame.Event, error)
	Close() error
}

var _ Game = (*aigame.Session)(nil)

// connect builds the game session for the configured transport, authenticates
// with ClientLogin and enters the requested character. Both transports yield
// the same *aigame.Session: the Web transport is the same protocol carried by
// the site's normal HTTP session endpoints, so every command works the same.
func connect(ctx context.Context, config Config, character string) (*aigame.Session, error) {
	var session *aigame.Session
	var err error
	switch config.Transport {
	case "http":
		session, err = connectWeb(ctx, config)
	default:
		session, err = aigame.Connect(ctx, aigame.Config{Address: config.Address},
			aigame.Credentials{Account: config.Account, Password: config.Password})
		if err != nil {
			err = fmt.Errorf("connect %s: %w", config.Address, err)
		}
	}
	if err != nil {
		return nil, err
	}
	if character != "" {
		if err := session.EnterCharacter(ctx, character); err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("enter character %q: %w", character, err)
		}
	}
	return session, nil
}

// connectWeb opens a session through the deployment's Web front end.
func connectWeb(ctx context.Context, config Config) (*aigame.Session, error) {
	client, err := websession.New(websession.Config{BaseURL: config.WebBaseURL, ServerID: config.ServerID})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", config.WebBaseURL, err)
	}
	serverID := config.ServerID
	if serverID == "" {
		serverID, err = firstWebServer(ctx, config.WebBaseURL)
		if err != nil {
			return nil, err
		}
	}
	connection, err := client.Dial(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("open web session on %s: %w", config.WebBaseURL, err)
	}
	session := aigame.NewSession(connection, aigame.Config{Address: serverID})
	if err := session.Authenticate(ctx, aigame.Credentials{Account: config.Account, Password: config.Password}); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("authenticate over %s: %w", config.WebBaseURL, err)
	}
	return session, nil
}

// firstWebServer asks the Web server directory for the first enabled line.
func firstWebServer(ctx context.Context, baseURL string) (string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/servers"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("list servers on %s: %w", baseURL, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("list servers on %s: %w", baseURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("list servers on %s: HTTP %d", baseURL, response.StatusCode)
	}
	var payload struct {
		Servers []struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("list servers on %s: %w", baseURL, err)
	}
	for _, server := range payload.Servers {
		if !server.Disabled && server.ID != "" {
			return server.ID, nil
		}
	}
	return "", fmt.Errorf("list servers on %s: no enabled game line", baseURL)
}

// directionLetter maps one user-facing direction to the native 2.5 wire
// alphabet: a=up, b=up-right, c=right, d=down-right, e=down, f=down-left,
// g=left, h=up-left (internal/ainavigation/types.go:87-90).
//
// Only names are accepted. The raw letters are ambiguous to a reader — "e"
// is the wire letter for down, not east — so they stay an internal detail.
func directionLetter(direction string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up", "north", "n":
		return "a", nil
	case "up-right", "upright", "northeast", "ne":
		return "b", nil
	case "right", "east", "e":
		return "c", nil
	case "down-right", "downright", "southeast", "se":
		return "d", nil
	case "down", "south", "s":
		return "e", nil
	case "down-left", "downleft", "southwest", "sw":
		return "f", nil
	case "left", "west", "w":
		return "g", nil
	case "up-left", "upleft", "northwest", "nw":
		return "h", nil
	default:
		return "", fmt.Errorf("unknown direction %q (use up/down/left/right or n/ne/e/se/s/sw/w/nw)", direction)
	}
}

// directionDelta returns the map delta for one wire direction letter. The
// 2.5 grid increases y downward, so "up" is y-1.
func directionDelta(letter string) (int, int) {
	switch letter {
	case "a":
		return 0, -1
	case "b":
		return 1, -1
	case "c":
		return 1, 0
	case "d":
		return 1, 1
	case "e":
		return 0, 1
	case "f":
		return -1, 1
	case "g":
		return -1, 0
	case "h":
		return -1, -1
	default:
		return 0, 0
	}
}
