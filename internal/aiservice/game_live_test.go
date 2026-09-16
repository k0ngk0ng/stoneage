package aiservice

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestLiveGameSessionMovementAndOwnState(t *testing.T) {
	if os.Getenv("STONEAGE_GAME_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_GAME_LIVE_TEST=1 for the isolated local game fixture")
	}
	address := os.Getenv("STONEAGE_GAME_TEST_ADDRESS")
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("the live game fixture requires an explicit loopback address")
	}
	account := os.Getenv("STONEAGE_GAME_TEST_ACCOUNT")
	password := os.Getenv("STONEAGE_GAME_TEST_PASSWORD")
	character := os.Getenv("STONEAGE_GAME_TEST_CHARACTER")
	if account == "" || password == "" || character == "" {
		t.Fatal("the live fixture account, password and character must be supplied")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := aigame.Login(ctx, aigame.Config{Address: address}, aigame.Credentials{Account: account, Password: password})
	if err != nil {
		t.Fatalf("live login: %v", err)
	}
	defer session.Close()
	if err := session.EnterCharacter(ctx, character); err != nil {
		t.Fatalf("live character entry: %v", err)
	}
	waitLiveSnapshot(t, ctx, session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Player.HasStatus && snapshot.Position.Floor > 0 && snapshot.Phase == aigame.PhaseWorld
	})
	t.Run("server-own-state", func(t *testing.T) {
		query, queryCancel := context.WithTimeout(ctx, 4*time.Second)
		defer queryCancel()
		if err := session.Execute(query, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"}); err != nil {
			t.Fatal(err)
		}
		snapshot := waitLiveSnapshot(t, query, session, func(snapshot aigame.Snapshot) bool { return snapshot.AI.Received })
		if snapshot.AI.Version != 1 || snapshot.AIObservationRevision == 0 {
			t.Fatal("the server did not supply a valid own-state extension")
		}
		t.Logf("server own-state observed: revision=%d stable_pets=%d", snapshot.AIObservationRevision, len(snapshot.AI.Pets))
	})
	t.Run("authoritative-movement", func(t *testing.T) {
		navigator, err := ainavigation.LoadDataDir(ctx, filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data"))
		if err != nil {
			t.Fatal(err)
		}
		gate := aicontrol.New()
		defer gate.Close()
		state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "local game verification")
		if err != nil {
			t.Fatal(err)
		}
		backend := &GameBackend{Session: session, Gate: gate, Owner: aicontrol.Agent,
			Binding: aimcp.Binding{AccountID: account, CharacterID: account + ":fixture", CharacterName: character, Generation: state.Generation}}
		observed, err := backend.Observe(ctx, backend.Binding)
		if err != nil {
			t.Fatal(err)
		}
		target, found := liveAdjacentDestination(ctx, navigator, observed)
		if !found {
			t.Fatal("no unoccupied adjacent tile has a verified route")
		}
		arguments, _ := json.Marshal(movementArguments{Floor: observed.Floor, X: target.X, Y: target.Y})
		skill := &MovementSkill{Backend: backend, Navigator: navigator}
		if err := skill.Execute(ctx, automation.Action{Skill: "move", Arguments: arguments, ExpectedRevision: observed.Revision}); err != nil {
			t.Fatalf("live movement: %v", err)
		}
		confirmed := session.Snapshot()
		if int(confirmed.Position.Floor) != observed.Floor || int(confirmed.Position.X) != target.X || int(confirmed.Position.Y) != target.Y {
			t.Fatalf("server position did not confirm destination: %+v", confirmed.Position)
		}
		t.Logf("server-confirmed move: floor=%d (%d,%d)->(%d,%d)", observed.Floor, observed.X, observed.Y, target.X, target.Y)
	})
}

func waitLiveSnapshot(t *testing.T, ctx context.Context, session *aigame.Session, predicate func(aigame.Snapshot) bool) aigame.Snapshot {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := session.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("server observation timed out: phase=%s revision=%d own_state=%v", snapshot.Phase, snapshot.Revision, snapshot.AI.Received)
			return aigame.Snapshot{}
		case <-ticker.C:
		}
	}
}

func liveAdjacentDestination(ctx context.Context, navigator *ainavigation.Navigator, observed aimcp.Observation) (ainavigation.Point, bool) {
	start := ainavigation.Point{X: observed.X, Y: observed.Y}
	for _, delta := range []ainavigation.Point{{X: 1}, {Y: 1}, {X: -1}, {Y: -1}} {
		target := ainavigation.Point{X: start.X + delta.X, Y: start.Y + delta.Y}
		occupied := false
		for _, actor := range observed.Actors {
			if actor.X == target.X && actor.Y == target.Y {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		route, err := navigator.RouteContext(ctx, observed.Floor, start, target)
		if err == nil && len(route.Directions) == 1 {
			return target, true
		}
	}
	return ainavigation.Point{}, false
}
