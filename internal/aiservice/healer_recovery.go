package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var ErrNoReachableHealer = errors.New("no reviewed healer reachable without encounters within the spending limit")

type constrainedTileNavigator interface {
	TileNavigator
	Walkable(int, int, int) bool
	RouteContextWithOptions(context.Context, int, ainavigation.Point, ainavigation.Point, ainavigation.RouteOptions) (ainavigation.Route, error)
}

// healerNavigator is used both to select and execute the recovery route.
// Replanning must not silently replace an encounter-free route with the
// ordinary shortest path through enemies. Known nurse tiles are occupied.
type healerNavigator struct {
	tiles     constrainedTileNavigator
	movement  *MovementSkill
	contracts map[string]HealerContract
}

func (n *healerNavigator) blocked(floor, x, y int) bool {
	if n.movement.encounterTile(floor, x, y) {
		return true
	}
	// Some compatible servers apply a warp on W without a separate EV.
	// Do not walk across the source of a known dangerous destination.
	for _, w := range n.movement.Backend.Knowledge.Warps {
		if w.From.Floor == floor && w.From.X == x && w.From.Y == y && n.movement.encounterTile(w.To.Floor, w.To.X, w.To.Y) {
			return true
		}
	}
	for _, c := range n.contracts {
		if c.NPC.Floor == floor && c.NPC.X == x && c.NPC.Y == y {
			return true
		}
	}
	return false
}
func (n *healerNavigator) Walkable(floor, x, y int) bool {
	return !n.blocked(floor, x, y) && n.tiles.Walkable(floor, x, y)
}
func (n *healerNavigator) RouteContext(ctx context.Context, floor int, from, to ainavigation.Point) (ainavigation.Route, error) {
	if !n.Walkable(floor, from.X, from.Y) || !n.Walkable(floor, to.X, to.Y) {
		return ainavigation.Route{}, ErrNoReachableHealer
	}
	return n.tiles.RouteContextWithOptions(ctx, floor, from, to, ainavigation.RouteOptions{Blocked: func(p ainavigation.Point) bool { return n.blocked(floor, p.X, p.Y) }})
}

type HealerDestination struct {
	Alias       string
	Position    aiknowledge.Point
	Quote       int64
	TravelSteps int
}

// HealerRecoverySkill chooses a reviewed nurse, walks an encounter-free route
// and invokes HP healing inside one durable automation action. It does not
// relax the low-health travel guard to escape wilderness. Such a character
// needs a separately verified portable remedy or evacuation route.
type HealerRecoverySkill struct {
	Healer *HealerSkill
	Tiles  TileNavigator
}

func (s *HealerRecoverySkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	if s == nil || s.Healer == nil || s.Healer.Backend == nil || s.Healer.Backend.Knowledge == nil {
		return aimcp.ErrBackend
	}
	if _, ok := s.Tiles.(constrainedTileNavigator); !ok {
		return errors.New("healer recovery requires constrained tile navigation")
	}
	if a.Skill != "npc.recover" || a.MaximumCost < 0 {
		return aimcp.ErrInvalidParams
	}
	var args struct{}
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return err
	}
	for alias, c := range s.Healer.Contracts {
		if alias != c.NPC.Alias {
			return ErrNPCUnknown
		}
		if _, err := c.registry(0); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *HealerRecoverySkill) movement() (*MovementSkill, error) {
	tiles, ok := s.Tiles.(constrainedTileNavigator)
	if !ok || s.Healer == nil || s.Healer.Backend == nil || s.Healer.Backend.Knowledge == nil {
		return nil, aimcp.ErrBackend
	}
	m := &MovementSkill{Backend: s.Healer.Backend}
	n := &healerNavigator{tiles: tiles, movement: m, contracts: s.Healer.Contracts}
	m.Navigator = n
	var warps []aiknowledge.MapWarp
	for _, w := range m.Backend.Knowledge.Warps {
		if n.Walkable(w.From.Floor, w.From.X, w.From.Y) && n.Walkable(w.To.Floor, w.To.X, w.To.Y) {
			warps = append(warps, w)
		}
	}
	m.WarpGraph = aiplanner.NewWarpGraphFromWarps(warps)
	return m, nil
}

