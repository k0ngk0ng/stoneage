package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type collectionRecoveryFunc func(context.Context) error

func (f collectionRecoveryFunc) Heal(ctx context.Context) error { return f(ctx) }

func TestCollectionRequiresExplicitSpeciesIncludingZero(t *testing.T) {
	for _, species := range []string{`"species_id":0,`, `"species_id":null,`, ``} {
		a := automation.Action{Skill: "pet.collect", Arguments: json.RawMessage(`{"targets":[{` + species + `"minimum_level":1,"maximum_level":6,"count":1}],"max_encounters":2,"max_moves":10,"max_attempts":1,"max_turns":2}`)}
		if _, err := collectionArguments(a); (err == nil) != (species == `"species_id":0,`) {
			t.Fatal(species, err)
		}
	}
}

func TestCollectionRecoveryCommitsBeforeConsumptionAndBoundsAcrossResumes(t *testing.T) {
	st := petCollectionState{Phase: "idle", Protected: []string{"original"}, Collected: []collectionPet{{ID: "caught", SpeciesID: 1}}}
	var saved []petCollectionState
	s := &QuestPetCollectionSkill{Movement: &MovementSkill{}}
	used := 0
	s.Movement.HealthRecovery = collectionRecoveryFunc(func(context.Context) error {
		if len(saved) == 0 || saved[len(saved)-1].Phase != "healing" || saved[len(saved)-1].Heals != used+1 {
			t.Fatal("item use preceded durable boundary", saved)
		}
		used++
		return nil
	})
	save := func(confirmed bool) error {
		if confirmed {
			t.Fatal("healing cannot confirm pet collection")
		}
		saved = append(saved, st)
		return nil
	}
	for i := 0; i < 32; i++ {
		if err := s.recoverPlayer(context.Background(), &st, save); err != nil || st.Phase != "idle" {
			t.Fatal(err, st)
		}
	}
	if err := s.recoverPlayer(context.Background(), &st, save); !errors.Is(err, ErrTravelHealingLimit) || used != 32 {
		t.Fatal("recovery limit bypassed", err, used)
	}
	if len(st.Collected) != 1 || st.Collected[0].ID != "caught" || st.Protected[0] != "original" {
		t.Fatal("recovery changed pet ownership", st)
	}
}

func TestCollectionRecoveryPreservesUncertainBoundary(t *testing.T) {
	for _, mode := range []string{"missing", "uncertain", "save-failed", "confirmation-save-failed"} {
		t.Run(mode, func(t *testing.T) {
			st := petCollectionState{Phase: "idle"}
			calls, saves := 0, 0
			s := &QuestPetCollectionSkill{Movement: &MovementSkill{HealthRecovery: collectionRecoveryFunc(func(context.Context) error {
				calls++
				if mode == "missing" {
					return ErrHealingItemUnavailable
				}
				if mode == "uncertain" {
					return errors.New("lost item-use reply")
				}
				return nil
			})}}
			lastDurable := "idle"
			err := s.recoverPlayer(context.Background(), &st, func(bool) error {
				saves++
				if mode == "save-failed" || mode == "confirmation-save-failed" && saves == 2 {
					return errors.New("disk failure")
				}
				lastDurable = st.Phase
				return nil
			})
			if err == nil {
				t.Fatal("failure lost")
			}
			if mode == "save-failed" && calls != 0 {
				t.Fatal("used an item without checkpoint")
			}
			if (mode == "uncertain" || mode == "confirmation-save-failed") && lastDurable != "healing" {
				t.Fatal("unknown item use would become replayable", lastDurable)
			}
			if mode == "missing" && (lastDurable != "idle" || !errors.Is(err, ErrHealingItemUnavailable)) {
				t.Fatal("missing supplies should permit replenishment and resume", lastDurable, err)
			}
		})
	}
}
