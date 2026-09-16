package aiservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type TileNavigator interface {
	RouteContext(context.Context, int, ainavigation.Point, ainavigation.Point) (ainavigation.Route, error)
}

// MovementSkill executes short native W segments, obtaining fresh S:c
// position samples after each segment. Stock W does not echo its owner's
// final position. No local prediction is used as completion evidence.
type MovementSkill struct {
	Backend                 *GameBackend
	Navigator               TileNavigator
	SegmentTimeout          time.Duration
	WarpGraph               *aiplanner.WarpGraph
	WarpTimeSection         aiplanner.TimeSection
	WarpConfirmationTimeout time.Duration
	MaxWarpEdges            int
	BattleRecovery          MovementBattleRecovery
	HealthRecovery          MovementHealthRecovery
	// SafeTravel is set by gameplay wiring for human and AI task movement.
	// It cannot be overridden by action arguments.
	SafeTravel bool
}
type movementArguments struct {
	Floor int    `json:"floor"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	NPC   string `json:"npc,omitempty"`
}

func (s *MovementSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	if s.Backend == nil || s.Navigator == nil {
		return aimcp.ErrBackend
	}
	if a.Skill != "move" || a.MaximumCost != 0 {
		return errors.New("movement requires a known zero cost")
	}
	var args movementArguments
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return err
	}
	if args.Floor < 0 || args.X < 0 || args.Y < 0 {
		return aimcp.ErrInvalidParams
	}
	return ctx.Err()
}
func (s *MovementSkill) Execute(ctx context.Context, a automation.Action) error {
	battles, healingAttempts := 0, 0
	resuming := false
	for {
		err := s.executeOnce(ctx, a, resuming)
		var petHealing *travelPetHealingRequired
		switch {
		case errors.As(err, &petHealing):
			if healingAttempts >= 15 {
				return ErrTravelHealingLimit
			}
			healingAttempts++
			if err := s.recoverTravelPet(ctx, petHealing); err != nil {
				return err
			}
		case errors.Is(err, ErrTravelHealingRequired) && s.HealthRecovery != nil:
			if healingAttempts >= 15 {
				return ErrTravelHealingLimit
			}
			healingAttempts++
			if err := s.HealthRecovery.Heal(ctx); err != nil {
				return fmt.Errorf("travel item recovery: %w", err)
			}
		case errors.Is(err, ErrMovementBattle) && s.BattleRecovery != nil:
			if battles >= 8 {
				return ErrTravelBattleLimit
			}
			battles++
			if err := s.BattleRecovery.Escape(ctx); err != nil {
				return err
			}
		default:
			return err
		}
		// Only explicit battle or health interruptions may resume after
		// recovery. Uncertain writes and position timeouts never enter here.
		resuming = true
	}
}

func (s *MovementSkill) executeOnce(ctx context.Context, a automation.Action, resuming bool) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	var args movementArguments
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return err
	}
	o, err := s.Backend.Observe(ctx, s.Backend.Binding)
	if err != nil {
		return err
	}
	// The caller's initial revision remains strict. Only after a confirmed
	// recovery may we plan from a fresh observation. Sampling twice across
	// that boundary spuriously rejected ordinary post-battle status packets.
	if !resuming && o.Revision != a.ExpectedRevision {
		return fmt.Errorf("movement initial observation: %w", aigame.ErrStaleRevision)
	}
	if err := movementObservationReady(o); err != nil {
		return err
	}
	if s.SafeTravel {
		guarded := *s
		navigator, err := s.travelNavigator(o.Character.Level)
		if err != nil {
			return err
		}
		guarded.Navigator = navigator
		s = &guarded
	}
	if o.Floor != args.Floor {
		return s.executeCrossMap(ctx, o, args)
	}
	route, err := s.Navigator.RouteContext(ctx, o.Floor, ainavigation.Point{X: o.X, Y: o.Y}, ainavigation.Point{X: args.X, Y: args.Y})
	if err != nil {
		return err
	}
	if len(route.Directions) != len(route.Points) {
		return errors.New("invalid tile route")
	}
	timeout := s.SegmentTimeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 5 * time.Second
	}
	for offset := 0; offset < len(route.Directions); {
		if err := movementObservationReady(o); err != nil {
			return err
		}
		end := s.segmentEnd(o, route, offset)
		target := route.Points[end-1]
		o, err = s.submitMove(ctx, o, aigame.Move(int32(o.X), int32(o.Y), route.Directions[offset:end]))
		if err != nil {
			return err
		}
		segment, cancel := context.WithTimeout(ctx, timeout)
		o, err = s.waitPosition(segment, args.Floor, target)
		cancel()
		if err != nil {
			return err
		}
		offset = end
	}
	return nil
}

// Native EN handling clears the queued W directions before attempting an
// encounter. Even when no battle can start, the remaining directions are
// gone. Use one-tile requests around encounter cells so successful partial
// execution cannot strand the position-confirmation loop. This does not
// retry an uncertain write or declare an unverified encounter safe.
func (s *MovementSkill) segmentEnd(o aimcp.Observation, route ainavigation.Route, offset int) int {
	end := min(offset+4, len(route.Directions))
	if s.Backend == nil || s.Backend.Knowledge == nil {
		return end
	}
	for _, area := range s.Backend.Knowledge.Encounters {
		if area.Floor != o.Floor || (area.EncounterProbability.Min <= 0 && area.EncounterProbability.Max <= 0) {
			continue
		}
		if area.Bounds.Contains(o.X, o.Y) {
			return offset + 1
		}
		for _, point := range route.Points[offset:end] {
			if area.Bounds.Contains(point.X, point.Y) {
				return offset + 1
			}
		}
	}
	return end
}

// submitMove handles the one revision race that is safe to recover from. A
// Session's ErrStaleRevision is returned by its revision fence before the
// packet write, so no W was sent. Re-observe the same authoritative position
// and retry that segment once under the new revision. Any other write error
// remains ambiguous and is returned without a retry; a changed position also
// invalidates the already-planned segment.
func (s *MovementSkill) submitMove(ctx context.Context, observed aimcp.Observation, action aigame.Action, checks ...func(aimcp.Observation) error) (aimcp.Observation, error) {
	if action.Kind != aigame.ActionMove {
		return observed, errors.New("movement retry requires a move action")
	}
	if s.SafeTravel {
		checks = append(checks, func(current aimcp.Observation) error { return s.checkTravelEncounters(current, action) })
	}
	if err := s.checkTravelHealth(observed, action); err != nil {
		return observed, err
	}
	for _, check := range checks {
		if err := check(observed); err != nil {
			return observed, err
		}
	}
	if err := s.submit(ctx, observed.Revision, action); err == nil {
		return observed, nil
	} else if !errors.Is(err, aigame.ErrStaleRevision) {
		return observed, err
	}

	refreshed, err := s.Backend.Observe(ctx, s.Backend.Binding)
	if err != nil {
		return refreshed, err
	}
	if err := movementObservationReady(refreshed); err != nil {
		return refreshed, err
	}
	if err := s.checkTravelHealth(refreshed, action); err != nil {
		return refreshed, err
	}
	for _, check := range checks {
		if err := check(refreshed); err != nil {
			return refreshed, err
		}
	}
	if observationPoint(refreshed) != observationPoint(observed) {
		return refreshed, aigame.ErrStaleRevision
	}
	if err := s.submit(ctx, refreshed.Revision, action); err != nil {
		return refreshed, err
	}
	return refreshed, nil
}

func (s *MovementSkill) submit(ctx context.Context, revision uint64, a aigame.Action) error {
	b := s.Backend
	return dispatchGameAction(ctx, b, func(writeCtx context.Context) error {
		bounded, cancel := context.WithTimeout(writeCtx, time.Second)
		defer cancel()
		return b.Session.ExecuteExpected(bounded, revision, a)
	})
}
func (s *MovementSkill) waitPosition(ctx context.Context, floor int, target ainavigation.Point) (aimcp.Observation, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last aimcp.Observation
	for {
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("movement confirmation target=%d:(%d,%d), last=%d:(%d,%d), revision=%d: %w", floor, target.X, target.Y, last.Floor, last.X, last.Y, last.Revision, ctx.Err())
		case <-ticker.C:
		}
		o, err := s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return o, err
		}
		last = o
		if err := movementObservationReady(o); err != nil {
			return o, err
		}
		if o.Floor != floor {
			return o, errors.New("movement interrupted before position confirmation")
		}
		if o.X == target.X && o.Y == target.Y {
			return o, nil
		}
		// This request is read-only and may be repeated. A rejected stale
		// observation is retried after observing again; W is never resent.
		err = s.submit(ctx, o.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"})
		if err != nil && !errors.Is(err, aigame.ErrStaleRevision) {
			return o, err
		}
	}
}

var _ DeterministicSkill = (*MovementSkill)(nil)
