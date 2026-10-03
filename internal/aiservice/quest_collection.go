package aiservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type PetCollectionTarget struct {
	SpeciesID    int32 `json:"species_id"`
	MinimumLevel int32 `json:"minimum_level"`
	MaximumLevel int32 `json:"maximum_level"`
	Count        int   `json:"count"`
}
type petCollectionArguments struct {
	Targets       []PetCollectionTarget `json:"targets"`
	MaxEncounters int                   `json:"max_encounters"`
	MaxMoves      int                   `json:"max_moves"`
	MaxAttempts   int                   `json:"max_attempts"`
	MaxTurns      int                   `json:"max_turns"`
}
type collectionPet struct {
	ID        string `json:"id"`
	SpeciesID int32  `json:"species_id"`
}
type petCollectionState struct {
	Version    int             `json:"version"`
	Definition string          `json:"definition"`
	Phase      string          `json:"phase"`
	Protected  []string        `json:"protected"`
	Collected  []collectionPet `json:"collected"`
	Encounters int             `json:"encounters"`
	Moves      int             `json:"moves"`
	Heals      int             `json:"heals,omitempty"`
}

// QuestPetCollectionSkill persists original and acquired identities before
// proceeding to another encounter. Only confirmed outputs can be delivered.
type QuestPetCollectionSkill struct {
	Backend   *GameBackend
	Movement  *MovementSkill
	Navigator *LevelingNavigator
}

func collectionArguments(a automation.Action) (petCollectionArguments, error) {
	var args petCollectionArguments
	if a.Skill != "pet.collect" || a.MaximumCost != 0 {
		return args, errors.New("pet.collect requires zero monetary cost")
	}
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return args, err
	}
	var fields struct {
		Targets []map[string]json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(a.Arguments, &fields); err != nil {
		return args, err
	}
	for _, target := range fields.Targets {
		if raw := target["species_id"]; len(raw) == 0 || string(raw) == "null" {
			return args, errors.New("pet.collect requires an explicit species_id for every target")
		}
	}
	if len(args.Targets) == 0 || len(args.Targets) > 5 || args.MaxEncounters < 1 || args.MaxEncounters > 256 || args.MaxMoves < 1 || args.MaxMoves > 4096 || args.MaxAttempts < 1 || args.MaxAttempts > 20 || args.MaxTurns < 1 || args.MaxTurns > 100 {
		return args, errors.New("invalid pet collection limits")
	}
	seen := map[int32]bool{}
	total := 0
	for _, t := range args.Targets {
		if t.SpeciesID < 0 || seen[t.SpeciesID] || t.MinimumLevel < 1 || t.MaximumLevel < t.MinimumLevel || t.MaximumLevel > 1000 || t.Count < 1 || t.Count > 5 {
			return args, errors.New("invalid pet collection target")
		}
		seen[t.SpeciesID] = true
		total += t.Count
	}
	if total > 5 {
		return args, errors.New("collection exceeds five pet slots")
	}
	return args, nil
}
func collectionDefinition(a petCollectionArguments) string {
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *QuestPetCollectionSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	if s == nil || s.Backend == nil || s.Movement == nil || s.Navigator == nil {
		return errors.New("pet collection navigation unavailable")
	}
	_, err := collectionArguments(a)
	if err != nil {
		return err
	}
	return ctx.Err()
}
func (*QuestPetCollectionSkill) Execute(context.Context, automation.Action) error {
	return errors.New("pet collection requires a durable task checkpoint")
}

func collectionWorld(o aigame.Snapshot) error {
	if !o.Connected || o.Phase != aigame.PhaseWorld || o.Battle.Active || !o.Player.HasStatus || o.Player.HP <= 0 || len(o.Party) > 1 || o.Trade.Active {
		return errors.New("pet collection requires a living, synchronized solo world session")
	}
	return nil
}
func collectionOwned(o aigame.Snapshot) (map[string]aigame.PetSnapshot, error) {
	owned := map[string]aigame.PetSnapshot{}
	for _, p := range o.Pets {
		if !p.IdentityKnown || p.StableID == "" || !p.SpeciesIDKnown || !p.EventFlagKnown {
			return nil, errors.New("pet collection requires complete stable pet identities")
		}
		if _, ok := owned[p.StableID]; ok {
			return nil, errors.New("duplicate pet identity")
		}
		owned[p.StableID] = p
	}
	if len(owned) > 5 {
		return nil, errors.New("invalid owned pet count")
	}
	return owned, nil
}
func (st petCollectionState) remaining(args petCollectionArguments) []PetCollectionTarget {
	var result []PetCollectionTarget
	for _, t := range args.Targets {
		for _, p := range st.Collected {
			if p.SpeciesID == t.SpeciesID {
				t.Count--
			}
		}
		if t.Count > 0 {
			result = append(result, t)
		}
	}
	return result
}
func (st petCollectionState) checkOwned(o aigame.Snapshot, args petCollectionArguments) error {
	owned, err := collectionOwned(o)
	if err != nil {
		return err
	}
	if len(st.Protected)+len(st.Collected) != len(owned) {
		return errors.New("owned pets changed outside the recorded collection")
	}
	seen := map[string]bool{}
	for _, id := range st.Protected {
		if _, ok := owned[id]; !ok || seen[id] {
			return errors.New("protected pet missing or duplicated")
		}
		seen[id] = true
	}
	for _, p := range st.Collected {
		current, ok := owned[p.ID]
		if !ok || seen[p.ID] || current.SpeciesID != p.SpeciesID || current.EventFlag != 0 {
			return errors.New("collected pet identity or kind changed")
		}
		seen[p.ID] = true
		valid := false
		for _, t := range args.Targets {
			valid = valid || t.SpeciesID == p.SpeciesID && current.Level >= t.MinimumLevel && current.Level <= t.MaximumLevel
		}
		if !valid {
			return errors.New("collected pet no longer matches required levels")
		}
	}
	return nil
}
func loadCollectionProgress(p automation.StepProgress, args petCollectionArguments) (petCollectionState, error) {
	var st petCollectionState
	if err := json.Unmarshal(p.State, &st); err != nil {
		return st, err
	}
	if st.Version != 1 || st.Definition != collectionDefinition(args) || st.Encounters < 0 || st.Moves < 0 || st.Encounters > args.MaxEncounters || st.Moves > args.MaxMoves || st.Heals < 0 || st.Heals > 32 {
		return st, errors.New("invalid collection checkpoint")
	}
	if len(st.Protected)+len(st.Collected) > 5 {
		return st, errors.New("collection checkpoint exceeds pet capacity")
	}
	return st, nil
}

