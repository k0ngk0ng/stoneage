package aiservice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func petDeliveryFixture() (*NPCPetDelivery, aigame.Snapshot, []string) {
	d := &NPCPetDelivery{Predicates: []string{"PET>0-113*2"}, AcceptText: "交付宠物？", RequiredItems: map[int32]int{20001: 1}, ForbiddenItems: []int32{20002}}
	pets := []aigame.PetSnapshot{
		{Slot: 2, StableID: "captured-b", IdentityKnown: true, SpeciesID: 113, SpeciesIDKnown: true, EventFlagKnown: true, Level: 1},
		{Slot: 0, StableID: "captured-a", IdentityKnown: true, SpeciesID: 113, SpeciesIDKnown: true, EventFlagKnown: true, Level: 1},
		{Slot: 4, StableID: "original-pet", IdentityKnown: true, SpeciesID: 113, SpeciesIDKnown: true, EventFlagKnown: true, Level: 1},
	}
	s := aigame.Snapshot{Pets: slices.Clone(pets), AI: aigame.AIObservation{Received: true, ItemsKnown: true, Pets: pets, Items: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 20001}}}, ActiveWindow: &aigame.WindowSnapshot{Data: d.AcceptText, ButtonType: 12}}
	return d, s, []string{"captured-b", "captured-a"}
}

func TestPetDeliveryMatchesNativeSlotsAndStrictLevels(t *testing.T) {
	d, s, ids := petDeliveryFixture()
	if err := d.validate(s, ids); err != nil {
		t.Fatal(err)
	}
	for _, predicate := range []string{"PET>1-113*2", "PET<1-113*2", "EVPET>0-113*2"} {
		d.Predicates[0] = predicate
		if err := d.validate(s, ids); err == nil {
			t.Fatalf("accepted wrong native predicate %s", predicate)
		}
	}
	d.Predicates[0] = "PET=1-113*2"
	if err := d.validate(s, ids); err != nil {
		t.Fatal(err)
	}
	// The two requested later pets match, but native would delete slot 0.
	if err := d.validate(s, []string{"captured-b", "original-pet"}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatal("substituted a later pet for the server's first match", err)
	}
}

func TestPetDeliveryRejectsNativeRewardCapacityBeforeConfirmation(t *testing.T) {
	d, s, ids := petDeliveryFixture()
	d.GoldReward = 500
	s.AI.GoldLimitKnown, s.AI.GoldLimit = true, 1000000
	for _, gold := range []int32{999499, 999500, 999950} {
		s.Player.Gold = gold
		if err := d.validate(s, ids); (err == nil) != (gold == 999499) {
			t.Fatal(gold, err)
		}
	}
	s.AI.GoldLimitKnown = false
	s.Player.Gold = 0
	if err := d.validate(s, ids); err == nil {
		t.Fatal("unknown reward capacity accepted")
	}
}

func TestPetDeliveryRejectsUncertainOrDifferentBranch(t *testing.T) {
	for _, mode := range []string{"extra-id", "duplicate-id", "unknown-id", "unknown-species", "unknown-event", "changed-slot", "missing-pet", "duplicate-slot", "missing-voucher", "other-voucher", "other-message", "unbounded", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			d, s, ids := petDeliveryFixture()
			switch mode {
			case "extra-id":
				ids = append(ids, "original-pet")
			case "duplicate-id":
				ids[1] = ids[0]
			case "unknown-id":
				s.AI.Pets[2].IdentityKnown = false
			case "unknown-species":
				s.AI.Pets[2].SpeciesIDKnown = false
			case "unknown-event":
				s.AI.Pets[2].EventFlagKnown = false
			case "changed-slot":
				s.Pets[0].StableID = "replacement"
			case "missing-pet":
				s.Pets = s.Pets[:2]
			case "duplicate-slot":
				s.AI.Pets[0].Slot = 0
			case "missing-voucher":
				s.AI.Items = nil
			case "other-voucher":
				s.AI.Items = append(s.AI.Items, aigame.AIInventoryItem{Slot: 6, TemplateID: 20002})
			case "other-message":
				s.ActiveWindow.Data = "另一项任务"
			case "unbounded":
				d.Predicates[0] = "PET>0-113"
			case "overlap":
				d.Predicates = append(d.Predicates, "PET>0-113*1")
			}
			if err := d.validate(s, ids); err == nil {
				t.Fatal("unsafe delivery accepted")
			}
		})
	}
}

type petDeliverySession struct {
	*npcSkillSession
	unauthorized bool
}

func (s *petDeliverySession) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	if err := s.npcSkillSession.ExecuteExpected(ctx, revision, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Kind == aigame.ActionStatus && strings.HasPrefix(a.Command, "AI:") {
		s.snapshot.AI.RequestID = strings.TrimPrefix(a.Command, "AI:")
		s.snapshot.AIObservationRevision = s.snapshot.Revision
		if s.unauthorized {
			s.snapshot.AI.Pets[1].StableID = "players-original-pet"
			s.snapshot.Pets[1].StableID = "players-original-pet"
		}
	}
	return nil
}

func TestNPCPetDeliveryRefreshesAndGuardsFinalWrite(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		skill, base, spec := npcSkillFixture(t, true)
		d, state, ids := petDeliveryFixture()
		spec.WindowType = 0
		spec.Choices = map[int]NPCChoice{4: {Button: 4, PetDelivery: d}}
		skill.Registry = MustNPCRegistry([]NPCSpec{spec})
		// Catalog callers cannot mutate the installed destructive contract.
		d.Predicates[0] = "PET>0-999*2"
		base.snapshot.Pets, base.snapshot.AI = state.Pets, state.AI
		base.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 100, ObjectID: 42, Data: state.ActiveWindow.Data, ButtonType: 12, Open: true}
		session := &petDeliverySession{npcSkillSession: base, unauthorized: unsafe}
		skill.Backend.Session = session
		raw, _ := json.Marshal(map[string]any{"npc": "trainer", "window_sequence": 100, "choice": 4, "pet_ids": ids})
		err := skill.Execute(context.Background(), automation.Action{Skill: "npc.window", ExpectedRevision: 12, Arguments: raw})
		if (err != nil) != unsafe {
			t.Fatalf("unsafe=%t err=%v", unsafe, err)
		}
		want := 2
		if unsafe {
			want = 1
		}
		if len(base.actions) != want || base.actions[0].action.Kind != aigame.ActionStatus {
			t.Fatal(base.actions)
		}
		if !unsafe && (base.actions[1].action.Kind != aigame.ActionWindow || base.actions[1].action.WindowSelect != 4) {
			t.Fatal(base.actions)
		}
	}
}

func TestNPCPetDeliveryCannotBeBypassedByAnotherChoice(t *testing.T) {
	d, _, _ := petDeliveryFixture()
	for _, button := range []int{1, 4, 16} {
		spec := verifiedNPCSpec()
		spec.WindowType = 0
		spec.Choices = map[int]NPCChoice{4: {Button: 4, PetDelivery: d}, 10: {Button: button}}
		if _, err := NewNPCRegistry([]NPCSpec{spec}); err == nil {
			t.Fatalf("unguarded alternative button %d bypasses delivery protection", button)
		}
	}
}
