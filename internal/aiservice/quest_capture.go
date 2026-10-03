package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var ErrPetNotCaptured = errors.New("battle ended without a newly confirmed matching pet")

// QuestCaptureSkill attempts one requested species in an already entered solo
// PvE battle. It does not replace/free owned pets, use items, or grant ownership
// from a successful T write. A new stable own-pet identity is required.
type QuestCaptureSkill struct{ Backend *GameBackend }
type captureArguments struct {
	WeakenAbovePercent int   `json:"weaken_above_percent,omitempty"`
	SpeciesID          int32 `json:"species_id"`
	MinimumLevel       int32 `json:"minimum_level"`
	MaximumLevel       int32 `json:"maximum_level"`
	MaxAttempts        int   `json:"max_attempts"`
	MaxTurns           int   `json:"max_turns"`
}

func (s *QuestCaptureSkill) ValidateSkill(ctx context.Context, action automation.Action) error {
	if s == nil || s.Backend == nil || s.Backend.Session == nil || s.Backend.Gate == nil {
		return aimcp.ErrBackend
	}
	if action.Skill != "pet.capture" || action.MaximumCost != 0 {
		return errors.New("pet.capture requires zero monetary/item cost")
	}
	var a captureArguments
	if err := decodeArguments(action.Arguments, &a); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(action.Arguments, &fields); err != nil || len(fields["species_id"]) == 0 || string(fields["species_id"]) == "null" {
		return errors.New("pet.capture requires an explicit species_id")
	}
	if a.SpeciesID < 0 || a.MinimumLevel < 1 || a.MaximumLevel < a.MinimumLevel || a.MaxAttempts < 1 || a.MaxAttempts > 20 || a.MaxTurns < 1 || a.MaxTurns > 100 || a.WeakenAbovePercent < 0 || a.WeakenAbovePercent > 90 {
		return aimcp.ErrInvalidParams
	}
	return ctx.Err()
}

func captureSolo(s aigame.Snapshot) error {
	if !s.Connected || !s.Battle.Active || s.Phase != aigame.PhaseBattle || (s.Battle.Type != 1 && s.Battle.Type != 5 && s.Battle.Type != 6) || s.Battle.LadderID != "" || len(s.Party) > 1 || s.Trade.Active {
		return errors.New("pet capture requires an active solo PvE battle")
	}
	if !s.Battle.MyNoKnown {
		return errors.New("pet capture requires the observed player slot")
	}
	for _, p := range s.Battle.Participants {
		if p.Player && p.BattleID != s.Battle.MyNo {
			return errors.New("pet capture refuses another human participant")
		}
	}
	return nil
}

func refreshCaptureQuote(ctx context.Context, npc *NPCSkill) (aigame.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		before, err := npc.observe(ctx)
		if err != nil {
			return before, err
		}
		if err := captureSolo(before); err != nil {
			return before, err
		}
		id := fmt.Sprintf("%016x", nextInventoryRequest.Add(1))
		err = npc.submit(ctx, before.Revision, func(s aigame.Snapshot) (aigame.Action, error) {
			if err := captureSolo(s); err != nil {
				return aigame.Action{}, err
			}
			return aigame.Action{Kind: aigame.ActionStatus, Command: "BCAP:" + id}, nil
		})
		if errors.Is(err, aigame.ErrStaleRevision) {
			if ctx.Err() != nil {
				return before, ctx.Err()
			}
			continue
		}
		if err != nil {
			return before, err
		}
		observed, err := waitIdentityState(ctx, npc, func(s aigame.Snapshot) (bool, error) {
			if !s.Battle.Active || s.Battle.Ended || s.Battle.Result != "" {
				return false, errors.New("battle ended while obtaining capture quote")
			}
			if err := captureSolo(s); err != nil {
				return false, err
			}
			q := s.Capture
			if q == nil && s.AIObservationRevision > before.Revision {
				return false, errIdentityReplySuperseded
			}
			if q == nil || q.RequestID != id || q.Revision <= before.Revision {
				return false, nil
			}
			if !q.Active || q.Self != s.Battle.MyNo || q.Turn != s.Battle.Turn {
				return false, errors.New("capture quote does not match the current PvE turn")
			}
			return true, nil
		})
		if !errors.Is(err, errIdentityReplySuperseded) {
			return observed, err
		}
	}
	return aigame.Snapshot{}, errors.New("capture quote changed repeatedly during refresh")
}

