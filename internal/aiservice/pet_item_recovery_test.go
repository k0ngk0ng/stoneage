package aiservice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

type petItemRecoverySession struct {
	mu sync.Mutex

	snapshot        aigame.Snapshot
	items           []aigame.AIInventoryItem
	uses            int
	actions         []aigame.Action
	healAmount      int32
	noConsume       bool
	noHeal          bool
	uncertain       bool
	replaceOnUse    bool
	expectedPetSlot int32
	onStatus        func(*petItemRecoverySession)
	onUse           func(*petItemRecoverySession)
}

func (s *petItemRecoverySession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return aigame.Snapshot{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.snapshot
	snapshot.Pets = append([]aigame.PetSnapshot(nil), s.snapshot.Pets...)
	snapshot.AI.Items = append([]aigame.AIInventoryItem(nil), s.snapshot.AI.Items...)
	return snapshot, nil
}

func (s *petItemRecoverySession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	s.actions = append(s.actions, action)
	s.snapshot.Revision++

	if action.Kind == aigame.ActionStatus && strings.HasPrefix(action.Command, "AI:") {
		s.snapshot.AI = aigame.AIObservation{
			RequestID:  strings.TrimPrefix(action.Command, "AI:"),
			Items:      append([]aigame.AIInventoryItem(nil), s.items...),
			ItemsKnown: true,
			Received:   true,
		}
		s.snapshot.AIObservationRevision = s.snapshot.Revision
		if s.onStatus != nil {
			s.onStatus(s)
		}
		return nil
	}
	if action.Kind != aigame.ActionItem {
		return nil
	}
	s.uses++
	if s.onUse != nil {
		s.onUse(s)
	}
	if action.TargetID != s.expectedPetSlot+1 || action.Command != "" {
		return errors.New("wrong native pet-use command")
	}
	if s.replaceOnUse {
		for index := range s.snapshot.Pets {
			if s.snapshot.Pets[index].Slot == s.expectedPetSlot {
				s.snapshot.Pets[index].StableID = "replacement-pet"
			}
		}
	}
	if !s.noConsume {
		for index, item := range s.items {
			if item.Slot != action.Index {
				continue
			}
			s.items = append(s.items[:index], s.items[index+1:]...)
			break
		}
	}
	s.snapshot.AI.ItemsKnown = false
	if !s.noHeal {
		amount := s.healAmount
		if amount == 0 {
			amount = 20
		}
		for index := range s.snapshot.Pets {
			pet := &s.snapshot.Pets[index]
			if pet.Slot != s.expectedPetSlot {
				continue
			}
			pet.HP += amount
			if pet.HP > pet.MaxHP {
				pet.HP = pet.MaxHP
			}
			pet.Alive = pet.HP > 0
		}
	}
	if s.uncertain {
		return errors.New("unknown item-use outcome")
	}
	return nil
}

func petItemRecoveryFixture(t *testing.T) (*TravelItemRecovery, *petItemRecoverySession, aigame.PetSnapshot) {
	t.Helper()
	backend, fixture := gameFixture(t)
	pet := aigame.PetSnapshot{
		Slot: 3, StableID: "pet-identity", IdentityKnown: true,
		Name: "Pet", FreeName: "Pet Free", Graphic: 100251,
		Level: 2, HP: 10, MaxHP: 50, Alive: true,
	}
	fixture.snapshot.Position = aigame.Point{Floor: 1, X: 10, Y: 20}
	fixture.snapshot.Player = aigame.PlayerSnapshot{HasStatus: true, HP: 100, MaxHP: 100, Level: 10, BattlePetSlot: 3, BattlePetSlotKnown: true}
	fixture.snapshot.Pets = []aigame.PetSnapshot{pet}
	session := &petItemRecoverySession{
		snapshot: fixture.snapshot, expectedPetSlot: pet.Slot,
		items: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 77}, {Slot: 8, TemplateID: 77}, {Slot: 11, TemplateID: 99}},
	}
	backend.Session = session
	backend.Knowledge = &aiknowledge.Knowledge{Digest: "source"}
	healing := &ItemHealingSkill{
		Backend: backend,
		Contracts: map[string]HealingItemContract{
			"small-meat": {TemplateID: 77, BaseHP: 20, ItemsetSHA256: strings.Repeat("a", 64), SourceFingerprint: "source", Verified: true},
		},
	}
	return &TravelItemRecovery{Healing: healing}, session, pet
}

func (s *petItemRecoverySession) itemActions() []aigame.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]aigame.Action, 0, len(s.actions))
	for _, action := range s.actions {
		if action.Kind == aigame.ActionItem {
			result = append(result, action)
		}
	}
	return result
}

func (s *petItemRecoverySession) itemCount(templateID int32) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.items {
		if item.TemplateID == templateID {
			count++
		}
	}
	return count
}

func (s *petItemRecoverySession) pet() aigame.PetSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pet := range s.snapshot.Pets {
		if pet.Slot == s.expectedPetSlot {
			return pet
		}
	}
	return aigame.PetSnapshot{}
}

func TestPetItemRecoveryUsesPetSlotPlusOneAndConfirmsActualGain(t *testing.T) {
	recovery, session, expected := petItemRecoveryFixture(t)
	session.healAmount = 17 // The reviewed BaseHP is 20; completion uses actual HP.

	if err := recovery.HealPet(context.Background(), expected); err != nil {
		t.Fatalf("HealPet() error = %v", err)
	}
	actions := session.itemActions()
	if session.uses != 1 || len(actions) != 1 {
		t.Fatalf("item uses/actions = %d/%d, want one", session.uses, len(actions))
	}
	if actions[0].Index != 5 || actions[0].TargetID != 4 {
		t.Fatalf("native pet item action = %+v, want slot 5 target 4", actions[0])
	}
	if session.itemCount(77) != 1 || session.pet().HP != 27 {
		t.Fatalf("post-use state: items=%d pet=%+v", session.itemCount(77), session.pet())
	}
}

