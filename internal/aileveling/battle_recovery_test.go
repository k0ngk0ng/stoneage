package aileveling

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestLevelingRetreatsWhenCharacterOrPetNeedsRecovery(t *testing.T) {
	for _, mode := range []string{"player_low", "pet_low", "pet_dead", "player_menu_off"} {
		t.Run(mode, func(t *testing.T) {
			game := petTurnGame(false, 0)
			for i := range game.snapshot.Battle.Participants {
				actor := &game.snapshot.Battle.Participants[i]
				if (mode == "player_low" || mode == "player_menu_off") && actor.BattleID == game.snapshot.Battle.MyNo {
					actor.HP = 1
				}
				if (mode == "pet_low" || mode == "pet_dead") && actor.BattleID == game.snapshot.Battle.MyNo+5 {
					actor.HP = 1
					if mode == "pet_dead" {
						actor.HP = 0
						actor.Dead = true
					}
				}
			}
			if mode == "player_menu_off" {
				game.snapshot.Battle.BPFlags = aigame.BattlePlayerMenuOff
			}
			c, _, _ := newCoordinator(t, game, nil)
			r := startCharacter(t, c, StartRequest{})
			checkpoint, err := c.Tick(context.Background(), r.Handle)
			want := "E"
			if mode == "player_menu_off" {
				want = "N"
			}
			if err != nil || checkpoint.Phase != "submitted" || len(game.actions) == 0 || game.actions[0].Command != want {
				t.Fatalf("unsafe turn: %+v %v actions=%+v", checkpoint, err, game.actions)
			}
			if mode != "pet_dead" && (len(game.actions) != 2 || game.actions[1].Command != "W|FF|FF") {
				t.Fatalf("pet attacked during retreat: %+v", game.actions)
			}
			count := len(game.actions)
			if _, err := c.Tick(context.Background(), r.Handle); err != nil || len(game.actions) != count {
				t.Fatalf("pending escape was replayed: %v %+v", err, game.actions)
			}
		})
	}
}

func TestLevelingDoesNotResumeEncountersWithInjuredBattlePet(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Player.BattlePetSlotKnown = true
	game.snapshot.Player.BattlePetSlot = 3
	game.snapshot.Pets = []aigame.PetSnapshot{{Slot: 3, HP: 1, MaxHP: 31}}
	navigationCalls := 0
	c, _, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
		navigationCalls++
		return Navigation{InArea: true}, nil
	}))
	r := startCharacter(t, c, StartRequest{})
	if _, err := c.Tick(context.Background(), r.Handle); err == nil {
		t.Fatal("injured pet accepted without recovery")
	}
	checkpoint, err := store.Load(context.Background(), r.Handle)
	if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "ready" || navigationCalls != 0 || len(game.actions) != 0 {
		t.Fatalf("unsafe restart: %+v %v calls=%d", checkpoint, err, navigationCalls)
	}
}

func TestLevelingRetreatUsesOnlyOwnRosterSlots(t *testing.T) {
	s := battleSnapshot()
	s.Battle.Participants = append(s.Battle.Participants, aigame.BattleParticipant{BattleID: 1, HP: 0, Dead: true}, aigame.BattleParticipant{BattleID: 5, HP: 16, MaxHP: 31})
	if levelingBattleNeedsRecovery(s) {
		t.Fatal("ally death or rounded half HP incorrectly caused retreat")
	}
	s.Battle.Participants[len(s.Battle.Participants)-1].HP = 15
	if !levelingBattleNeedsRecovery(s) {
		t.Fatal("low own pet HP did not cause retreat")
	}
}
