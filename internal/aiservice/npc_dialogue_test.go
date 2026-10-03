package aiservice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type dialogueSession struct {
	*npcSkillSession
	pages []aigame.WindowSnapshot
}

func (s *dialogueSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := s.npcSkillSession.ExecuteExpected(ctx, revision, action); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if action.Kind == aigame.ActionWindow {
		previous := *s.snapshot.ActiveWindow
		previous.Submitted = true
		s.snapshot.ActiveWindow = &previous
		if action.WindowSelect == npcNextPage && len(s.pages) > 0 {
			next := s.pages[0]
			s.pages = s.pages[1:]
			s.snapshot.ActiveWindow = &next
		}
	}
	return nil
}

func dialogueFixture(t *testing.T, buttons int32) (*NPCSkill, *dialogueSession, automation.Action) {
	t.Helper()
	skill, base, spec := npcSkillFixture(t, true)
	spec.WindowType = 0
	spec.Choices = map[int]NPCChoice{4: {Button: 4, MaximumCost: 25}, 32: {Button: 32}}
	skill.Registry = MustNPCRegistry([]NPCSpec{spec})
	base.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 100, ObjectID: 42, Open: true, ButtonType: buttons}
	session := &dialogueSession{npcSkillSession: base}
	skill.Backend.Session = session
	return skill, session, automation.Action{Skill: "npc.dialogue", ExpectedRevision: 12, MaximumCost: 25, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":4}`)}
}

func TestNPCDialoguePagesThenConfirms(t *testing.T) {
	skill, session, action := dialogueFixture(t, 32)
	session.pages = []aigame.WindowSnapshot{
		{Type: 0, Sequence: 100, ObjectID: 42, Open: true, ButtonType: 32, Data: "same text"},
		{Type: 0, Sequence: 100, ObjectID: 42, Open: true, ButtonType: 12, Data: "same text"},
	}
	if err := skill.Execute(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(session.actions) != 3 {
		t.Fatal(session.actions)
	}
	for i, want := range []int32{32, 32, 4} {
		if got := session.actions[i]; got.action.WindowSelect != want || got.expected != uint64(12+i) {
			t.Fatal(got)
		}
	}
}

func TestNPCDialogueNeverResendsUnacknowledgedPage(t *testing.T) {
	skill, session, action := dialogueFixture(t, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := skill.Execute(ctx, action); err == nil {
		t.Fatal("missing acknowledgement accepted")
	}
	if len(session.actions) != 1 || session.actions[0].action.WindowSelect != 32 {
		t.Fatal(session.actions)
	}
}

func TestNPCDialogueRejectsChangedWindowAndUnavailableFinalButton(t *testing.T) {
	for _, mode := range []string{"npc", "sequence", "button", "budget", "next-price"} {
		t.Run(mode, func(t *testing.T) {
			skill, session, action := dialogueFixture(t, 32)
			next := aigame.WindowSnapshot{Type: 0, Sequence: 100, ObjectID: 42, Open: true, ButtonType: 12}
			switch mode {
			case "npc":
				next.ObjectID++
			case "sequence":
				next.Sequence++
			case "button":
				next.ButtonType = 1
			case "budget":
				action.MaximumCost = 24
			case "next-price":
				spec := skill.Registry["trainer"]
				spec.WindowSequence, spec.WindowObjectID, spec.Choices = 0, 0, nil
				spec.Windows[0].Choices[32] = NPCChoice{Button: 32, MaximumCost: 1}
				skill.Registry = MustNPCRegistry([]NPCSpec{spec})
			}
			session.pages = []aigame.WindowSnapshot{next}
			if err := skill.Execute(context.Background(), action); err == nil {
				t.Fatal("unreviewed dialogue accepted")
			}
			for _, record := range session.actions {
				if record.action.WindowSelect != 32 {
					t.Fatal("final mutation sent", record)
				}
			}
		})
	}
}
