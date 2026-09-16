package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func recoveryFixture(t *testing.T) (*HealerRecoverySkill, *healerSession) {
	t.Helper()
	healer, game, _ := healerFixture(t)
	var floors []ainavigation.FloorMap
	for _, id := range []int{10, 11} {
		floors = append(floors, ainavigation.FloorMap{ID: id, Width: 12, Height: 12, Tiles: make([]uint16, 144), Objects: make([]uint16, 144)})
	}
	tiles, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{0: {Walkable: 2}}}, floors)
	if err != nil {
		t.Fatal(err)
	}
	healer.Backend.Knowledge = &aiknowledge.Knowledge{}
	return &HealerRecoverySkill{Healer: healer, Tiles: tiles}, game
}

func recoveryObservation() aimcp.Observation {
	return aimcp.Observation{Connected: true, Ready: true, Floor: 10, X: 0, Y: 6, Gold: 50, Character: aimcp.Entity{HP: 1, MaxHP: 29, Level: 3}}
}

func TestRecoveryPlansAndWalksWithSameEncounterConstraint(t *testing.T) {
	s, _ := recoveryFixture(t)
	s.Healer.Backend.Knowledge.Encounters = []aiknowledge.EncounterArea{{Floor: 10, Bounds: aiknowledge.Rectangle{X: 2, Y: 5, X2: 2, Y2: 7}, EncounterProbability: aiknowledge.Range{Max: 1}}}
	m, err := s.movement()
	if err != nil {
		t.Fatal(err)
	}
	o := recoveryObservation()
	to, err := s.selectDestination(context.Background(), o, 1, m)
	if err != nil {
		t.Fatal(err)
	}
	route, err := m.Navigator.RouteContext(context.Background(), 10, ainavigation.Point{X: o.X, Y: o.Y}, ainavigation.Point{X: to.Position.X, Y: to.Position.Y})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range route.Points {
		if m.encounterTile(10, p.X, p.Y) || p == (ainavigation.Point{X: 5, Y: 6}) {
			t.Fatalf("unsafe recovery step %+v", p)
		}
	}
	if len(route.Points) != to.TravelSteps {
		t.Fatal("selection/execution route mismatch")
	}
}

