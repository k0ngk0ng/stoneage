package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type crossMapNavigator struct{}

func (crossMapNavigator) RouteContext(ctx context.Context, floor int, from, to ainavigation.Point) (ainavigation.Route, error) {
	if err := ctx.Err(); err != nil {
		return ainavigation.Route{}, err
	}
	if from == to {
		return ainavigation.Route{Floor: floor, From: from, To: to}, nil
	}
	directions := make([]byte, 0, abs(from.X-to.X)+abs(from.Y-to.Y))
	points := make([]ainavigation.Point, 0, cap(directions))
	current := from
	for current.X != to.X {
		if current.X < to.X {
			current.X++
			directions = append(directions, 'c')
		} else {
			current.X--
			directions = append(directions, 'g')
		}
		points = append(points, current)
	}
	for current.Y != to.Y {
		if current.Y < to.Y {
			current.Y++
			directions = append(directions, 'e')
		} else {
			current.Y--
			directions = append(directions, 'a')
		}
		points = append(points, current)
	}
	return ainavigation.Route{Floor: floor, From: from, To: to, Directions: string(directions), Points: points}, nil
}

func (crossMapNavigator) Walkable(int, int, int) bool { return true }

type trackingCrossMapNavigator struct {
	crossMapNavigator
	walkable     bool
	routeCalls   int
	walkableCall int
}

func (n *trackingCrossMapNavigator) RouteContext(ctx context.Context, floor int, from, to ainavigation.Point) (ainavigation.Route, error) {
	n.routeCalls++
	return n.crossMapNavigator.RouteContext(ctx, floor, from, to)
}

func (n *trackingCrossMapNavigator) Walkable(int, int, int) bool {
	n.walkableCall++
	return n.walkable
}

type crossMapGame struct {
	mu           sync.Mutex
	snapshot     aigame.Snapshot
	warpTargets  map[aiknowledge.Point]aiknowledge.Point
	warpOnMove   bool
	warpOnStatus bool
	wrongFloor   int
	moves        []aigame.Action
	mapEvents    []aigame.Action
	mapEventAcks map[int32]aigame.Event
	ackWake      chan struct{}
	statuses     int
}

func (g *crossMapGame) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot, nil
}

func (g *crossMapGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapshot.Revision != revision {
		return aigame.ErrStaleRevision
	}
	switch action.Kind {
	case aigame.ActionMove:
		g.moves = append(g.moves, action)
		applyCrossMapRoute(&g.snapshot.Position, action.Route)
		if g.wrongFloor != 0 {
			g.snapshot.Position = aigame.Point{Floor: int32(g.wrongFloor), X: 99, Y: 99}
		} else if g.warpOnMove {
			if destination, ok := g.warpTargets[knowledgePoint(g.snapshot.Position)]; ok {
				g.snapshot.Position = aigame.Point{Floor: int32(destination.Floor), X: int32(destination.X), Y: int32(destination.Y)}
			}
		}
	case aigame.ActionStatus:
		g.statuses++
		if g.warpOnStatus {
			if destination, ok := g.warpTargets[knowledgePoint(g.snapshot.Position)]; ok {
				g.snapshot.Position = aigame.Point{Floor: int32(destination.Floor), X: int32(destination.X), Y: int32(destination.Y)}
			}
		}
	case aigame.ActionMapEvent:
		g.mapEvents = append(g.mapEvents, action)
		if g.mapEventAcks == nil {
			g.mapEventAcks = make(map[int32]aigame.Event)
		}
		g.mapEventAcks[action.EventSequence] = aigame.Event{Function: "EV", Fields: []aigame.Field{
			{Kind: aigame.FieldInt, Int: action.EventSequence},
			{Kind: aigame.FieldInt, Int: 1},
		}}
	default:
		return errors.New("unexpected action in cross-map test")
	}
	g.snapshot.Revision++
	if action.Kind == aigame.ActionMapEvent && g.ackWake != nil {
		select {
		case g.ackWake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (g *crossMapGame) WaitForMapEvent(ctx context.Context, sequence int32) (aigame.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if g.ackWake == nil {
		g.ackWake = make(chan struct{}, 1)
	}
	for {
		g.mu.Lock()
		if event, ok := g.mapEventAcks[sequence]; ok {
			delete(g.mapEventAcks, sequence)
			g.mu.Unlock()
			return event, nil
		}
		wake := g.ackWake
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return aigame.Event{}, ctx.Err()
		case <-wake:
		}
	}
}

func applyCrossMapRoute(position *aigame.Point, route string) {
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

func knowledgePoint(position aigame.Point) aiknowledge.Point {
	return aiknowledge.Point{Floor: int(position.Floor), X: int(position.X), Y: int(position.Y)}
}

func newCrossMapSkill(t *testing.T, game *crossMapGame, warps []aiknowledge.MapWarp) *MovementSkill {
	t.Helper()
	backend, _ := gameFixture(t)
	backend.Session = game
	return &MovementSkill{
		Backend:                 backend,
		Navigator:               crossMapNavigator{},
		WarpGraph:               aiplanner.NewWarpGraphFromWarps(warps),
		SegmentTimeout:          700 * time.Millisecond,
		WarpConfirmationTimeout: 700 * time.Millisecond,
	}
}

func crossMapAction(revision uint64, floor, x, y int) automation.Action {
	return automation.Action{Skill: "move", ExpectedRevision: revision, Arguments: mustJSON(movementArguments{Floor: floor, X: x, Y: y})}
}

func mustJSON(value any) []byte {
	// The movement tests only use literals, so keep this helper local to avoid
	// repeating an error path that cannot occur for movementArguments.
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestMovementCrossMapConfirmsMultipleWarpsAndContinuesGroundMovement(t *testing.T) {
	firstFrom := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	firstTo := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	secondFrom := aiknowledge.Point{Floor: 2, X: 1, Y: 0}
	secondTo := aiknowledge.Point{Floor: 3, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot:     aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}},
		warpTargets:  map[aiknowledge.Point]aiknowledge.Point{firstFrom: firstTo, secondFrom: secondTo},
		warpOnStatus: true,
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: firstFrom, To: firstTo, Attribute: "NULL"}, {Type: "NONE", Time: "NULL", From: secondFrom, To: secondTo, Attribute: "NULL"}})
	if err := skill.Execute(context.Background(), crossMapAction(12, 3, 2, 0)); err != nil {
		t.Fatal(err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.moves) != 3 || len(game.mapEvents) != 2 || game.statuses != 2 {
		t.Fatalf("moves=%d map_events=%d statuses=%d, want two warp-source moves, two EVs, final ground move and two refreshes", len(game.moves), len(game.mapEvents), game.statuses)
	}
	if got := game.snapshot.Position; got.Floor != 3 || got.X != 2 || got.Y != 0 {
		t.Fatalf("final position=%+v", got)
	}
}

func TestMovementCrossMapCancellationDoesNotRepeatMove(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot:    aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}},
		warpTargets: map[aiknowledge.Point]aiknowledge.Point{from: to},
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	skill.WarpConfirmationTimeout = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- skill.Execute(ctx, crossMapAction(12, 2, 0, 0)) }()
	time.Sleep(350 * time.Millisecond)
	cancel()
	err := <-done
	game.mu.Lock()
	defer game.mu.Unlock()
	if !errors.Is(err, context.Canceled) || len(game.moves) != 1 {
		t.Fatalf("err=%v moves=%d, want cancellation after one Move", err, len(game.moves))
	}
}

