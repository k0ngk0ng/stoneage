package aiservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
)

// QuestBattleSkill resolves an already initiated solo PvE battle. It does not
// initiate arbitrary fights, spend items, or infer quest victory from RS/RD:
// the following task conditions must prove the flag, reward or passage.
type QuestBattleSkill struct{ Backend *GameBackend }
type questBattleArguments struct {
	MaxTurns int `json:"max_turns"`
}

func (s *QuestBattleSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	if s == nil || s.Backend == nil || s.Backend.Session == nil || s.Backend.Gate == nil {
		return aimcp.ErrBackend
	}
	if a.Skill != "battle.finish" || a.MaximumCost != 0 {
		return errors.New("quest battle requires battle.finish with zero monetary cost")
	}
	var args questBattleArguments
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return err
	}
	if args.MaxTurns < 1 || args.MaxTurns > 100 {
		return errors.New("quest battle max_turns must be 1..100")
	}
	return ctx.Err()
}

func (s *QuestBattleSkill) Execute(ctx context.Context, a automation.Action) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	var args questBattleArguments
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return err
	}
	first, err := s.Backend.Session.Observe(ctx)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || first.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	if !first.Battle.Active {
		return errors.New("quest battle has not started")
	}
	writer := &MovementSkill{Backend: s.Backend}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	type submission struct {
		turn int32
		pet  bool
	}
	sent := map[submission]bool{}
	turns := map[int32]bool{}
	ended, defeated := false, false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.Backend.Session.Observe(ctx)
		if err != nil {
			return err
		}
		if current.Account != s.Backend.Binding.AccountID || current.Character != s.Backend.Binding.CharacterName {
			return aimcp.ErrInvalidBinding
		}
		if !current.Connected || !current.Player.HasStatus {
			return errors.New("quest battle session unavailable")
		}
		b := current.Battle
		if ended && !b.Active && current.Phase == aigame.PhaseWorld {
			if defeated || current.Player.HP <= 0 {
				return ErrTravelDead
			}
			if b.Result == "escaped" {
				return errors.New("quest battle ended by escape")
			}
			return nil
		}
		// BATTLE_CreateVsEnemy sends type 6 for NPC/boss challenges and
		// type 5 for DP encounters, although both use PvE internally.
		// Keep the allowlist explicit: PvP, spectators and unknown types
		// must never acquire quest automation control.
		if (b.Type != 1 && b.Type != 5 && b.Type != 6) || b.LadderID != "" || len(current.Party) > 1 {
			return errors.New("quest battle requires solo PvE")
		}
		if !b.Active && !ended {
			return errors.New("quest battle disappeared before terminal acknowledgement")
		}
		if b.MyNoKnown {
			for _, p := range b.Participants {
				if p.Player && p.BattleID != b.MyNo {
					return errors.New("quest battle refuses another human participant")
				}
			}
		}
		defeated = defeated || b.MySideDefeated() || current.Player.HP <= 0
		// An empty recovery policy forbids inventory consumption and spells.
		decision, ready := battleauto.Decide(current, nil, battleauto.Policy{})
		if ready && !ended {
			key := submission{turn: b.Turn, pet: !b.PlayerCommandReady() && b.PetCommandReady()}
			terminal := decision.Action.Kind == aigame.ActionBattleEnd
			if terminal || !sent[key] {
				if !terminal && !turns[b.Turn] && len(turns) >= args.MaxTurns {
					return errors.New("quest battle turn limit reached")
				}
				if err := writer.submit(ctx, current.Revision, decision.Action); err != nil {
					if !errors.Is(err, aigame.ErrStaleRevision) && !errors.Is(err, aigame.ErrBattleNotReady) {
						return fmt.Errorf("quest battle submission: %w", err)
					}
				} else if terminal {
					ended = true
				} else {
					sent[key] = true
					turns[b.Turn] = true
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
