package aiservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

type passageGame struct {
	*crossMapGame
	actions   []aigame.Action
	uncertain bool
}

func (g *passageGame) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionMove || a.Kind == aigame.ActionStatus {
		return g.crossMapGame.ExecuteExpected(ctx, rev, a)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if rev != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	g.actions = append(g.actions, a)
	switch a.Kind {
	case aigame.ActionLook:
		g.snapshot.Position.Direction = a.Direction
	case aigame.ActionTalk:
		g.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 281, ObjectID: 7, ButtonType: 12, Open: true}
	case aigame.ActionWindow:
		if g.uncertain {
			return errors.New("unknown challenge write")
		}
		w := *g.snapshot.ActiveWindow
		w.Submitted = true
		g.snapshot.ActiveWindow = &w
		g.snapshot.Phase = aigame.PhaseBattle
		g.snapshot.Battle = aigame.BattleSnapshot{Active: true, Type: 6, MyNo: 0, MyNoKnown: true, BPReceived: true, BCReceived: true, CommandReady: true, Participants: []aigame.BattleParticipant{{BattleID: 0, HP: 100, Player: true}, {BattleID: 10, HP: 10}}}
	case aigame.ActionBattle:
		g.snapshot.Battle.Result = "-2|0|1,,,,,"
	case aigame.ActionBattleEnd:
		g.snapshot.Battle.Active = false
		g.snapshot.Battle.Ended = true
		g.snapshot.Battle.LastCommand = "EO"
		g.snapshot.Phase = aigame.PhaseWorld
		g.snapshot.Actors[0].Graphic = 0
	default:
		return errors.New("unexpected guard action")
	}
	g.snapshot.Revision++
	return ctx.Err()
}

func passageFixture(t *testing.T) (*MovementSkill, *passageGame, NPCSpec) {
	b, original := safeTravelGame(t, 10, 0, 0, 40)
	original.snapshot.Player.MaxHP, original.snapshot.Player.HP = 100, 100
	original.snapshot.Actors = []aigame.ActorSnapshot{{ID: 7, Kind: "character", CharType: 20, Name: "guard", X: 2, Graphic: 100000, GraphicKnown: true}}
	g := &passageGame{crossMapGame: original}
	b.Session = g
	b.Knowledge = &aiknowledge.Knowledge{Digest: "passage-test"}
	spec := NPCSpec{Alias: "guard", Name: "guard", Floor: 10, X: 2, TalkRange: 1, SourceFingerprint: "passage-test", Verified: true,
		Windows: []NPCWindowSpec{{Type: 0, Sequence: 281, WindowObjectFromActor: true, Choices: map[int]NPCChoice{4: {Button: 4}}}},
		Passage: &NPCPassage{WindowSequence: 281, Choice: 4, MaxTurns: 10, MinimumLevel: 40, MinimumHPPercent: 75}}
	s := &MovementSkill{Backend: b, Navigator: safeTravelTiles(t, safeTravelFloor(10, 5, 1)), NPCs: MustNPCRegistry([]NPCSpec{spec})}
	return s, g, spec
}

func TestMovementOpensReviewedPassageAndContinues(t *testing.T) {
	s, g, _ := passageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Execute(ctx, crossMapAction(12, 10, 4, 0)); err != nil {
		t.Fatal(err)
	}
	if g.snapshot.Position.X != 4 || g.snapshot.Battle.Active || g.snapshot.Actors[0].Graphic != 0 {
		t.Fatal(g.snapshot.Position, g.snapshot.Battle)
	}
	var challenge, attack, end int
	for _, a := range g.actions {
		switch a.Kind {
		case aigame.ActionWindow:
			challenge++
		case aigame.ActionBattle:
			attack++
		case aigame.ActionBattleEnd:
			end++
		}
	}
	if challenge != 1 || attack != 1 || end != 1 {
		t.Fatal(g.actions)
	}
}

