package aiservice

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type racingNPCSession struct {
	*dialogueSession
	stale, attempts int
	changed         bool
	staleChoice     int32
}

type staleOtherSkill struct{ calls int }

func (*staleOtherSkill) ValidateSkill(context.Context, automation.Action) error { return nil }
func (s *staleOtherSkill) Execute(context.Context, automation.Action) error {
	s.calls++
	return aigame.ErrStaleRevision
}

func TestQuestDoesNotRetryOtherSkillOrImpersonatedNPC(t *testing.T) {
	for _, name := range []string{"item.use", "npc.dialogue"} {
		backend, _ := gameFixture(t)
		skill := &staleOtherSkill{}
		game := &AutomationGame{Backend: backend, Skills: SkillSet{name: skill}}
		if err := game.Execute(context.Background(), automation.Action{Skill: name}); !errors.Is(err, aigame.ErrStaleRevision) || skill.calls != 1 {
			t.Fatal("non-NPC mutation was retried", err, skill.calls)
		}
	}
}

func (s *racingNPCSession) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionWindow {
		s.attempts++
		if s.stale > 0 && (s.staleChoice == 0 || a.WindowSelect == s.staleChoice) {
			s.stale--
			s.mu.Lock()
			s.snapshot.Revision++
			if s.changed {
				w := *s.snapshot.ActiveWindow
				w.ObjectID++
				s.snapshot.ActiveWindow = &w
			}
			s.mu.Unlock()
			return aigame.ErrStaleRevision
		}
	}
	return s.dialogueSession.ExecuteExpected(ctx, rev, a)
}

func TestQuestNPCStaleFinalChoiceDoesNotRepeatAcknowledgedPages(t *testing.T) {
	skill, base, action := dialogueFixture(t, 32)
	base.pages = []aigame.WindowSnapshot{{Type: 0, Sequence: 100, ObjectID: 42, Open: true, ButtonType: 4}}
	session := &racingNPCSession{dialogueSession: base, stale: 1, staleChoice: 4}
	skill.Backend.Session = session
	game := &AutomationGame{Backend: skill.Backend, Skills: SkillSet{"npc.dialogue": skill}}
	if err := game.Execute(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 2 || session.actions[0].action.WindowSelect != 32 || session.actions[1].action.WindowSelect != 4 {
		t.Fatal("acknowledged page was replayed", session.actions)
	}
}

func TestQuestNPCRefreshesOnlyKnownUnsentConfirmations(t *testing.T) {
	for _, mode := range []string{"stale", "changed-window", "continuous-stale", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			skill, base, action := dialogueFixture(t, 4)
			session := &racingNPCSession{dialogueSession: base, stale: 1}
			if mode == "changed-window" {
				session.changed = true
			}
			if mode == "continuous-stale" {
				session.stale = 10
			}
			if mode == "uncertain" {
				session.stale = 0
				base.err = io.ErrUnexpectedEOF
			}
			skill.Backend.Session = session
			game := &AutomationGame{Backend: skill.Backend, Skills: SkillSet{"npc.dialogue": skill}}
			err := game.Execute(context.Background(), action)
			if mode == "stale" {
				if err != nil || len(session.actions) != 1 || session.attempts != 2 {
					t.Fatal(err, session.actions, session.attempts)
				}
			} else if err == nil {
				t.Fatal("expected refusal")
			}
			if mode == "changed-window" && len(session.actions) != 0 {
				t.Fatal("confirmed a different window")
			}
			if mode == "continuous-stale" && (!errors.Is(err, aigame.ErrStaleRevision) || session.attempts != 3 || len(session.actions) != 0) {
				t.Fatal(err, session.attempts)
			}
			if mode == "uncertain" && (!errors.Is(err, io.ErrUnexpectedEOF) || session.attempts != 1 || len(session.actions) != 1) {
				t.Fatal("replayed unknown write", err, session.attempts)
			}
		})
	}
}
