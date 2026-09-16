package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

type healingWalkingSession struct {
	*itemHealingSession
	moves   int
	pending bool
}

func (s *healingWalkingSession) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if err := s.itemHealingSession.ExecuteExpected(ctx, rev, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Kind == aigame.ActionMove {
		s.moves++
		s.pending = true
	}
	if a.Kind == aigame.ActionStatus && a.Command == "c" && s.pending {
		s.snapshot.Position.X = 1
		s.snapshot.Revision++
		s.pending = false
	}
	return nil
}

func TestTravelUsesConfirmedItemBeforeResumingRiskyMovement(t *testing.T) {
	for _, mode := range []string{"low", "multiple-items", "after-battle", "ready", "safe-town", "missing", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			heal, session, _ := itemHealingFixture(t)
			session.snapshot.Position.Floor = 10
			if mode == "multiple-items" {
				session.snapshot.Player.MaxHP = 101
				session.bag = append(session.bag, aigame.AIInventoryItem{Slot: 9, TemplateID: 77})
			}
			if mode == "ready" {
				session.snapshot.Player.HP = 15
			}
			if mode == "missing" {
				session.bag = nil
			}
			if mode == "uncertain" {
				session.uncertain = true
			}
			walking := &healingWalkingSession{itemHealingSession: session}
			heal.Backend.Session = walking
			if mode != "safe-town" {
				heal.Backend.Knowledge.Encounters = []aiknowledge.EncounterArea{{Floor: 10, Bounds: aiknowledge.Rectangle{X: 0, X2: 1}, EncounterProbability: aiknowledge.Range{Max: 1}}}
			}
			move := &MovementSkill{Backend: heal.Backend, Navigator: oneStepNavigator{}, HealthRecovery: &TravelItemRecovery{Healing: heal}}
			battles := 0
			if mode == "after-battle" {
				session.snapshot.Phase = aigame.PhaseBattle
				session.snapshot.Battle.Active = true
				move.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error {
					battles++
					session.snapshot.Phase = aigame.PhaseWorld
					session.snapshot.Battle.Active = false
					session.snapshot.Revision++
					return nil
				})
			}
			err := move.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
			switch mode {
			case "missing":
				if !errors.Is(err, ErrHealingItemUnavailable) || session.uses != 0 || walking.moves != 0 {
					t.Fatalf("err=%v uses=%d moves=%d", err, session.uses, walking.moves)
				}
			case "uncertain":
				if err == nil || session.uses != 1 || walking.moves != 0 {
					t.Fatalf("err=%v uses=%d moves=%d", err, session.uses, walking.moves)
				}
			default:
				want := 1
				if mode == "multiple-items" {
					want = 3
				}
				if mode == "ready" || mode == "safe-town" {
					want = 0
				}
				if err != nil || session.uses != want || walking.moves != 1 {
					t.Fatalf("err=%v uses=%d moves=%d", err, session.uses, walking.moves)
				}
				if mode == "after-battle" && battles != 1 {
					t.Fatalf("battles=%d", battles)
				}
			}
		})
	}
}

type movementHealthRecoveryFunc func(context.Context) error

func (f movementHealthRecoveryFunc) Heal(ctx context.Context) error { return f(ctx) }

func TestTravelItemRecoveryBoundDoesNotInventHealth(t *testing.T) {
	move, game := movementFixture(t, true)
	game.snapshot.Player.HP = 1
	game.snapshot.Player.MaxHP = 29
	move.Backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{Floor: 10, Bounds: aiknowledge.Rectangle{X: 0, X2: 1}, EncounterProbability: aiknowledge.Range{Max: 1}}}}
	attempts := 0
	move.HealthRecovery = movementHealthRecoveryFunc(func(context.Context) error { attempts++; return nil })
	err := move.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
	if !errors.Is(err, ErrTravelHealingLimit) || attempts != 15 || game.moves != 0 {
		t.Fatalf("err=%v attempts=%d moves=%d", err, attempts, game.moves)
	}
}