func TestMovementPassageNeverChallengesUnreviewedOrUnready(t *testing.T) {
	for _, mode := range []string{"no-contract", "mismatched-data", "player", "unknown-graphic", "party", "trade", "level", "hp"} {
		t.Run(mode, func(t *testing.T) {
			s, g, spec := passageFixture(t)
			switch mode {
			case "no-contract":
				spec.Passage = nil
				s.NPCs = MustNPCRegistry([]NPCSpec{spec})
			case "mismatched-data":
				s.Backend.Knowledge.Digest = "other"
			case "player":
				g.snapshot.Actors[0].CharType = 1
			case "unknown-graphic":
				g.snapshot.Actors[0].GraphicKnown = false
			case "party":
				g.snapshot.Party = make([]aigame.PartyMember, 2)
			case "trade":
				g.snapshot.Trade.Active = true
			case "level":
				g.snapshot.Player.Level = 1
			case "hp":
				g.snapshot.Player.HP = 50
			}
			if err := s.Execute(context.Background(), crossMapAction(12, 10, 4, 0)); err == nil {
				t.Fatal("unexpected passage")
			}
			for _, a := range g.actions {
				if a.Kind == aigame.ActionWindow || a.Kind == aigame.ActionBattle {
					t.Fatal("unapproved battle", g.actions)
				}
			}
		})
	}
}

func TestMovementPassageDoesNotReplayUnknownChallenge(t *testing.T) {
	s, g, _ := passageFixture(t)
	g.uncertain = true
	if err := s.Execute(context.Background(), crossMapAction(12, 10, 4, 0)); err == nil {
		t.Fatal("unknown write accepted")
	}
	count := 0
	for _, a := range g.actions {
		if a.Kind == aigame.ActionWindow {
			count++
		}
	}
	if count != 1 {
		t.Fatal(g.actions)
	}
}

func TestMovementDetoursInsteadOfChallengingPassageWhenPossible(t *testing.T) {
	s, g, _ := passageFixture(t)
	s.Navigator = safeTravelTiles(t, safeTravelFloor(10, 5, 3))
	if err := s.Execute(context.Background(), crossMapAction(12, 10, 4, 0)); err != nil {
		t.Fatal(err)
	}
	if len(g.actions) != 0 || g.snapshot.Actors[0].Graphic == 0 || g.snapshot.Position.X != 4 {
		t.Fatal("unnecessary guard battle", g.actions, g.snapshot.Position)
	}
}

func TestPassageChallengeRechecksPartyAtWindowBoundary(t *testing.T) {
	s, g, spec := passageFixture(t)
	g.snapshot.Position.X = 1
	g.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 281, ObjectID: 7, ButtonType: 12, Open: true}
	g.snapshot.Party = make([]aigame.PartyMember, 2)
	npc := NewNPCSkill(s.Backend, s.NPCs)
	args := npcWindowArguments{NPC: spec.Alias, WindowSequence: []byte("281"), Choice: []byte("4")}
	if err := npc.executeWindow(context.Background(), g.snapshot.Revision, 0, spec, args); err == nil || len(g.actions) != 0 {
		t.Fatal("party joined after approach but challenge was sent", err, g.actions)
	}
}

func TestNPCPassageContractIsCopiedAndRequiresFreeActorBoundChallenge(t *testing.T) {
	_, _, spec := passageFixture(t)
	registry := MustNPCRegistry([]NPCSpec{spec})
	spec.Passage.MaxTurns = 0
	if got, ok := registry.Lookup("guard"); !ok || got.Passage.MaxTurns != 10 {
		t.Fatal(got, ok)
	}
	for _, mode := range []string{"turns", "paid", "unbound", "button", "hp"} {
		_, _, source := passageFixture(t)
		switch mode {
		case "turns":
			source.Passage.MaxTurns = 0
		case "paid":
			source.Windows[0].Choices[4] = NPCChoice{Button: 4, MaximumCost: 1}
		case "unbound":
			source.Windows[0].WindowObjectFromActor = false
		case "button":
			source.Passage.Choice = 1
		case "hp":
			source.Passage.MinimumHPPercent = 0
		}
		if _, err := NewNPCRegistry([]NPCSpec{source}); err == nil {
			t.Fatal(mode)
		}
	}
}

var _ GameSession = (*passageGame)(nil)
