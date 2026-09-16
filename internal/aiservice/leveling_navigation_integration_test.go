package aiservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// levelingIntegrationGame models the only server behavior this integration
// test needs: W moves update the authoritative coordinate, while an explicit
// EV at a reviewed mapwarp source changes the floor.
type levelingIntegrationGame struct {
	mu              sync.Mutex
	snapshot        aigame.Snapshot
	warps           map[aiknowledge.Point]aiknowledge.Point
	actions         []aigame.Action
	positionQueries int
}

func (g *levelingIntegrationGame) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot, nil
}

func (g *levelingIntegrationGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapshot.Revision != revision {
		return aigame.ErrStaleRevision
	}
	if action.Kind == aigame.ActionStatus && action.Command == "c" {
		// The test controls delayed position replies below. A read-only query
		// neither moves the character nor belongs to the mutation sequence.
		g.positionQueries++
		return nil
	}
	g.actions = append(g.actions, action)
	switch action.Kind {
	case aigame.ActionMove:
		applyLevelingIntegrationRoute(&g.snapshot.Position, action.Route)
	case aigame.ActionMapEvent:
		if action.Event != aigame.MapEventWarp || action.Direction != -1 {
			return errors.New("leveling integration received invalid warp EV")
		}
		source := aiknowledge.Point{Floor: int(g.snapshot.Position.Floor), X: int(g.snapshot.Position.X), Y: int(g.snapshot.Position.Y)}
		destination, ok := g.warps[source]
		if !ok || action.X != int32(source.X) || action.Y != int32(source.Y) {
			return errors.New("leveling integration EV was not sent at warp source")
		}
		g.snapshot.Position = aigame.Point{Floor: int32(destination.Floor), X: int32(destination.X), Y: int32(destination.Y)}
	default:
		return errors.New("leveling integration expected Move or EV")
	}
	g.snapshot.Revision++
	return nil
}

func applyLevelingIntegrationRoute(position *aigame.Point, route string) {
	for _, direction := range route {
		switch direction {
		case 'a':
			position.Y--
		case 'b':
			position.X++
			position.Y--
		case 'c':
			position.X++
		case 'd':
			position.X++
			position.Y++
		case 'e':
			position.Y++
		case 'f':
			position.X--
			position.Y++
		case 'g':
			position.X--
		case 'h':
			position.X--
			position.Y--
		}
	}
}

type levelingIntegrationStore struct {
	mu    sync.Mutex
	items map[string]automation.Checkpoint
}

func (s *levelingIntegrationStore) Create(_ context.Context, checkpoint automation.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[checkpoint.Plan.ID]; exists {
		return automation.ErrConflict
	}
	s.items[checkpoint.Plan.ID] = checkpoint
	return nil
}

func (s *levelingIntegrationStore) Load(_ context.Context, id string) (automation.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, exists := s.items[id]
	if !exists {
		return automation.Checkpoint{}, automation.ErrNotFound
	}
	return checkpoint, nil
}

func (s *levelingIntegrationStore) Save(_ context.Context, checkpoint automation.Checkpoint, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.items[checkpoint.Plan.ID]
	if !exists || current.Revision != expected || checkpoint.Revision != expected+1 {
		return automation.ErrConflict
	}
	s.items[checkpoint.Plan.ID] = checkpoint
	return nil
}

