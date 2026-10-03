package aiservice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestNPCQuestKeywordUsesReviewedTextAtExactNPC(t *testing.T) {
	for _, command := range []string{"", "talk", "P|月亮"} {
		t.Run(command, func(t *testing.T) {
			skill, session, spec := npcSkillFixture(t, true)
			session.updateDirection, session.statusDirection = true, 2
			spec.TalkText = "P|月亮"
			skill.Registry = MustNPCRegistry([]NPCSpec{spec})
			args, _ := json.Marshal(map[string]any{"npc": spec.Alias, "command": command})
			if err := skill.Execute(context.Background(), automation.Action{Skill: "npc.talk", ExpectedRevision: 12, Arguments: args}); err != nil {
				t.Fatal(err)
			}
			last := session.actions[len(session.actions)-1]
			if last.action.Kind != aigame.ActionTalk || last.action.Command != "P|月亮" || last.action.Range != 3 || last.expected != 14 {
				t.Fatal(last)
			}
		})
	}
}

func TestNPCQuestKeywordCannotBeOverriddenOrUsedAtDifferentNPC(t *testing.T) {
	for _, mode := range []string{"unreviewed-text", "wrong-npc", "wrong-floor", "out-of-range", "stale", "wrong-keyword"} {
		t.Run(mode, func(t *testing.T) {
			skill, session, spec := npcSkillFixture(t, false)
			spec.TalkText = "P|月亮"
			command := ""
			revision := uint64(12)
			switch mode {
			case "unreviewed-text":
				spec.TalkText, command = "", "P|月亮"
			case "wrong-npc":
				session.snapshot.Actors[0].Name = "Someone else"
			case "wrong-floor":
				session.snapshot.Position.Floor++
			case "out-of-range":
				session.snapshot.Position.X = 100
			case "stale":
				revision--
			case "wrong-keyword":
				command = "P|hi"
			}
			skill.Registry = MustNPCRegistry([]NPCSpec{spec})
			args, _ := json.Marshal(map[string]any{"npc": spec.Alias, "command": command})
			if err := skill.Execute(context.Background(), automation.Action{Skill: "npc.talk", ExpectedRevision: revision, Arguments: args}); err == nil || len(session.actions) != 0 {
				t.Fatal("invalid keyword submitted", err, session.actions)
			}
		})
	}
}

func TestNPCRegistryRejectsInvalidKeyword(t *testing.T) {
	for _, value := range []string{"answer", "P|", "P|a|b", "P|a\nb", "P|a\x00", "P|" + strings.Repeat("a", 256), "P|\xff"} {
		spec := verifiedNPCSpec()
		spec.TalkText = value
		if _, err := NewNPCRegistry([]NPCSpec{spec}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestNPCRegistryLoadsReviewedKeyword(t *testing.T) {
	document := validNPCRegistryDocument()
	document.NPCs[0].TalkText = "P|月亮"
	registry, err := decodeNPCRegistry(marshalNPCRegistryDocument(t, document), catalogTestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := registry.Lookup("trainer")
	if !ok || spec.TalkText != "P|月亮" {
		t.Fatal(spec)
	}
}