func TestRecoveryRejectsEncounterBarrierAndUnsafeStart(t *testing.T) {
	for _, startRisk := range []bool{false, true} {
		s, _ := recoveryFixture(t)
		x := 2
		if startRisk {
			x = 0
		}
		s.Healer.Backend.Knowledge.Encounters = []aiknowledge.EncounterArea{{Floor: 10, Bounds: aiknowledge.Rectangle{X: x, Y: 0, X2: x, Y2: 11}, EncounterProbability: aiknowledge.Range{Max: 1}}}
		m, _ := s.movement()
		if _, err := s.selectDestination(context.Background(), recoveryObservation(), 1, m); !errors.Is(err, ErrNoReachableHealer) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestRecoveryRespectsBudgetAndUnlimitedFunding(t *testing.T) {
	s, _ := recoveryFixture(t)
	m, _ := s.movement()
	o := recoveryObservation()
	o.Gold = 0
	if _, err := s.selectDestination(context.Background(), o, 1, m); !errors.Is(err, ErrNoReachableHealer) {
		t.Fatal(err)
	}
	o.UnlimitedFunds = true
	if _, err := s.selectDestination(context.Background(), o, 0, m); !errors.Is(err, ErrNoReachableHealer) {
		t.Fatal("unlimited funding bypassed task budget", err)
	}
	if _, err := s.selectDestination(context.Background(), o, 1, m); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCrossMapRejectsDangerousWarpLanding(t *testing.T) {
	for _, danger := range []bool{false, true} {
		s, _ := recoveryFixture(t)
		c := s.Healer.Contracts["trainer"]
		c.NPC.Floor = 11
		s.Healer.Contracts["trainer"] = c
		s.Healer.Backend.Knowledge.Warps = []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", Attribute: "NULL", From: aiknowledge.Point{Floor: 10, X: 1, Y: 1}, To: aiknowledge.Point{Floor: 11, X: 0, Y: 1}}}
		if danger {
			s.Healer.Backend.Knowledge.Encounters = []aiknowledge.EncounterArea{{Floor: 11, Bounds: aiknowledge.Rectangle{X: 0, Y: 1, X2: 0, Y2: 1}, EncounterProbability: aiknowledge.Range{Max: 1}}}
		}
		m, _ := s.movement()
		_, err := s.selectDestination(context.Background(), recoveryObservation(), 1, m)
		if danger && !errors.Is(err, ErrNoReachableHealer) || !danger && err != nil {
			t.Fatalf("danger=%v err=%v", danger, err)
		}
		if danger && m.Navigator.(staticWalkabilityNavigator).Walkable(10, 1, 1) {
			t.Fatal("dangerous automatic warp source remains walkable")
		}
	}
}

func TestRecoveryExecutesSelectedNurseHealing(t *testing.T) {
	s, game := recoveryFixture(t)
	err := s.Execute(context.Background(), automation.Action{Skill: "npc.recover", Arguments: json.RawMessage(`{}`), ExpectedRevision: 12, MaximumCost: 1})
	if err != nil || game.confirmations != 1 || game.snapshot.Player.HP != 29 {
		t.Fatalf("err=%v confirmations=%d HP=%d", err, game.confirmations, game.snapshot.Player.HP)
	}
}

type recoveryWalkingSession struct {
	*healerSession
	steps        []ainavigation.Point
	moveAttempts int
	failMove     bool
}

func (s *recoveryWalkingSession) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionMove {
		s.moveAttempts++
		if s.failMove {
			return errors.New("unknown recovery W outcome")
		}
	}
	if err := s.healerSession.ExecuteExpected(ctx, revision, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Kind == aigame.ActionLook {
		s.snapshot.Position.Direction = a.Direction
	}
	if a.Kind == aigame.ActionMove {
		deltas := [...][2]int32{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}
		for _, d := range []byte(a.Route) {
			s.snapshot.Position.X += deltas[d-'a'][0]
			s.snapshot.Position.Y += deltas[d-'a'][1]
			s.steps = append(s.steps, ainavigation.Point{X: int(s.snapshot.Position.X), Y: int(s.snapshot.Position.Y)})
		}
	}
	return nil
}

func TestRecoveryWalksDetourThenHealsAndNeverRetriesUnknownMove(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		s, game := recoveryFixture(t)
		game.snapshot.Position.X = 0
		walking := &recoveryWalkingSession{healerSession: game, failMove: uncertain}
		s.Healer.Backend.Session = walking
		s.Healer.Backend.Knowledge.Encounters = []aiknowledge.EncounterArea{{Floor: 10, Bounds: aiknowledge.Rectangle{X: 2, Y: 5, X2: 2, Y2: 7}, EncounterProbability: aiknowledge.Range{Max: 1}}}
		err := s.Execute(context.Background(), automation.Action{Skill: "npc.recover", Arguments: json.RawMessage(`{}`), ExpectedRevision: 12, MaximumCost: 1})
		if uncertain {
			if err == nil || walking.moveAttempts != 1 || game.confirmations != 0 {
				t.Fatalf("err=%v attempts=%d confirmations=%d", err, walking.moveAttempts, game.confirmations)
			}
			continue
		}
		if err != nil || game.confirmations != 1 || len(walking.steps) == 0 {
			t.Fatalf("err=%v confirmations=%d steps=%v", err, game.confirmations, walking.steps)
		}
		for _, p := range walking.steps {
			if p.X == 2 && p.Y >= 5 && p.Y <= 7 {
				t.Fatalf("walked encounter tile %+v", p)
			}
		}
	}
}

type recoveryTickNavigator struct {
	constrainedTileNavigator
	tick func()
}

func (n *recoveryTickNavigator) RouteContextWithOptions(ctx context.Context, floor int, from, to ainavigation.Point, options ainavigation.RouteOptions) (ainavigation.Route, error) {
	if n.tick != nil {
		tick := n.tick
		n.tick = nil
		tick()
	}
	return n.constrainedTileNavigator.RouteContextWithOptions(ctx, floor, from, to, options)
}

func TestRecoveryRefreshesReadOnlySelectionWithoutIgnoringChangedInputs(t *testing.T) {
	for _, change := range []string{"revision", "health", "position", "funds"} {
		s, game := recoveryFixture(t)
		s.Tiles = &recoveryTickNavigator{constrainedTileNavigator: s.Tiles.(constrainedTileNavigator), tick: func() {
			game.mu.Lock()
			defer game.mu.Unlock()
			game.snapshot.Revision++
			switch change {
			case "health":
				game.snapshot.Player.HP++
			case "position":
				game.snapshot.Position.X--
			case "funds":
				game.snapshot.Player.Gold = 0
			}
		}}
		err := s.Execute(context.Background(), automation.Action{Skill: "npc.recover", Arguments: json.RawMessage(`{}`), ExpectedRevision: 12, MaximumCost: 1})
		if change == "revision" {
			if err != nil || game.confirmations != 1 {
				t.Fatalf("revision-only update: err=%v confirmations=%d", err, game.confirmations)
			}
		} else if err == nil || len(game.actions) != 0 {
			t.Fatalf("%s: err=%v actions=%d", change, err, len(game.actions))
		}
	}
}

func TestRecoveryUsesReviewedRangeAcrossNurseCounter(t *testing.T) {
	for _, talkRange := range []int{1, 2} {
		s, _ := recoveryFixture(t)
		c := s.Healer.Contracts["trainer"]
		c.NPC.TalkRange = talkRange
		s.Healer.Contracts["trainer"] = c
		objects := make([]uint16, 144)
		for i := range objects {
			if i/12 < 8 {
				objects[i] = 1
			}
		}
		n, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{0: {Walkable: 2}, 1: {Walkable: 0}}}, []ainavigation.FloorMap{{ID: 10, Width: 12, Height: 12, Tiles: make([]uint16, 144), Objects: objects}})
		if err != nil {
			t.Fatal(err)
		}
		s.Tiles = n
		m, _ := s.movement()
		o := recoveryObservation()
		o.X = 5
		o.Y = 9
		to, err := s.selectDestination(context.Background(), o, 1, m)
		if talkRange == 1 {
			if !errors.Is(err, ErrNoReachableHealer) {
				t.Fatalf("range1 err=%v", err)
			}
		} else if err != nil || to.Position.Y != 8 {
			t.Fatalf("range2 destination=%+v err=%v", to, err)
		}
	}
}