func TestLevelingNavigationCoordinatorConfirmsWarpAndContinues(t *testing.T) {
	game := &levelingIntegrationGame{
		snapshot: aigame.Snapshot{
			Revision: 1, Connected: true, Phase: aigame.PhaseWorld, Character: "Hero",
			Position: aigame.Point{Floor: 1006, X: 15, Y: 22},
			Player:   aigame.PlayerSnapshot{HasStatus: true, Level: 1, HP: 100, MaxHP: 100, RidePet: -1, RidePetKnown: true},
		},
		warps: map[aiknowledge.Point]aiknowledge.Point{
			{Floor: 1006, X: 10, Y: 20}:  {Floor: 1000, X: 98, Y: 44},
			{Floor: 1000, X: 49, Y: 116}: {Floor: 100, X: 637, Y: 491},
		},
	}
	knowledge := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{{
			ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140},
			Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 11, Y: 567, X2: 155, Y2: 707},
		}},
		Warps: []aiknowledge.MapWarp{
			{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1006, X: 10, Y: 20}, To: aiknowledge.Point{Floor: 1000, X: 98, Y: 44}, Attribute: "NULL"},
			{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1000, X: 49, Y: 116}, To: aiknowledge.Point{Floor: 100, X: 637, Y: 491}, Attribute: "NULL"},
		},
	}
	navigator := &LevelingNavigator{Knowledge: knowledge, Tiles: crossMapNavigator{}}
	gate := aicontrol.New()
	t.Cleanup(gate.Close)
	if _, _, err := gate.Switch(1, aicontrol.Leveling, "integration"); err != nil {
		t.Fatal(err)
	}
	coordinator := &aileveling.Coordinator{
		Game: game, Navigator: navigator,
		Store: &levelingIntegrationStore{items: make(map[string]automation.Checkpoint)},
		Gate:  gate, CharacterID: "char-1", CharacterName: "Hero",
		NoProgressTimeout: time.Minute,
	}
	receipt, err := coordinator.Start(context.Background(), aileveling.StartRequest{
		TargetKind: "character", TargetID: "char-1", TargetLevel: 2,
		AreaID: 28, MaximumSeconds: 30, NoProgressTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("initial navigation checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 1 {
		t.Fatalf("initial actions=%+v", game.actions)
	}
	// Simulate a delayed S:c packet that reports an intermediate tile while an
	// unrelated pet level update arrives. Neither change confirms the Move.
	game.snapshot.Position = aigame.Point{Floor: 1006, X: 12, Y: 22}
	game.snapshot.Pets = []aigame.PetSnapshot{{StableID: "pet-1", IdentityKnown: true, Level: 2}}
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("intermediate update checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 1 {
		t.Fatalf("intermediate update repeated Move actions=%+v", game.actions)
	}
	if game.positionQueries != 1 {
		t.Fatalf("missing native position query: %d", game.positionQueries)
	}
	// The first segment's exact current-floor endpoint is now authoritative;
	// the next Tick may submit the final source-entering segment.
	game.snapshot.Position = aigame.Point{Floor: 1006, X: 11, Y: 22}
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("warp source segment checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 2 {
		t.Fatalf("warp source segment actions=%+v", game.actions)
	}
	if got := game.snapshot.Position; got.Floor != 1006 || got.X != 10 || got.Y != 20 {
		t.Fatalf("server did not leave player at warp source, position=%+v", got)
	}
	game.mu.Unlock()

	// W only reaches the source. The next Tick submits the native EV, which is
	// the operation that causes the server-side floor transition.
	checkpoint, err = coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("warp event checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 3 || game.actions[2].Kind != aigame.ActionMapEvent || game.actions[2].Event != aigame.MapEventWarp {
		t.Fatalf("warp event actions=%+v", game.actions)
	}
	if got := game.snapshot.Position; got.Floor != 1000 || got.X != 98 || got.Y != 44 {
		t.Fatalf("server did not apply explicit first warp, position=%+v", got)
	}
	game.mu.Unlock()

	// The destination snapshot confirms the EV. The following Tick resumes
	// navigation on floor 1000 and submits a short ground segment toward the
	// next verified source.
	checkpoint, err = coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("post-warp navigation checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 4 {
		t.Fatalf("post-warp navigation repeated or omitted Move actions=%+v", game.actions)
	}
	if got := game.actions[3]; got.Kind != aigame.ActionMove || got.X != 98 || got.Y != 44 || got.Route == "" || len(got.Route) > 4 {
		t.Fatalf("next floor route=%+v", got)
	}
}
