package aileveling

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func petTurnGame(failPet bool, flags int32) *fakeGame {
	game := &fakeGame{snapshot: battleSnapshot()}
	game.snapshot.Battle.BPFlags = flags
	game.snapshot.Battle.BPReceived = true
	game.snapshot.Battle.BCReceived = true
	game.snapshot.Battle.Participants = append(game.snapshot.Battle.Participants, aigame.BattleParticipant{BattleID: 5, HP: 30, MaxHP: 30})
	game.onAction = func(action aigame.Action) {
		game.snapshot.Revision++
		if action.Command == "W|FF|FF" {
			game.snapshot.Battle.PetSubmitted = true
			game.snapshot.Battle.CommandReady = false
			if failPet {
				game.err = errors.New("pet packet delivery uncertain")
			}
		} else {
			game.snapshot.Battle.PlayerSubmitted = true
			game.snapshot.Battle.CommandReady = true
		}
	}
	return game
}

func TestLevelingSubmitsPlayerThenPetOncePerTurn(t *testing.T) {
	for _, flags := range []int32{0, battlePlayerMenuNon, aigame.BattleEnemySurprise} {
		game := petTurnGame(false, flags)
		coordinator, _, _ := newCoordinator(t, game, nil)
		receipt := startCharacter(t, coordinator, StartRequest{})
		checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
		if err != nil || checkpoint.Phase != "submitted" {
			t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
		}
		player := "H|A"
		if flags != 0 {
			player = "N"
		}
		if len(game.actions) != 2 || game.actions[0].Command != player || game.actions[1].Command != "W|FF|FF" {
			t.Fatalf("player/pet order=%+v", game.actions)
		}
		if _, err := coordinator.Tick(context.Background(), receipt.Handle); err != nil {
			t.Fatal(err)
		}
		if len(game.actions) != 2 {
			t.Fatal("pending turn resubmitted player or pet")
		}
	}
}

func TestLevelingNeverReplaysPlayerAfterUncertainPetSubmission(t *testing.T) {
	game := petTurnGame(true, 0)
	coordinator, _, store := newCoordinator(t, game, nil)
	receipt := startCharacter(t, coordinator, StartRequest{})
	_, err := coordinator.Tick(context.Background(), receipt.Handle)
	if !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("uncertain pet error=%v", err)
	}
	checkpoint, err := store.Load(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "prepared" {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	_, _ = coordinator.Tick(context.Background(), receipt.Handle)
	if len(game.actions) != 2 {
		t.Fatalf("uncertain turn was replayed: %+v", game.actions)
	}
}
