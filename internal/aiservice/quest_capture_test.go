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

type captureGame struct {
	*questBattleGame
	mode     string
	queries  []string
	attempts int
}

func (g *captureGame) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if rev != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	if a.Kind == aigame.ActionStatus {
		g.queries = append(g.queries, a.Command)
		g.snapshot.Revision++
		switch {
		case strings.HasPrefix(a.Command, "AI:"):
			g.snapshot.AI.Received = true
			g.snapshot.AI.ItemsKnown = true
			g.snapshot.AI.RequestID = strings.TrimPrefix(a.Command, "AI:")
			g.snapshot.AIObservationRevision = g.snapshot.Revision
			g.snapshot.Capture = nil
			if g.mode == "background-always" || g.mode == "background-once" && len(g.queries) == 1 {
				g.snapshot.AI.RequestID = ""
			}
		case strings.HasPrefix(a.Command, "BCAP:"):
			q := &aigame.CaptureObservation{Active: true, RequestID: strings.TrimPrefix(a.Command, "BCAP:"), Revision: g.snapshot.Revision, Self: 0, Turn: g.snapshot.Battle.Turn, FreeSlots: 4, Targets: []aigame.CaptureTarget{{Slot: 10, SpeciesID: 113, Level: 1, Graphic: 100113, Eligible: true}}}
			switch g.mode {
			case "cost":
				q.Targets[0].RequiredItems = []int32{1810}
			case "stale-request":
				q.RequestID = "old"
			case "old-turn":
				q.Turn--
			case "wrong-graphic":
				q.Targets[0].Graphic++
			case "missing-target":
				q.Targets = nil
			case "no-slot":
				q.FreeSlots = 0
			case "no-species":
				q.Targets[0].SpeciesID = 114
			}
			g.snapshot.Capture = q
		default:
			return errors.New("unexpected query")
		}
		return nil
	}
	if a.Kind == aigame.ActionBattle && strings.HasPrefix(a.Command, "T|") {
		g.attempts++
		if g.mode == "success" || g.mode == "weaken" {
			g.snapshot.Pets = append(g.snapshot.Pets, aigame.PetSnapshot{Slot: 1, IdentityKnown: true, StableID: "new-pet", SpeciesIDKnown: true, SpeciesID: 113, Level: 1})
			g.forever = false
		}
	}
	if g.mode == "weaken" && a.Kind == aigame.ActionBattle && strings.HasPrefix(a.Command, "H|") {
		g.snapshot.Battle.Participants[1].HP = 50
	}
	return g.questBattleGame.ExecuteExpected(ctx, rev, a)
}

