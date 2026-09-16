package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type healerSession struct {
	*npcSkillSession
	confirmations                             int
	noHP, uncertain, wrongActor, changedLevel bool
	confirmationReads                         int
	beforeConfirm                             func(*aigame.Snapshot)
}

func (s *healerSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.ActiveWindow != nil && s.snapshot.ActiveWindow.Sequence == 221 {
		s.confirmationReads++
		if s.confirmationReads == 2 && s.beforeConfirm != nil {
			s.beforeConfirm(&s.snapshot)
		}
	}
	return s.snapshot, nil
}

func (s *healerSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := s.npcSkillSession.ExecuteExpected(ctx, revision, action); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case action.Kind == aigame.ActionTalk:
		s.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 2, Sequence: 220, ObjectID: 42, Open: true, ButtonType: 2}
	case action.Kind == aigame.ActionWindow && action.WindowSequence == 220:
		if action.Text != "2" {
			return errors.New("incorrect native healer HP selection")
		}
		s.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 221, ObjectID: 42, Open: true, ButtonType: 12}
		if s.wrongActor {
			s.snapshot.ActiveWindow.ObjectID = 99
		}
		if s.changedLevel {
			s.snapshot.Player.Level++
		}
	case action.Kind == aigame.ActionWindow && action.WindowSequence == 221:
		if action.WindowSelect != 4 {
			return errors.New("incorrect healer confirmation")
		}
		s.confirmations++
		if s.uncertain {
			return errors.New("unknown healer write outcome")
		}
		s.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 222, ObjectID: 42, Open: true, ButtonType: 1}
		if !s.noHP {
			s.snapshot.Player.HP = s.snapshot.Player.MaxHP
		}
	}
	return nil
}

func healerFixture(t *testing.T) (*HealerSkill, *healerSession, automation.Action) {
	t.Helper()
	npc, session, spec := npcSkillFixture(t, true)
	session.snapshot.Position.Direction = 2
	session.snapshot.Player = aigame.PlayerSnapshot{HasStatus: true, HP: 1, MaxHP: 29, Level: 3, Gold: 50}
	game := &healerSession{npcSkillSession: session}
	npc.Backend.Session = game
	heal := &HealerSkill{Backend: npc.Backend, Contracts: map[string]HealerContract{spec.Alias: {NPC: spec, PaidFromLevel: 1, HPRateMilli: 500}}}
	return heal, game, automation.Action{Skill: "npc.heal", ExpectedRevision: 12, MaximumCost: 1, Arguments: json.RawMessage(`{"npc":"trainer"}`)}
}

func TestHealerNativeFlowRequiresObservedHP(t *testing.T) {
	for _, noHP := range []bool{false, true} {
		skill, session, action := healerFixture(t)
		session.noHP = noHP
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := skill.Execute(ctx, action)
		cancel()
		if noHP && !errors.Is(err, context.DeadlineExceeded) || !noHP && err != nil {
			t.Fatalf("noHP=%v err=%v", noHP, err)
		}
		if session.confirmations != 1 {
			t.Fatalf("confirmations=%d", session.confirmations)
		}
	}
}

func TestHealerRejectsChangedQuoteIdentityAndUnfundedCharge(t *testing.T) {
	for _, name := range []string{"wrong-actor", "changed-level", "unfunded", "over-budget", "unverified", "unknown-status"} {
		t.Run(name, func(t *testing.T) {
			skill, session, action := healerFixture(t)
			switch name {
			case "wrong-actor":
				session.wrongActor = true
			case "changed-level":
				session.changedLevel = true
			case "unfunded":
				session.snapshot.Player.Gold = 0
			case "over-budget":
				action.MaximumCost = 0
			case "unverified":
				c := skill.Contracts["trainer"]
				c.NPC.Verified = false
				skill.Contracts["trainer"] = c
			case "unknown-status":
				session.snapshot.Player.HasStatus = false
			}
			if err := skill.Execute(context.Background(), action); err == nil || session.confirmations != 0 {
				t.Fatalf("err=%v confirmations=%d", err, session.confirmations)
			}
		})
	}
}

func TestHealerUnknownConfirmationIsNotRetried(t *testing.T) {
	skill, session, action := healerFixture(t)
	session.uncertain = true
	if err := skill.Execute(context.Background(), action); err == nil || session.confirmations != 1 {
		t.Fatalf("err=%v confirmations=%d", err, session.confirmations)
	}
}

func TestHealerRevalidatesStatsInsideFinalWriteGate(t *testing.T) {
	for _, edit := range []func(*aigame.Snapshot){
		func(s *aigame.Snapshot) { s.Player.Level++ },
		func(s *aigame.Snapshot) { s.Player.MaxHP++ },
		func(s *aigame.Snapshot) { s.Position.Y-- },
	} {
		skill, session, action := healerFixture(t)
		// Levels 2 and 3 have the same truncated quote. Validate the full
		// contract at the write boundary even when the quote still matches.
		session.snapshot.Player.Level = 2
		session.beforeConfirm = edit
		if err := skill.Execute(context.Background(), action); err == nil || session.confirmations != 0 {
			t.Fatalf("err=%v confirmations=%d", err, session.confirmations)
		}
	}
}

func TestHealerUsesCurrentServerFundingAuthorization(t *testing.T) {
	skill, session, action := healerFixture(t)
	session.snapshot.Player.Gold = 0
	skill.Backend.Funding = func(context.Context) (bool, error) { return true, nil }
	if err := skill.Execute(context.Background(), action); err != nil || session.confirmations != 1 {
		t.Fatalf("err=%v confirmations=%d", err, session.confirmations)
	}
}

func TestHealerQuoteMatchesNativeThresholdAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		level, threshold, rate int32
		want                   int64
	}{
		{1, 10, 500, 0}, {10, 10, 500, 5}, {3, 1, 500, 1}, {1, 1, 500, 1}, {1, 1, 4000, 4},
	} {
		c := HealerContract{PaidFromLevel: tc.threshold, HPRateMilli: tc.rate}
		got, err := c.hpQuote(aigame.PlayerSnapshot{HasStatus: true, Level: tc.level, HP: 1, MaxHP: 29})
		if err != nil || got != tc.want {
			t.Fatalf("%+v quote=%d err=%v", tc, got, err)
		}
	}
}
