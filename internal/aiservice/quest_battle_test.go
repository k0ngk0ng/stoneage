package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type questBattleGame struct {
	*fakeGame
	commands           []string
	uncertain, forever bool
}

func (g *questBattleGame) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	if revision != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	if a.Kind == aigame.ActionBattleEnd {
		if g.snapshot.Battle.Result == "" && !g.snapshot.Battle.MySideDefeated() {
			return errors.New("premature EO")
		}
		g.commands = append(g.commands, "EO")
		g.snapshot.Battle.Active = false
		g.snapshot.Battle.Ended = true
		g.snapshot.Battle.LastCommand = "EO"
		g.snapshot.Phase = aigame.PhaseWorld
	} else if a.Kind == aigame.ActionBattle {
		g.commands = append(g.commands, a.Command)
		if g.uncertain {
			return errors.New("uncertain write")
		}
		if strings.HasPrefix(a.Command, "W|") {
			g.snapshot.Battle.PetSubmitted = true
		} else {
			g.snapshot.Battle.PlayerSubmitted = true
		}
		if g.snapshot.Battle.PlayerSubmitted && (!g.snapshot.Battle.HasActivePet() || g.snapshot.Battle.PetSubmitted) {
			if g.forever {
				g.snapshot.Battle.Turn++
				g.snapshot.Battle.PlayerSubmitted = false
				g.snapshot.Battle.PetSubmitted = false
			} else {
				g.snapshot.Battle.Result = "-2|0|1,,,,,"
			}
		}
	} else {
		return errors.New("unexpected world mutation")
	}
	g.snapshot.Revision++
	return nil
}
func questBattleFixture(t *testing.T, pet bool) (*QuestBattleSkill, *questBattleGame, automation.Action) {
	r, base := travelBattleFixture(t, pet)
	g := &questBattleGame{fakeGame: base.fakeGame}
	r.Backend.Session = g
	return &QuestBattleSkill{Backend: r.Backend}, g, automation.Action{Skill: "battle.finish", Arguments: json.RawMessage(`{"max_turns":2}`), ExpectedRevision: g.snapshot.Revision}
}
func TestQuestBattleFinishesPlayerPetAndNativeResult(t *testing.T) {
	for _, pet := range []bool{false, true} {
		s, g, a := questBattleFixture(t, pet)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.Execute(ctx, a)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if pet {
			want = 3
		}
		if len(g.commands) != want || g.commands[0] != "H|A" || g.commands[len(g.commands)-1] != "EO" || g.snapshot.Battle.Active {
			t.Fatal(g.commands)
		}
	}
}
func TestQuestBattleRejectsPvPPartyAndUnexpectedBattle(t *testing.T) {
	for _, mode := range []string{"pvp", "arena", "party", "human", "not-started", "stale"} {
		t.Run(mode, func(t *testing.T) {
			s, g, a := questBattleFixture(t, false)
			switch mode {
			case "pvp":
				g.snapshot.Battle.Type = 2
			case "arena":
				g.snapshot.Battle.LadderID = "m"
			case "party":
				g.snapshot.Party = make([]aigame.PartyMember, 2)
			case "human":
				g.snapshot.Battle.Participants[1].Player = true
			case "not-started":
				g.snapshot.Battle.Active = false
			case "stale":
				a.ExpectedRevision--
			}
			if err := s.Execute(context.Background(), a); err == nil || len(g.commands) != 0 {
				t.Fatal(err, g.commands)
			}
		})
	}
}

func TestQuestBattleNativeEncounterTypes(t *testing.T) {
	for _, typ := range []int32{0, 1, 2, 3, 4, 5, 6, 7} {
		s, g, a := questBattleFixture(t, false)
		g.snapshot.Battle.Type = typ
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.Execute(ctx, a)
		cancel()
		allowed := typ == 1 || typ == 5 || typ == 6
		if allowed && (err != nil || len(g.commands) != 2 || g.commands[1] != "EO") {
			t.Fatalf("PvE type %d: commands=%v err=%v", typ, g.commands, err)
		}
		if !allowed && (err == nil || len(g.commands) != 0) {
			t.Fatalf("non-PvE type %d: commands=%v err=%v", typ, g.commands, err)
		}
	}
}
func TestQuestBattleDoesNotRetryUncertainCommandOrExceedLimit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		s, g, a := questBattleFixture(t, false)
		g.uncertain = uncertain
		g.forever = !uncertain
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.Execute(ctx, a)
		cancel()
		if err == nil {
			t.Fatal("missing failure")
		}
		want := 2
		if uncertain {
			want = 1
		}
		if len(g.commands) != want {
			t.Fatal(g.commands)
		}
	}
}
func TestQuestBattleEscapeDoesNotSatisfyTask(t *testing.T) {
	s, g, a := questBattleFixture(t, false)
	g.snapshot.Battle.Result = "escaped"
	if err := s.Execute(context.Background(), a); err == nil {
		t.Fatal("escape reported as success")
	}
	if len(g.commands) != 1 || g.commands[0] != "EO" {
		t.Fatal(g.commands)
	}
}