func TestPetItemRecoveryRejectsReplacementOfStablePet(t *testing.T) {
	recovery, session, expected := petItemRecoveryFixture(t)
	session.replaceOnUse = true

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := recovery.HealPet(ctx, expected); err == nil {
		t.Fatal("HealPet accepted a replaced stable pet")
	}
	if session.uses != 1 {
		t.Fatalf("item uses = %d, want one", session.uses)
	}
}

func TestPetItemRecoveryRejectsChangedSubjectBeforeUse(t *testing.T) {
	for _, mode := range []string{"replacement", "unknown-identity", "selection-changed"} {
		t.Run(mode, func(t *testing.T) {
			recovery, session, expected := petItemRecoveryFixture(t)
			if mode == "selection-changed" {
				session.snapshot.Player.BattlePetSlot = 0
			} else {
				for i := range session.snapshot.Pets {
					pet := &session.snapshot.Pets[i]
					if pet.Slot != expected.Slot {
						continue
					}
					if mode == "replacement" {
						pet.StableID = "replacement-pet"
					} else {
						pet.IdentityKnown = false
						pet.StableID = ""
					}
				}
			}
			if err := recovery.HealPet(context.Background(), expected); err == nil {
				t.Fatal("HealPet accepted a changed or unknown subject")
			}
			if session.uses != 0 || len(session.itemActions()) != 0 || session.itemCount(77) != 2 {
				t.Fatalf("rejected subject consumed an item: uses=%d items=%d", session.uses, session.itemCount(77))
			}
		})
	}
}

func TestPetItemRecoveryUnknownStableIdentityRequiresSameIncarnation(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "enrich-same-pet", false: "reused-slot"}[same], func(t *testing.T) {
			recovery, session, expected := petItemRecoveryFixture(t)
			expected.IdentityKnown, expected.StableID = false, ""
			expected.Identity, expected.IdentityEpoch = "session:pet:3:1", 1
			for i := range session.snapshot.Pets {
				pet := &session.snapshot.Pets[i]
				pet.Identity, pet.IdentityEpoch = expected.Identity, expected.IdentityEpoch
				if !same {
					pet.Identity, pet.IdentityEpoch = "session:pet:3:2", 2
				}
			}
			err := recovery.HealPet(context.Background(), expected)
			if same {
				if err != nil || session.uses != 1 {
					t.Fatalf("same pet enrichment: err=%v uses=%d", err, session.uses)
				}
			} else if err == nil || session.uses != 0 {
				t.Fatalf("reused slot accepted: err=%v uses=%d", err, session.uses)
			}
		})
	}
}

func TestPetItemRecoveryRequiresConsumptionAndHPGain(t *testing.T) {
	for _, mode := range []string{"no-heal", "no-consume"} {
		t.Run(mode, func(t *testing.T) {
			recovery, session, expected := petItemRecoveryFixture(t)
			if mode == "no-heal" {
				session.noHeal = true
			} else {
				session.noConsume = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if err := recovery.HealPet(ctx, expected); err == nil {
				t.Fatal("HealPet accepted an incomplete item outcome")
			}
			if session.uses != 1 {
				t.Fatalf("item uses = %d, want one", session.uses)
			}
		})
	}
}

func TestPetItemRecoveryNeverRetriesUnknownUse(t *testing.T) {
	recovery, session, expected := petItemRecoveryFixture(t)
	session.uncertain = true

	if err := recovery.HealPet(context.Background(), expected); err == nil {
		t.Fatal("HealPet accepted an unknown item-use result")
	}
	if session.uses != 1 || len(session.itemActions()) != 1 {
		t.Fatalf("unknown result was retried: uses=%d actions=%v", session.uses, session.itemActions())
	}
}

func TestPetItemRecoveryMountedRole(t *testing.T) {
	for _, mode := range []string{"mounted", "unknown", "switch-on-refresh", "switch-on-use"} {
		t.Run(mode, func(t *testing.T) {
			recovery, session, expected := petItemRecoveryFixture(t)
			session.snapshot.Player.BattlePetSlot = -1
			session.snapshot.Player.RidePet = expected.Slot
			session.snapshot.Player.RidePetKnown = mode != "unknown"
			switchRole := func(s *petItemRecoverySession) {
				s.snapshot.Player.RidePet = -1
				s.snapshot.Player.BattlePetSlot = expected.Slot
			}
			if mode == "switch-on-refresh" {
				session.onStatus = switchRole
			}
			if mode == "switch-on-use" {
				session.onUse = switchRole
			}
			err := recovery.HealPet(context.Background(), expected)
			if mode == "mounted" {
				if err != nil || session.uses != 1 || session.pet().HP != 30 {
					t.Fatalf("mounted recovery: err=%v uses=%d pet=%+v", err, session.uses, session.pet())
				}
			} else {
				wantUses := 0
				if mode == "switch-on-use" {
					wantUses = 1
				}
				if err == nil || session.uses != wantUses {
					t.Fatalf("role change accepted/retried: err=%v uses=%d", err, session.uses)
				}
			}
		})
	}
}
