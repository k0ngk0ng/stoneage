package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type travelBattleGame struct {
	*fakeGame
	commands    []string
	failEscape  bool
	neverEscape bool
}

func TestMovementRecoveryDoesNotReplayUnknownMove(t *testing.T) {
	for _, writeError := range []bool{false, true} {
		skill, game := movementFixture(t, false)
		if writeError {
			game.moveError = errors.New("uncertain W write")
		}
		calls := 0
		skill.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error { calls++; return nil })
		err := skill.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
		if err == nil || game.moves != 1 || calls != 0 {
			t.Fatalf("moves=%d recovery=%d err=%v", game.moves, calls, err)
		}
	}
}

func TestMovementAtDestinationStillWaitsForBattleRecovery(t *testing.T) {
	backend, fake := gameFixture(t)
	fake.snapshot.Position = aigame.Point{Floor: 10, X: 4}
	fake.snapshot.Phase = aigame.PhaseBattle
	fake.snapshot.Battle.Active = true
	skill := &MovementSkill{Backend: backend, Navigator: crossMapNavigator{}}
	calls := 0
	skill.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error {
		calls++
		fake.snapshot.Phase = aigame.PhaseWorld
		fake.snapshot.Battle.Active = false
		fake.snapshot.Revision++
		return nil
	})
	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 0)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || fake.writes != 0 {
		t.Fatalf("recovery=%d writes=%d", calls, fake.writes)
	}
}

func (g *travelBattleGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if revision != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	if action.Kind == aigame.ActionBattleEnd {
		if g.snapshot.Battle.Result != "escaped" {
			return errors.New("EO before successful escape")
		}
		g.commands = append(g.commands, "EO")
		g.snapshot.Battle.Active = false
		g.snapshot.Battle.Ended = true
		g.snapshot.Battle.LastCommand = "EO"
		g.snapshot.Phase = aigame.PhaseWorld
	} else if action.Kind == aigame.ActionBattle {
		g.commands = append(g.commands, action.Command)
		if g.failEscape {
			return errors.New("uncertain battle write")
		}
		if g.neverEscape {
			g.snapshot.Battle.Turn++
		} else if action.Command == "E" || action.Command == "N" {
			g.snapshot.Battle.PlayerSubmitted = true
			if !g.snapshot.Battle.HasActivePet() {
				g.snapshot.Battle.Result = "escaped"
			}
		} else if action.Command == "W|FF|FF" {
			g.snapshot.Battle.PetSubmitted = true
			g.snapshot.Battle.Result = "escaped"
		}
	} else {
		return errors.New("unexpected travel recovery action")
	}
	g.snapshot.Revision++
	return nil
}

func travelBattleFixture(t *testing.T, pet bool) (*TravelBattle, *travelBattleGame) {
	t.Helper()
	backend, fake := gameFixture(t)
	fake.snapshot.Phase = aigame.PhaseBattle
	fake.snapshot.Battle = aigame.BattleSnapshot{Active: true, Type: 1, MyNo: 0, MyNoKnown: true, Turn: 1,
		BPReceived: true, BCReceived: true, CommandReady: true,
		Participants: []aigame.BattleParticipant{{BattleID: 0, HP: 10}, {BattleID: 10, HP: 10}}}
	if pet {
		fake.snapshot.Battle.Participants = append(fake.snapshot.Battle.Participants, aigame.BattleParticipant{BattleID: 5, HP: 10})
	}
	game := &travelBattleGame{fakeGame: fake}
	backend.Session = game
	return &TravelBattle{Backend: backend}, game
}

func TestTravelEscapeCompletesNativePlayerPetAndTerminalSequence(t *testing.T) {
	for _, pet := range []bool{false, true} {
		recovery, game := travelBattleFixture(t, pet)
		if err := recovery.Escape(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := []string{"E", "EO"}
		if pet {
			want = []string{"E", "W|FF|FF", "EO"}
		}
		if !reflect.DeepEqual(game.commands, want) || game.snapshot.Battle.Active {
			t.Fatalf("sequence=%v want=%v", game.commands, want)
		}
	}
}

func TestTravelEscapeNeverUsesEOForFailedOrUncertainEscape(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		recovery, game := travelBattleFixture(t, false)
		game.failEscape, game.neverEscape = uncertain, !uncertain
		err := recovery.Escape(context.Background())
		if err == nil {
			t.Fatal("failed escape became success")
		}
		want := 5
		if uncertain {
			want = 1
		}
		if len(game.commands) != want {
			t.Fatalf("attempts=%v", game.commands)
		}
		for _, command := range game.commands {
			if command != "E" {
				t.Fatalf("failed escape forced terminal action: %v", game.commands)
			}
		}
	}
}

func TestTravelEscapeRefusesDuelPartyDeadAndCancelledActions(t *testing.T) {
	for _, kind := range []string{"duel", "party", "allied-player", "dead", "cancelled", "binding"} {
		t.Run(kind, func(t *testing.T) {
			recovery, game := travelBattleFixture(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "duel":
				game.snapshot.Battle.Type = 2
			case "party":
				game.snapshot.Party = make([]aigame.PartyMember, 2)
			case "allied-player":
				game.snapshot.Battle.Participants = append(game.snapshot.Battle.Participants, aigame.BattleParticipant{BattleID: 1, HP: 10, Player: true})
			case "dead":
				game.snapshot.Battle.Participants[0].Dead = true
			case "cancelled":
				cancel()
			case "binding":
				game.snapshot.Character = "someone-else"
			}
			if err := recovery.Escape(ctx); err == nil || len(game.commands) != 0 {
				t.Fatalf("commands=%v err=%v", game.commands, err)
			}
		})
	}
}
