package aiservice

import (
	"context"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

var (
	ErrMovementBattle    = errors.New("movement interrupted by an observed battle")
	ErrTravelBattleLimit = errors.New("travel battle recovery limit reached")
	ErrTravelDead        = errors.New("travel character is dead; recovery is required")
)

type MovementBattleRecovery interface {
	Escape(context.Context) error
}

// TravelBattle handles ordinary solo travel encounters through native escape
// commands. It never attacks other players, changes party membership, or uses
// EO to abort an unfinished battle. The containing task's prepared checkpoint
// remains uncertain after a crash or ambiguous write and prevents replay.
type TravelBattle struct {
	Backend *GameBackend
}

func (r *TravelBattle) Escape(ctx context.Context) error {
	if r == nil || r.Backend == nil || r.Backend.Session == nil {
		return errors.New("travel battle backend is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	writer := &MovementSkill{Backend: r.Backend}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	turns := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := r.Backend.Session.Observe(ctx)
		if err != nil {
			return err
		}
		if snapshot.Account != r.Backend.Binding.AccountID || snapshot.Character != r.Backend.Binding.CharacterName {
			return errors.New("travel battle character binding changed")
		}
		if !snapshot.Connected || !snapshot.Player.HasStatus {
			return errors.New("travel battle session is unavailable")
		}
		battle := snapshot.Battle
		if !battle.Active && (!battle.Ended || battle.LastCommand == "EO") {
			if snapshot.Phase != aigame.PhaseWorld {
				return errors.New("travel battle did not return to world")
			}
			if snapshot.Player.HP <= 0 {
				return ErrTravelDead
			}
			return nil
		}
		var action aigame.Action
		if battle.Result != "" || battle.Ended {
			action = aigame.EndBattle()
		} else {
			if battle.Type != 1 {
				return errors.New("travel escape requires an ordinary PvE encounter")
			}
			// A party escape can change another player's journey. Automated
			// solo recovery does not make that decision for a mixed party.
			if len(snapshot.Party) > 1 {
				return errors.New("travel escape requires a solo character")
			}
			if snapshot.Player.HP <= 0 {
				return ErrTravelDead
			}
			if battle.MyNoKnown {
				for _, actor := range battle.Participants {
					if actor.Player && actor.BattleID != battle.MyNo && actor.BattleID >= 0 && actor.BattleID < 20 && actor.BattleID/10 == battle.MyNo/10 {
						return errors.New("travel escape refuses a battle with another allied player")
					}
					if actor.BattleID == battle.MyNo && (actor.Dead || actor.HP <= 0) {
						return ErrTravelDead
					}
				}
			}
			if battle.PlayerCommandReady() {
				if turns >= 5 {
					return ErrTravelBattleLimit
				}
				command := "E"
				if battle.BPFlags&(aigame.BattlePlayerMenuOff|aigame.BattleEnemySurprise) != 0 {
					command = "N"
				}
				action = aigame.Battle(command)
			} else if battle.PetCommandReady() {
				action = aigame.Battle("W|FF|FF")
			}
		}
		if action.Kind != "" {
			if err := writer.submit(ctx, snapshot.Revision, action); err != nil {
				if !errors.Is(err, aigame.ErrStaleRevision) && !errors.Is(err, aigame.ErrBattleNotReady) {
					return err
				}
			} else if action.Kind == aigame.ActionBattle && action.Command != "W|FF|FF" {
				turns++
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