func freeCaptureTarget(s aigame.Snapshot, a captureArguments) (int32, error) {
	q := s.Capture
	if q == nil || !q.Active || q.Self != s.Battle.MyNo || q.Turn != s.Battle.Turn || q.FreeSlots < 1 {
		return -1, errors.New("fresh capture quote and a free pet slot are required")
	}
	target := int32(-1)
	for _, t := range q.Targets {
		// Native can retarget a dead target before execution. Refuse item
		// requirements for every possible enemy, not only our first choice.
		if len(t.RequiredItems) != 0 || len(t.Consumption) != 0 {
			return -1, errors.New("this encounter can consume capture items; item-funded capture is not enabled")
		}
		matched := false
		for _, p := range s.Battle.Participants {
			if p.BattleID == t.Slot && !p.Player && !p.Dead && p.HP > 0 && p.Graphic == t.Graphic && p.Level == t.Level {
				matched = true
			}
		}
		if !matched {
			return -1, errors.New("capture quote disagrees with visible combatants")
		}
		if t.Eligible && t.SpeciesID == a.SpeciesID && t.Level >= a.MinimumLevel && t.Level <= a.MaximumLevel && (target < 0 || t.Slot < target) {
			target = t.Slot
		}
	}
	for _, p := range s.Battle.Participants {
		if p.BattleID/10 == s.Battle.MyNo/10 || p.Dead || p.HP <= 0 {
			continue
		}
		found := false
		for _, t := range q.Targets {
			found = found || t.Slot == p.BattleID
		}
		if !found {
			return -1, errors.New("capture quote omits a live opponent")
		}
	}
	return target, nil
}

func newCapturedPet(s aigame.Snapshot, before map[string]bool, a captureArguments) bool {
	for _, pet := range s.Pets {
		if pet.IdentityKnown && pet.StableID != "" && !before[pet.StableID] && pet.SpeciesIDKnown && pet.SpeciesID == a.SpeciesID && pet.Level >= a.MinimumLevel && pet.Level <= a.MaximumLevel {
			return true
		}
	}
	return false
}