// Recovery consumes only reviewed portable supplies already owned by the
// player. Persist the boundary before UseItem; a lost reply cannot turn into
// a second consumption on resume. The next loop checks actual observed HP.
func (s *QuestPetCollectionSkill) recoverPlayer(ctx context.Context, st *petCollectionState, save func(bool) error) error {
	if s.Movement.HealthRecovery == nil {
		return ErrTravelHealingRequired
	}
	if st.Heals >= 32 {
		return ErrTravelHealingLimit
	}
	st.Phase = "healing"
	st.Heals++
	if err := save(false); err != nil {
		return err
	}
	err := s.Movement.HealthRecovery.Heal(ctx)
	if err != nil && !errors.Is(err, ErrHealingItemUnavailable) {
		return err
	}
	// Missing reviewed supplies is a proven pre-write refusal. Permit the
	// player to restock/heal manually and resume with the same collected IDs.
	st.Phase = "idle"
	if saveErr := save(false); saveErr != nil {
		return saveErr
	}
	return err
}
func (s *QuestPetCollectionSkill) CanResumeStep(ctx context.Context, a automation.Action, p automation.StepProgress) error {
	args, err := collectionArguments(a)
	if err != nil {
		return err
	}
	st, err := loadCollectionProgress(p, args)
	if err != nil {
		return err
	}
	if st.Phase != "idle" {
		return errors.New("collection interrupted during an unconfirmed operation")
	}
	o, err := refreshOwnIdentity(ctx, NewNPCSkill(s.Backend, nil), false)
	if err != nil {
		return err
	}
	if err := collectionWorld(o); err != nil {
		return err
	}
	return st.checkOwned(o, args)
}

