package aiservice

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// NPCPassage authorizes only a reviewed NPCEnemy dieact=0 challenge. It is
// installed with the NPC catalog, never supplied in a movement request.
type NPCPassage struct {
	WindowSequence   int `json:"window_sequence"`
	Choice           int `json:"choice"`
	MaxTurns         int `json:"max_turns"`
	MinimumLevel     int `json:"minimum_level"`
	MinimumHPPercent int `json:"minimum_hp_percent"`
}

func validatePassageContract(spec NPCSpec) error {
	p := spec.Passage
	if p.MaxTurns < 1 || p.MaxTurns > 100 || p.MinimumLevel < 1 || p.MinimumLevel > 1000 || p.MinimumHPPercent < 50 || p.MinimumHPPercent > 100 || p.Choice != 4 {
		return fmt.Errorf("%w: invalid NPC passage limits", ErrNPCRegistry)
	}
	w, err := resolveNPCWindow(spec, json.RawMessage(fmt.Sprint(p.WindowSequence)))
	if err != nil {
		return err
	}
	c, err := resolveNPCChoice(w.Choices, json.RawMessage(fmt.Sprint(p.Choice)))
	if err != nil {
		return err
	}
	quote, err := verifiedNPCQuote(c)
	if err != nil || w.Type != 0 || !w.WindowObjectFromActor || c.Button != 4 || c.Data != "" || quote != 0 {
		return fmt.Errorf("%w: passage requires an actor-bound free YES challenge", ErrNPCRegistry)
	}
	return nil
}

func validatePassageSnapshot(spec NPCSpec, snap aigame.Snapshot) (aigame.ActorSnapshot, error) {
	p := spec.Passage
	if p == nil || snap.Phase != aigame.PhaseWorld || !snap.Connected || snap.Battle.Active || snap.Trade.Active || len(snap.Party) > 1 || snap.Ladder != nil && snap.Ladder.Snapshot.ReservesCharacter() {
		return aigame.ActorSnapshot{}, fmt.Errorf("%w: passage requires an idle solo world session", ErrMovementOccupied)
	}
	if !snap.Player.HasStatus || snap.Player.Level < int32(p.MinimumLevel) || snap.Player.HP <= 0 || snap.Player.MaxHP <= 0 || snap.Player.HP > snap.Player.MaxHP || int64(snap.Player.HP)*100 < int64(snap.Player.MaxHP)*int64(p.MinimumHPPercent) {
		return aigame.ActorSnapshot{}, fmt.Errorf("%w: passage level or HP requirement is not met", ErrMovementOccupied)
	}
	a, _, err := validateTalkSnapshot(spec, npcTalkArguments{NPC: spec.Alias}, snap)
	if err != nil {
		return a, err
	}
	if a.CharType != 20 || !a.GraphicKnown || a.Graphic <= 0 {
		return a, fmt.Errorf("%w: passage guard is not a visible NPCEnemy", ErrMovementOccupied)
	}
	return a, nil
}

func (s *MovementSkill) openPassage(ctx context.Context, blocked *movementBlockage) error {
	if s.Backend.Knowledge == nil {
		return blocked
	}
	var spec NPCSpec
	count := 0
	for alias := range s.NPCs {
		candidate, ok := s.NPCs.Lookup(alias)
		if ok && candidate.Passage != nil && candidate.Floor == blocked.floor && candidate.X == blocked.tile.X && candidate.Y == blocked.tile.Y && candidate.SourceFingerprint == s.Backend.Knowledge.Fingerprint() {
			spec = candidate
			count++
		}
	}
	if count != 1 {
		return blocked
	}
	// Approach uses all existing travel checks and cannot recursively start
	// another guard challenge. Unknown writes still abort the whole journey.
	approach := *s
	approach.NPCs = nil
	current, err := s.Backend.Observe(ctx, s.Backend.Binding)
	if err != nil {
		return err
	}
	if current.Floor != blocked.floor {
		return blocked
	}
	raw, _ := json.Marshal(movementArguments{Floor: blocked.floor, X: blocked.approach.X, Y: blocked.approach.Y})
	if err := approach.Execute(ctx, automation.Action{Skill: "move", Arguments: raw, ExpectedRevision: current.Revision}); err != nil {
		return err
	}
	npc := NewNPCSkill(s.Backend, s.NPCs)
	snap, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	// Another player can clear a gate while we approach. Accept only the
	// same catalogued NPCEnemy's explicit disappearance, not a missing actor.
	actor, err := findNPCActor(spec, npcTalkArguments{}, snap)
	if err != nil {
		return err
	}
	if actor.CharType == 20 && actor.GraphicKnown && actor.Graphic == 0 {
		return nil
	}
	actor, err = validatePassageSnapshot(spec, snap)
	if err != nil {
		return err
	}
	game := &AutomationGame{Backend: s.Backend, NPCs: s.NPCs, Skills: SkillSet{"npc.talk": npc, "npc.window": npc, "battle.finish": &QuestBattleSkill{Backend: s.Backend}}}
	run := func(skill string, args any) error {
		o, err := s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		return game.Execute(ctx, automation.Action{Skill: skill, Arguments: raw, ExpectedRevision: o.Revision})
	}
	if err := run("npc.talk", map[string]any{"npc": spec.Alias}); err != nil {
		return err
	}
	args := npcWindowArguments{NPC: spec.Alias, WindowSequence: json.RawMessage(fmt.Sprint(spec.Passage.WindowSequence)), Choice: json.RawMessage(fmt.Sprint(spec.Passage.Choice))}
	if err := waitPassage(ctx, npc, func(o aigame.Snapshot) bool { return validateWindowSnapshot(spec, args, o) == nil }); err != nil {
		return err
	}
	if err := run("npc.window", args); err != nil {
		return err
	}
	if err := waitPassage(ctx, npc, func(o aigame.Snapshot) bool { return o.Battle.Active }); err != nil {
		return err
	}
	if err := run("battle.finish", questBattleArguments{MaxTurns: spec.Passage.MaxTurns}); err != nil {
		return err
	}
	return waitPassage(ctx, npc, func(o aigame.Snapshot) bool {
		if o.Phase != aigame.PhaseWorld || o.Battle.Active || o.Position.Floor != int32(spec.Floor) {
			return false
		}
		a, err := findNPCActor(spec, npcTalkArguments{}, o)
		return err == nil && a.ID == actor.ID && a.CharType == 20 && a.GraphicKnown && a.Graphic == 0
	})
}

func waitPassage(ctx context.Context, npc *NPCSkill, ready func(aigame.Snapshot) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		snap, err := npc.observe(ctx)
		if err != nil {
			return err
		}
		if ready(snap) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("passage confirmation missing: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