func (s *QuestCaptureSkill) Execute(ctx context.Context, action automation.Action) error {
	if err := s.ValidateSkill(ctx, action); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var args captureArguments
	_ = json.Unmarshal(action.Arguments, &args)
	npc := NewNPCSkill(s.Backend, nil)
	first, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	if action.ExpectedRevision == 0 || first.Revision != action.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	if err := captureSolo(first); err != nil {
		return err
	}
	fresh, err := refreshOwnIdentity(ctx, npc, true)
	if err != nil {
		return err
	}
	before := map[string]bool{}
	for _, p := range fresh.Pets {
		if !p.IdentityKnown || p.StableID == "" {
			return errors.New("capture requires all existing pet identities")
		}
		before[p.StableID] = true
	}
	if len(before) >= 5 {
		return errors.New("no free pet slot; existing pets are never discarded")
	}
	attempts := 0
	startTurn := first.Battle.Turn
	sentPlayer, sentPet := map[int32]bool{}, map[int32]bool{}
	finish := func(current aigame.Snapshot) error {
		if current.Battle.Active {
			remaining := args.MaxTurns - int(current.Battle.Turn-startTurn)
			if remaining < 1 {
				remaining = 1
			}
			raw, _ := json.Marshal(questBattleArguments{MaxTurns: remaining})
			if err := (&QuestBattleSkill{Backend: s.Backend}).Execute(ctx, automation.Action{Skill: "battle.finish", ExpectedRevision: current.Revision, Arguments: raw}); err != nil {
				return err
			}
		}
		observed, err := refreshOwnIdentity(ctx, npc, false)
		if err != nil {
			return err
		}
		if !newCapturedPet(observed, before, args) {
			return ErrPetNotCaptured
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := npc.observe(ctx)
		if err != nil {
			return err
		}
		if !current.Battle.Active || current.Battle.Ended || current.Battle.Result != "" || current.Battle.MySideDefeated() {
			return finish(current)
		}
		if err := captureSolo(current); err != nil {
			return err
		}
		if int(current.Battle.Turn-startTurn) >= args.MaxTurns {
			return errors.New("capture turn limit reached")
		}
		if current.Battle.PlayerCommandReady() && !sentPlayer[current.Battle.Turn] {
			current, err = refreshOwnIdentity(ctx, npc, true)
			if err != nil {
				return err
			}
			if !current.Battle.Active || current.Battle.Result != "" || current.Battle.Ended {
				return finish(current)
			}
			if !current.Battle.PlayerCommandReady() || sentPlayer[current.Battle.Turn] {
				continue
			}
			if newCapturedPet(current, before, args) || attempts >= args.MaxAttempts {
				return finish(current)
			}
			current, err = refreshCaptureQuote(ctx, npc)
			if err != nil {
				return err
			}
			target, err := freeCaptureTarget(current, args)
			if err != nil {
				return err
			}
			if target < 0 {
				return finish(current)
			}
			command := fmt.Sprintf("T|%X", target)
			capture := current.Battle.BPFlags&(aigame.BattlePlayerMenuOff|aigame.BattleEnemySurprise) == 0
			if !capture {
				command = "N"
			} else if args.WeakenAbovePercent > 0 {
				for _, p := range current.Battle.Participants {
					if p.BattleID == target && p.MaxHP > 0 && int64(p.HP)*100 > int64(p.MaxHP)*int64(args.WeakenAbovePercent) {
						command = fmt.Sprintf("H|%X", target)
						capture = false
					}
				}
			}
			err = npc.submit(ctx, current.Revision, func(now aigame.Snapshot) (aigame.Action, error) {
				if err := captureSolo(now); err != nil {
					return aigame.Action{}, err
				}
				if !now.Battle.PlayerCommandReady() || now.Battle.Turn != current.Battle.Turn {
					return aigame.Action{}, aigame.ErrBattleNotReady
				}
				if _, err := freeCaptureTarget(now, args); err != nil {
					return aigame.Action{}, err
				}
				return aigame.Action{Kind: aigame.ActionBattle, Command: command}, nil
			})
			if errors.Is(err, aigame.ErrStaleRevision) || errors.Is(err, aigame.ErrBattleNotReady) {
				continue
			}
			if err != nil {
				return err
			}
			sentPlayer[current.Battle.Turn] = true
			if capture {
				attempts++
			}
		} else if current.Battle.PetCommandReady() && !sentPet[current.Battle.Turn] {
			err = npc.submit(ctx, current.Revision, func(now aigame.Snapshot) (aigame.Action, error) {
				if err := captureSolo(now); err != nil {
					return aigame.Action{}, err
				}
				if !now.Battle.PetCommandReady() || now.Battle.Turn != current.Battle.Turn {
					return aigame.Action{}, aigame.ErrBattleNotReady
				}
				return aigame.Action{Kind: aigame.ActionBattle, Command: "W|FF|FF"}, nil
			})
			if errors.Is(err, aigame.ErrStaleRevision) || errors.Is(err, aigame.ErrBattleNotReady) {
				continue
			}
			if err != nil {
				return err
			}
			sentPet[current.Battle.Turn] = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
