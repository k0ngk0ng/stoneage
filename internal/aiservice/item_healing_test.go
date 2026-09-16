package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type itemHealingSession struct {
	staleRequest bool
	*refreshTestSession
	bag                          []aigame.AIInventoryItem
	uses                         int
	noHeal, noConsume, uncertain bool
	healAmount                   int32
}

func (s *itemHealingSession) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	if err := s.refreshTestSession.ExecuteExpected(ctx, revision, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Kind == aigame.ActionStatus && strings.HasPrefix(a.Command, "AI:") {
		s.snapshot.Revision++
		s.snapshot.AIObservationRevision = s.snapshot.Revision
		s.snapshot.AI = aigame.AIObservation{RequestID: strings.TrimPrefix(a.Command, "AI:"), Received: true, ItemsKnown: true, Items: append([]aigame.AIInventoryItem(nil), s.bag...)}
		if s.staleRequest {
			s.snapshot.AI.RequestID = "0000000000000000"
		}
	}
	if a.Kind == aigame.ActionItem {
		s.uses++
		if len(s.bag) == 0 || a.Index != s.bag[0].Slot || a.TargetID != 0 || a.Command != "" {
			return errors.New("wrong native self-use command")
		}
		if s.uncertain {
			return errors.New("unknown item use outcome")
		}
		if !s.noConsume {
			s.bag = append([]aigame.AIInventoryItem(nil), s.bag[1:]...)
		}
		s.snapshot.AI.ItemsKnown = false
		if !s.noHeal {
			amount := s.healAmount
			if amount == 0 {
				amount = 20
			}
			s.snapshot.Player.HP += amount
			if s.snapshot.Player.HP > s.snapshot.Player.MaxHP {
				s.snapshot.Player.HP = s.snapshot.Player.MaxHP
			}
		}
	}
	return nil
}

func itemHealingFixture(t *testing.T) (*ItemHealingSkill, *itemHealingSession, automation.Action) {
	t.Helper()
	b, f := gameFixture(t)
	f.snapshot.Player = aigame.PlayerSnapshot{HasStatus: true, HP: 1, MaxHP: 29, Level: 1}
	session := &itemHealingSession{refreshTestSession: &refreshTestSession{snapshot: f.snapshot}, bag: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 77}, {Slot: 7, TemplateID: 77}}}
	b.Session = session
	b.Knowledge = &aiknowledge.Knowledge{Digest: "source"}
	skill := &ItemHealingSkill{Backend: b, Contracts: map[string]HealingItemContract{"food": {TemplateID: 77, BaseHP: 20, Verified: true, SourceFingerprint: "source"}}}
	return skill, session, automation.Action{Skill: "item.heal", ExpectedRevision: 12, Arguments: json.RawMessage(`{"item":"food"}`)}
}

func TestItemHealingRequiresBothConsumptionAndHPRecovery(t *testing.T) {
	for _, mode := range []string{"success", "hp-only", "consumed-only", "cap-at-max", "random-low", "random-high"} {
		s, game, a := itemHealingFixture(t)
		switch mode {
		case "random-low":
			game.healAmount = 18
		case "random-high":
			game.healAmount = 22
		case "hp-only":
			game.noConsume = true
		case "consumed-only":
			game.noHeal = true
		case "cap-at-max":
			game.snapshot.Player.HP = 28
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err := s.Execute(ctx, a)
		cancel()
		wantSuccess := mode == "success" || mode == "cap-at-max" || mode == "random-low" || mode == "random-high"
		if wantSuccess && err != nil || !wantSuccess && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s err=%v", mode, err)
		}
		if game.uses != 1 || len(game.actions) != 3 {
			t.Fatalf("%s uses=%d actions=%v", mode, game.uses, game.actions)
		}
	}
}

func TestItemHealingRejectsUnknownTemplateAndStaleContract(t *testing.T) {
	for _, mode := range []string{"missing", "other-item", "unverified", "wrong-source"} {
		s, game, a := itemHealingFixture(t)
		switch mode {
		case "missing":
			game.bag = nil
		case "other-item":
			game.bag = []aigame.AIInventoryItem{{Slot: 5, TemplateID: 4}}
		case "unverified":
			c := s.Contracts["food"]
			c.Verified = false
			s.Contracts["food"] = c
		case "wrong-source":
			c := s.Contracts["food"]
			c.SourceFingerprint = "other"
			s.Contracts["food"] = c
		}
		if err := s.Execute(context.Background(), a); !errors.Is(err, ErrHealingItemUnavailable) || game.uses != 0 {
			t.Fatalf("%s err=%v uses=%d", mode, err, game.uses)
		}
	}
}

func TestItemHealingNeverRetriesUnknownUse(t *testing.T) {
	s, game, a := itemHealingFixture(t)
	game.uncertain = true
	if err := s.Execute(context.Background(), a); err == nil || game.uses != 1 || len(game.actions) != 2 {
		t.Fatalf("err=%v uses=%d actions=%d", err, game.uses, len(game.actions))
	}
}

func TestItemHealingAtFullHPDoesNotConsumeOrQuery(t *testing.T) {
	s, game, a := itemHealingFixture(t)
	game.snapshot.Player.HP = 29
	if err := s.Execute(context.Background(), a); err != nil || len(game.actions) != 0 {
		t.Fatalf("err=%v actions=%d", err, len(game.actions))
	}
}

func TestItemHealingDoesNotConsumeFromUncorrelatedInventory(t *testing.T) {
	s, game, a := itemHealingFixture(t)
	game.staleRequest = true
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := s.Execute(ctx, a); !errors.Is(err, context.DeadlineExceeded) || game.uses != 0 || len(game.actions) != 1 {
		t.Fatalf("err=%v uses=%d actions=%d", err, game.uses, len(game.actions))
	}
}