func TestQuestCaptureWeakensBeforeUsingBoundedCaptureAttempts(t *testing.T) {
	s, g, a := captureFixture(t, "weaken", false)
	g.forever = true
	g.snapshot.Battle.Participants[1].HP = 100
	g.snapshot.Battle.Participants[1].MaxHP = 100
	a.Arguments = json.RawMessage(`{"species_id":113,"minimum_level":1,"maximum_level":6,"max_attempts":1,"max_turns":3,"weaken_above_percent":50}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Execute(ctx, a); err != nil {
		t.Fatal(err, g.commands)
	}
	if strings.Join(g.commands, ",") != "H|A,T|A,EO" || g.attempts != 1 {
		t.Fatal("weakening used a capture attempt or never attempted capture", g.commands, g.attempts)
	}
}

func TestIdentityRefreshSurvivesBackgroundReplyWithoutAcceptingIt(t *testing.T) {
	for _, mode := range []string{"background-once", "background-always"} {
		s, g, _ := captureFixture(t, mode, false)
		observed, err := refreshOwnIdentity(context.Background(), NewNPCSkill(s.Backend, nil), true)
		if mode == "background-once" {
			if err != nil || len(g.queries) != 2 || observed.AI.RequestID == "" {
				t.Fatal(err, g.queries, observed.AI.RequestID)
			}
		} else if !errors.Is(err, errIdentityReplySuperseded) || len(g.queries) != 3 {
			t.Fatal("unrelated responses accepted or query limit exceeded", err, g.queries)
		}
		if len(g.commands) != 0 {
			t.Fatal(g.commands)
		}
	}
}

func captureFixture(t *testing.T, mode string, pet bool) (*QuestCaptureSkill, *captureGame, automation.Action) {
	s, base, _ := questBattleFixture(t, pet)
	g := &captureGame{questBattleGame: base, mode: mode}
	s.Backend.Session = g
	g.snapshot.Battle.Participants[1].Level = 1
	g.snapshot.Battle.Participants[1].Graphic = 100113
	g.snapshot.Pets = []aigame.PetSnapshot{{Slot: 0, IdentityKnown: true, StableID: "existing", SpeciesIDKnown: true, SpeciesID: 113, Level: 1}}
	return &QuestCaptureSkill{Backend: s.Backend}, g, automation.Action{Skill: "pet.capture", ExpectedRevision: g.snapshot.Revision, Arguments: json.RawMessage(`{"species_id":113,"minimum_level":1,"maximum_level":6,"max_attempts":2,"max_turns":3}`)}
}

func TestQuestCaptureConfirmsNewIdentityAfterNativeResult(t *testing.T) {
	for _, pet := range []bool{false, true} {
		s, g, a := captureFixture(t, "success", pet)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.Execute(ctx, a)
		cancel()
		if err != nil {
			t.Fatal(err, g.commands, g.queries)
		}
		want := "T|A,EO"
		if pet {
			want = "T|A,W|FF|FF,EO"
		}
		if strings.Join(g.commands, ",") != want || g.snapshot.Battle.Active || g.attempts != 1 {
			t.Fatal(g.commands)
		}
	}
}

func TestQuestCaptureNeverTreatsWriteOrExistingPetAsSuccess(t *testing.T) {
	s, g, a := captureFixture(t, "failed", true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Execute(ctx, a); !errors.Is(err, ErrPetNotCaptured) {
		t.Fatal(err)
	}
	if strings.Join(g.commands, ",") != "T|A,W|FF|FF,EO" {
		t.Fatal(g.commands)
	}
}

func TestQuestCaptureRejectsUnsafeOrUncorrelatedQuote(t *testing.T) {
	for _, mode := range []string{"cost", "stale-request", "old-turn", "wrong-graphic", "missing-target", "no-slot"} {
		t.Run(mode, func(t *testing.T) {
			s, g, a := captureFixture(t, mode, false)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if err := s.Execute(ctx, a); err == nil {
				t.Fatal("unsafe quote accepted")
			}
			if len(g.commands) != 0 {
				t.Fatal("wrote despite unsafe quote", g.commands)
			}
		})
	}
}

func TestQuestCaptureDoesNotReplayUncertainWrite(t *testing.T) {
	s, g, a := captureFixture(t, "failed", true)
	g.uncertain = true
	err := s.Execute(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "uncertain") || g.attempts != 1 || len(g.commands) != 1 {
		t.Fatal(err, g.commands)
	}
}

func TestQuestCaptureBoundsAttemptsAndTurns(t *testing.T) {
	for _, turnLimit := range []bool{false, true} {
		s, g, a := captureFixture(t, "failed", false)
		g.forever = true
		if turnLimit {
			a.Arguments = json.RawMessage(`{"species_id":113,"minimum_level":1,"maximum_level":6,"max_attempts":20,"max_turns":3}`)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.Execute(ctx, a)
		cancel()
		wantAttempts := 2
		if turnLimit {
			wantAttempts = 3
		}
		if err == nil || errors.Is(err, context.DeadlineExceeded) || g.attempts != wantAttempts || len(g.commands) > 3 {
			t.Fatal(err, g.commands)
		}
	}
}

func TestQuestCaptureRequiresSoloPvEAndKnownPetIdentities(t *testing.T) {
	for _, mode := range []string{"pvp", "arena", "party", "human", "trade", "stale", "unknown-pet"} {
		t.Run(mode, func(t *testing.T) {
			s, g, a := captureFixture(t, "success", false)
			switch mode {
			case "pvp":
				g.snapshot.Battle.Type = 2
			case "arena":
				g.snapshot.Battle.LadderID = "match"
			case "party":
				g.snapshot.Party = make([]aigame.PartyMember, 2)
			case "human":
				g.snapshot.Battle.Participants[1].Player = true
			case "trade":
				g.snapshot.Trade.Active = true
			case "stale":
				a.ExpectedRevision--
			case "unknown-pet":
				g.snapshot.Pets[0].IdentityKnown = false
			}
			if err := s.Execute(context.Background(), a); err == nil || len(g.commands) != 0 {
				t.Fatal(err, g.commands)
			}
		})
	}
}

func TestQuestCaptureRequiresExplicitSpeciesIncludingZero(t *testing.T) {
	s, _, a := captureFixture(t, "success", false)
	for _, v := range []string{"", `"species_id":null,`, `"species_id":-1,`, `"species_id":0,`} {
		a.Arguments = json.RawMessage(`{` + v + `"minimum_level":1,"maximum_level":6,"max_attempts":2,"max_turns":3}`)
		err := s.ValidateSkill(context.Background(), a)
		if (err == nil) != (v == `"species_id":0,`) {
			t.Fatal(v, err)
		}
	}
}