func (s *HealerRecoverySkill) selectDestination(ctx context.Context, o aimcp.Observation, maximumCost int64, m *MovementSkill) (HealerDestination, error) {
	if err := movementObservationReady(o); err != nil {
		return HealerDestination{}, err
	}
	if !m.Navigator.(staticWalkabilityNavigator).Walkable(o.Floor, o.X, o.Y) {
		return HealerDestination{}, ErrNoReachableHealer
	}
	aliases := make([]string, 0, len(s.Healer.Contracts))
	for alias := range s.Healer.Contracts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var best HealerDestination
	found := false
	for _, alias := range aliases {
		if err := ctx.Err(); err != nil {
			return best, err
		}
		c := s.Healer.Contracts[alias]
		quote, err := c.hpQuote(aigame.PlayerSnapshot{HasStatus: true, Level: int32(o.Character.Level), HP: int32(o.Character.HP), MaxHP: int32(o.Character.MaxHP)})
		if err != nil {
			return best, err
		}
		if quote > maximumCost || (!o.UnlimitedFunds && quote > o.Gold) {
			continue
		}
		rangeLimit := c.NPC.TalkRange
		if rangeLimit == 0 {
			rangeLimit = 1
		}
		for dy := -rangeLimit; dy <= rangeLimit; dy++ {
			for dx := -rangeLimit; dx <= rangeLimit; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				to := aiknowledge.Point{Floor: c.NPC.Floor, X: c.NPC.X + dx, Y: c.NPC.Y + dy}
				if !m.Navigator.(staticWalkabilityNavigator).Walkable(to.Floor, to.X, to.Y) {
					continue
				}
				from := observationPoint(o)
				var edges []aiplanner.WarpEdge
				if from.Floor != to.Floor {
					edges, err = m.findCrossMapRoute(ctx, m.WarpGraph, from, to)
					if err != nil {
						if ctx.Err() != nil {
							return best, ctx.Err()
						}
						continue
					}
				}
				steps := len(edges)
				valid := true
				for _, edge := range edges {
					route, err := m.routeBetween(ctx, from, edge.From)
					if err != nil {
						valid = false
						break
					}
					steps += len(route.Points)
					from = edge.To
				}
				if !valid {
					continue
				}
				route, err := m.routeBetween(ctx, from, to)
				if err != nil {
					if ctx.Err() != nil {
						return best, ctx.Err()
					}
					continue
				}
				steps += len(route.Points)
				if !found || steps < best.TravelSteps || (steps == best.TravelSteps && quote < best.Quote) {
					best, found = HealerDestination{Alias: alias, Position: to, Quote: quote, TravelSteps: steps}, true
				}
			}
		}
	}
	if !found {
		return best, ErrNoReachableHealer
	}
	return best, nil
}

func (s *HealerRecoverySkill) Execute(ctx context.Context, a automation.Action) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	b := s.Healer.Backend
	o, err := b.Observe(ctx, b.Binding)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || o.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	if err := movementObservationReady(o); err != nil {
		return err
	}
	if o.Character.HP > 0 && o.Character.HP == o.Character.MaxHP {
		return nil
	}
	m, err := s.movement()
	if err != nil {
		return err
	}
	to, err := s.selectDestination(ctx, o, a.MaximumCost, m)
	if err != nil {
		return err
	}
	// Selection is read-only and can take longer than a server status tick.
	// Refresh its revision before the first write, but keep the position and
	// health inputs fixed. This is not a retry of a movement packet.
	fresh, err := b.Observe(ctx, b.Binding)
	if err != nil {
		return err
	}
	if err := movementObservationReady(fresh); err != nil {
		return err
	}
	if observationPoint(fresh) != observationPoint(o) || fresh.Character.Level != o.Character.Level || fresh.Character.HP != o.Character.HP || fresh.Character.MaxHP != o.Character.MaxHP {
		return aigame.ErrStaleRevision
	}
	if !fresh.UnlimitedFunds && fresh.Gold < to.Quote {
		return ErrNoReachableHealer
	}
	arguments, _ := json.Marshal(movementArguments{Floor: to.Position.Floor, X: to.Position.X, Y: to.Position.Y})
	if err := m.Execute(ctx, automation.Action{Skill: "move", ExpectedRevision: fresh.Revision, Arguments: arguments}); err != nil {
		return err
	}
	after, err := b.Observe(ctx, b.Binding)
	if err != nil {
		return err
	}
	healArgs, _ := json.Marshal(map[string]string{"npc": to.Alias})
	return s.Healer.Execute(ctx, automation.Action{Skill: "npc.heal", Arguments: healArgs, ExpectedRevision: after.Revision, MaximumCost: a.MaximumCost})
}