func (s *QuestPetCollectionSkill) ExecuteStep(ctx context.Context, a automation.Action, scope automation.StepExecution) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	if scope.PlanID == "" || scope.StepID == "" || scope.Save == nil {
		return errors.New("pet collection checkpoint missing")
	}
	args, _ := collectionArguments(a)
	npc := NewNPCSkill(s.Backend, nil)
	first, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || first.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	if err := collectionWorld(first); err != nil {
		return err
	}
	o, err := refreshOwnIdentity(ctx, npc, false)
	if err != nil {
		return err
	}
	owned, err := collectionOwned(o)
	if err != nil {
		return err
	}
	st := petCollectionState{Version: 1, Definition: collectionDefinition(args), Phase: "idle"}
	if len(scope.Progress.State) > 0 {
		st, err = loadCollectionProgress(scope.Progress, args)
		if err != nil {
			return err
		}
		if st.Phase != "idle" {
			return errors.New("collection has an unresolved operation")
		}
		if err := st.checkOwned(o, args); err != nil {
			return err
		}
	} else {
		for id := range owned {
			st.Protected = append(st.Protected, id)
		}
		slices.Sort(st.Protected)
	}
	save := func(confirmed bool) error {
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		p := automation.StepProgress{State: raw, Confirmed: confirmed}
		for _, pet := range st.Collected {
			p.PetIDs = append(p.PetIDs, pet.ID)
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		return scope.Save(persist, p)
	}
	if err := save(false); err != nil {
		return err
	}
	blockedMoves := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		o, err = refreshOwnIdentity(ctx, npc, false)
		if err != nil {
			return err
		}
		if err := collectionWorld(o); err != nil {
			return err
		}
		if err := st.checkOwned(o, args); err != nil {
			return err
		}
		remaining := st.remaining(args)
		if len(remaining) == 0 {
			return save(true)
		}
		need := 0
		for _, t := range remaining {
			need += t.Count
		}
		if len(o.Pets)+need > 5 {
			return errors.New("insufficient free pet slots; existing pets are never discarded")
		}
		if st.Encounters >= args.MaxEncounters || st.Moves >= args.MaxMoves {
			return errors.New("pet collection search limit reached")
		}
		if o.Player.MaxHP <= 0 || o.Player.HP > o.Player.MaxHP {
			return ErrTravelHealingRequired
		}
		if o.Player.HP < o.Player.MaxHP/2+o.Player.MaxHP%2 {
			if err := s.recoverPlayer(ctx, &st, save); err != nil {
				return err
			}
			continue
		}
		nav, err := s.Navigator.NextForPets(ctx, o, remaining)
		if err != nil {
			return err
		}
		if nav.InArea || !nav.Ready {
			return errors.New("pet collection has no executable encounter route")
		}
		to := nav.Destination
		if nav.WarpDestinationKnown {
			to = nav.WarpDestination
		}
		raw, _ := json.Marshal(movementArguments{Floor: int(to.Floor), X: int(to.X), Y: int(to.Y)})
		st.Phase = "moving"
		if err := save(false); err != nil {
			return err
		}
		current, err := s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return err
		}
		move := *s.Movement
		move.BattleRecovery = nil
		err = move.Execute(ctx, automation.Action{Skill: "move", Arguments: raw, ExpectedRevision: current.Revision})
		if errors.Is(err, ErrMovementStartStale) || errors.Is(err, ErrMovementOccupied) {
			blockedMoves++
			st.Phase = "idle"
			if err := save(false); err != nil {
				return err
			}
			if blockedMoves >= 3 {
				return err
			}
			continue
		}
		if err != nil && !errors.Is(err, ErrMovementBattle) {
			return err
		}
		st.Moves++
		blockedMoves = 0
		if err == nil {
			st.Phase = "idle"
			if err := save(false); err != nil {
				return err
			}
			continue
		}
		st.Encounters++
		st.Phase = "battle"
		if err := save(false); err != nil {
			return err
		}
		var currentBattle aigame.Snapshot
		for {
			currentBattle, err = npc.observe(ctx)
			if err != nil {
				return err
			}
			if currentBattle.Battle.PlayerCommandReady() || currentBattle.Battle.Result != "" || currentBattle.Battle.MySideDefeated() {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		var chosen *PetCollectionTarget
		if currentBattle.Battle.PlayerCommandReady() {
			currentBattle, err = refreshCaptureQuote(ctx, npc)
			if err != nil {
				return err
			}
			for _, t := range remaining {
				target, e := freeCaptureTarget(currentBattle, captureArguments{SpeciesID: t.SpeciesID, MinimumLevel: t.MinimumLevel, MaximumLevel: t.MaximumLevel})
				if e != nil {
					return e
				}
				if target >= 0 {
					copy := t
					chosen = &copy
					break
				}
			}
		}
		currentBattle, err = npc.observe(ctx)
		if err != nil {
			return err
		}
		if chosen != nil {
			raw, _ := json.Marshal(captureArguments{SpeciesID: chosen.SpeciesID, MinimumLevel: chosen.MinimumLevel, MaximumLevel: chosen.MaximumLevel, MaxAttempts: args.MaxAttempts, MaxTurns: args.MaxTurns, WeakenAbovePercent: 50})
			err = (&QuestCaptureSkill{Backend: s.Backend}).Execute(ctx, automation.Action{Skill: "pet.capture", Arguments: raw, ExpectedRevision: currentBattle.Revision})
		} else {
			raw, _ := json.Marshal(questBattleArguments{MaxTurns: args.MaxTurns})
			err = (&QuestBattleSkill{Backend: s.Backend}).Execute(ctx, automation.Action{Skill: "battle.finish", Arguments: raw, ExpectedRevision: currentBattle.Revision})
		}
		if err != nil && !errors.Is(err, ErrPetNotCaptured) {
			return err
		}
		after, err := refreshOwnIdentity(ctx, npc, false)
		if err != nil {
			return err
		}
		if err := collectionWorld(after); err != nil {
			return err
		}
		newOwned, err := collectionOwned(after)
		if err != nil {
			return err
		}
		previous := map[string]bool{}
		for _, id := range st.Protected {
			previous[id] = true
		}
		for _, pet := range st.Collected {
			previous[pet.ID] = true
		}
		for id := range previous {
			if _, ok := newOwned[id]; !ok {
				return fmt.Errorf("owned pet disappeared during collection: %s", id)
			}
		}
		for id, pet := range newOwned {
			if previous[id] {
				continue
			}
			if chosen != nil && pet.SpeciesID == chosen.SpeciesID && pet.Level >= chosen.MinimumLevel && pet.Level <= chosen.MaximumLevel && pet.EventFlag == 0 {
				st.Collected = append(st.Collected, collectionPet{ID: id, SpeciesID: pet.SpeciesID})
			} else {
				st.Protected = append(st.Protected, id)
			}
		}
		st.Phase = "idle"
		if err := save(false); err != nil {
			return err
		}
	}
}