func TestMovementCrossMapWrongDestinationIsConfirmationError(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot:   aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}},
		wrongFloor: 99,
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	err := skill.Execute(context.Background(), crossMapAction(12, 2, 0, 0))
	game.mu.Lock()
	defer game.mu.Unlock()
	if !errors.Is(err, ErrCrossMapConfirmation) || len(game.moves) != 1 {
		t.Fatalf("err=%v moves=%d, want confirmation error after one Move", err, len(game.moves))
	}
}

func TestMovementCrossMapTimeoutDoesNotRepeatMove(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot: aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}},
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	skill.WarpConfirmationTimeout = 80 * time.Millisecond
	err := skill.Execute(context.Background(), crossMapAction(12, 2, 0, 0))
	game.mu.Lock()
	defer game.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || len(game.moves) != 1 {
		t.Fatalf("err=%v moves=%d, want timeout after one Move", err, len(game.moves))
	}
}

func TestMovementCrossMapRejectsPaidAndUnsupportedWarps(t *testing.T) {
	for _, edge := range []aiknowledge.MapWarp{{Type: "FREE", Time: "NULL", Attribute: "NULL"}, {Type: "NONE", Time: "NULL", Attribute: "500"}, {Type: "ERROR", Time: "NULL", Attribute: "NULL"}} {
		t.Run(edge.Type+edge.Attribute, func(t *testing.T) {
			from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
			to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
			edge.From, edge.To = from, to
			game := &crossMapGame{snapshot: aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}}}
			skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{edge})
			err := skill.Execute(context.Background(), crossMapAction(12, 2, 0, 0))
			game.mu.Lock()
			defer game.mu.Unlock()
			if !errors.Is(err, ErrCrossMapUnavailable) || len(game.moves) != 0 {
				t.Fatalf("err=%v moves=%d, want unavailable without writes", err, len(game.moves))
			}
		})
	}
}

func TestMovementCrossMapRejectsBlockedDestinationBeforeGroundSearch(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{snapshot: aigame.Snapshot{Account: "account", Character: "character", Revision: 12, Connected: true, Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10}}}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	navigator := &trackingCrossMapNavigator{walkable: false}
	skill.Navigator = navigator

	_, err := skill.findCrossMapRoute(context.Background(), skill.WarpGraph, from, to)
	if !errors.Is(err, ErrCrossMapUnavailable) {
		t.Fatalf("blocked destination error = %v, want ErrCrossMapUnavailable", err)
	}
	if navigator.walkableCall != 1 {
		t.Fatalf("Walkable calls = %d, want one static destination check", navigator.walkableCall)
	}
	if navigator.routeCalls != 0 {
		t.Fatalf("RouteContext calls = %d, want no ground search for blocked destination", navigator.routeCalls)
	}
}
