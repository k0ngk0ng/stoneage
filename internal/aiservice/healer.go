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

// HealerContract is installed by the server owner after reviewing the native
// npc_windowhealer source and effective NPC configuration. Rates are the
// integer CHAR_WORK_HP value, after the server's argument/default conversion.
// Requests can select an alias, never supply rates or NPC identity.
type HealerContract struct {
	NPC           NPCSpec
	PaidFromLevel int32
	HPRateMilli   int32
}

type HealerRates struct {
	PaidFromLevel int32 `json:"paid_from_level"`
	HPRateMilli   int32 `json:"hp_rate_milli"`
}

func (r HealerRates) validate() error {
	if r.PaidFromLevel < 0 || r.HPRateMilli <= 0 || r.HPRateMilli > 1000000 {
		return errors.New("invalid reviewed healer rates")
	}
	return nil
}

// HealerContracts projects reviewed nurse metadata from the same protected
// NPC catalog loaded by the runtime. Missing metadata never opts an NPC in.
func (r NPCRegistry) HealerContracts() (map[string]HealerContract, error) {
	result := make(map[string]HealerContract)
	for alias := range r {
		npc, ok := r.Lookup(alias)
		if !ok {
			return nil, ErrNPCRegistry
		}
		if npc.Healer != nil {
			result[npc.Alias] = HealerContract{NPC: npc, PaidFromLevel: npc.Healer.PaidFromLevel, HPRateMilli: npc.Healer.HPRateMilli}
		}
	}
	return result, nil
}

type HealerSkill struct {
	Backend   *GameBackend
	Contracts map[string]HealerContract
}

func (c HealerContract) registry(quote int64) (NPCRegistry, error) {
	if err := (HealerRates{PaidFromLevel: c.PaidFromLevel, HPRateMilli: c.HPRateMilli}).validate(); err != nil {
		return nil, err
	}
	n := c.NPC
	n.WindowType, n.WindowSequence, n.WindowObjectID = 0, 0, 0
	n.Choices = nil
	n.WindowObjectFromActor = false
	n.Windows = []NPCWindowSpec{
		{Type: 2, Sequence: 220, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, Data: "2"}}},
		{Type: 0, Sequence: 221, WindowObjectFromActor: true, Choices: map[int]NPCChoice{4: {Button: 4, MaximumCost: quote}}},
	}
	return NewNPCRegistry([]NPCSpec{n})
}

func (c HealerContract) hpQuote(p aigame.PlayerSnapshot) (int64, error) {
	if !p.HasStatus || p.Level < 1 || p.HP <= 0 || p.MaxHP <= 0 || p.HP > p.MaxHP {
		return 0, errors.New("healing requires known living player status")
	}
	if p.Level < c.PaidFromLevel || p.HP == p.MaxHP {
		return 0, nil
	}
	// Match NPC_WindowCostCheck's double multiplication then conversion to C
	// int. Do not round up fractional fees.
	cost := float64(p.Level) * (float64(c.HPRateMilli) / 1000)
	if cost > 2147483647 {
		return 0, errors.New("healer fee exceeds native integer range")
	}
	if cost < 1 {
		return 1, nil
	}
	return int64(cost), nil
}

func (s *HealerSkill) contract(a automation.Action) (HealerContract, error) {
	if s == nil || s.Backend == nil || s.Backend.Session == nil || s.Backend.Gate == nil {
		return HealerContract{}, aimcp.ErrBackend
	}
	if a.Skill != "npc.heal" || a.MaximumCost < 0 {
		return HealerContract{}, aimcp.ErrInvalidParams
	}
	var args struct {
		NPC string `json:"npc"`
	}
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return HealerContract{}, err
	}
	c, ok := s.Contracts[args.NPC]
	if !ok || args.NPC != c.NPC.Alias {
		return HealerContract{}, ErrNPCUnknown
	}
	_, err := c.registry(0)
	return c, err
}

func (s *HealerSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	_, err := s.contract(a)
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Execute restores player HP at an already reached, reviewed nurse. The
// surrounding automation engine owns the durable prepared checkpoint. No
// state-changing packet is retried, including when the transport outcome is
// unknown. Route selection, evacuation and pet-only healing are separate work.
func (s *HealerSkill) Execute(ctx context.Context, a automation.Action) error {
	c, err := s.contract(a)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	npc := NewNPCSkill(s.Backend, nil)
	initial, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || initial.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	quote, err := c.hpQuote(initial.Player)
	if err != nil {
		return err
	}
	if quote > a.MaximumCost {
		return errors.New("healer quote exceeds maximum cost")
	}
	registry, err := c.registry(quote)
	if err != nil {
		return err
	}
	npc.Registry = registry
	spec, _ := registry.Lookup(c.NPC.Alias)
	if _, _, err := validateTalkSnapshot(spec, npcTalkArguments{NPC: spec.Alias}, initial); err != nil {
		return err
	}
	if initial.Battle.Active {
		return ErrMovementBattle
	}
	if initial.Player.HP == initial.Player.MaxHP {
		return nil
	}
	unchanged := func(snapshot aigame.Snapshot) error {
		if snapshot.Position.Floor != initial.Position.Floor || snapshot.Position.X != initial.Position.X || snapshot.Position.Y != initial.Position.Y {
			return errors.New("character moved during healing")
		}
		if snapshot.Battle.Active || snapshot.Player.Level != initial.Player.Level || snapshot.Player.MaxHP != initial.Player.MaxHP {
			return errors.New("player state changed during healer quote")
		}
		return nil
	}
	talkArgs, _ := json.Marshal(map[string]string{"npc": spec.Alias})
	if err := npc.Execute(ctx, automation.Action{Skill: "npc.talk", ExpectedRevision: initial.Revision, Arguments: talkArgs}); err != nil {
		return err
	}
	for _, sequence := range []int32{220, 221} {
		snapshot, err := waitHealerState(ctx, npc, func(snapshot aigame.Snapshot) (bool, error) {
			if err := unchanged(snapshot); err != nil {
				return false, err
			}
			return snapshot.ActiveWindow != nil && snapshot.ActiveWindow.Open && snapshot.ActiveWindow.Sequence == sequence, nil
		})
		if err != nil {
			return err
		}
		args := npcWindowArguments{NPC: spec.Alias, WindowSequence: json.RawMessage(fmt.Sprint(sequence)), Choice: json.RawMessage("1")}
		button, data := int32(1), "2"
		if sequence == 221 {
			args.Choice = json.RawMessage("4")
			button, data = 4, ""
		}
		if err := npc.submit(ctx, snapshot.Revision, func(current aigame.Snapshot) (aigame.Action, error) {
			if err := unchanged(current); err != nil {
				return aigame.Action{}, err
			}
			if _, _, err := validateTalkSnapshot(spec, npcTalkArguments{NPC: spec.Alias}, current); err != nil {
				return aigame.Action{}, err
			}
			if err := validateWindowSnapshot(spec, args, current); err != nil {
				return aigame.Action{}, err
			}
			if sequence == 221 {
				if current.ActiveWindow.ButtonType&4 == 0 {
					return aigame.Action{}, errors.New("healer did not offer confirmation")
				}
				fresh, err := c.hpQuote(current.Player)
				if err != nil || fresh != quote || current.Player.HP == current.Player.MaxHP {
					return aigame.Action{}, errors.New("healer quote changed before confirmation")
				}
				funded := false
				if s.Backend.Funding != nil {
					funded, err = s.Backend.Funding(ctx)
					if err != nil {
						return aigame.Action{}, err
					}
				}
				if !funded && int64(current.Player.Gold) < quote {
					return aigame.Action{}, errors.New("insufficient funds for healer")
				}
			}
			return aigame.Window(current.Position.X, current.Position.Y, sequence, current.ActiveWindow.ObjectID, button, data), nil
		}); err != nil {
			return err
		}
	}
	_, err = waitHealerState(ctx, npc, func(snapshot aigame.Snapshot) (bool, error) {
		if err := unchanged(snapshot); err != nil {
			return false, err
		}
		return snapshot.Revision > initial.Revision && snapshot.Player.HasStatus && snapshot.Player.HP == initial.Player.MaxHP && snapshot.Player.MaxHP == initial.Player.MaxHP, nil
	})
	return err
}

func waitHealerState(ctx context.Context, npc *NPCSkill, ready func(aigame.Snapshot) (bool, error)) (aigame.Snapshot, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := npc.observe(ctx)
		if err != nil {
			return snapshot, err
		}
		if !snapshot.Connected || snapshot.Phase != aigame.PhaseWorld || snapshot.Battle.Active {
			return snapshot, errors.New("healing interrupted by game state")
		}
		ok, err := ready(snapshot)
		if err != nil || ok {
			return snapshot, err
		}
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-ticker.C:
		}
	}
}
