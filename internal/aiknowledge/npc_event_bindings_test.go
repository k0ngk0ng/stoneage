package aiknowledge

import (
	"strings"
	"testing"
)

func TestNPCEventBindingsRespectHandlerSourcesAndAmbiguity(t *testing.T) {
	point := Rectangle{X: 3, Y: 4, X2: 3, Y2: 4}
	npc := NPCKnowledge{
		Templates: []NPCTemplate{{Name: "change", FunctionSet: "ExChangeMan", DisplayName: "Fallback", Source: SourceRef{Path: "npc/event.template"}}},
		Creates:   []NPCCreate{{Floor: 1000, Born: point, Move: point, SpawnCount: 1, Enemies: []NPCEnemyRef{{Template: "change", Argument: "file:event/task"}}, Source: SourceRef{Path: "npc/task.create", Line: 5}}},
		Files:     []NPCFile{{Path: "npc/event/task", Kind: "event", Supported: true, Events: &NPCEventScript{Rules: []NPCEventRule{{Number: 3}}}, Source: SourceRef{Path: "npc/event/task", SHA256: "script"}}, {Path: "npc/task.create", SHA256: "create"}, {Path: "npc/event.template", SHA256: "template"}},
	}
	bindings := npc.EventBindings()
	if len(bindings) != 1 || len(bindings[0].Blockers) != 0 || bindings[0].Name != "Fallback" || bindings[0].Rules != 1 || bindings[0].Create.SHA256 != "create" || bindings[0].Templates[0].SHA256 != "template" {
		t.Fatal(bindings)
	}
	npc.Templates = append(npc.Templates, npc.Templates[0])
	npc.Creates[0].Move.X2++
	npc.Creates[0].Time = 1
	bindings = npc.EventBindings()
	if len(bindings) != 1 || len(bindings[0].Blockers) != 3 {
		t.Fatal("ambiguity disappeared", bindings)
	}
	npc.Templates = npc.Templates[:1]
	npc.Templates[0].FunctionSet = "Other"
	if binding := npc.EventBindings()[0]; !strings.Contains(strings.Join(binding.Blockers, " "), "different NPC handler") {
		t.Fatal(binding)
	}
}

func TestNPCArgumentFileNeverNormalizesAmbiguousNativePaths(t *testing.T) {
	for _, arg := range []string{"profile:event/task", "file:../secret", "file:/event/task", "file:event//task", "file:event/task:other", "file:" + strings.Repeat("x", 59), "FILE:event/task"} {
		if _, err := npcArgumentFile(arg); err == nil {
			t.Fatal("ambiguous path accepted", arg)
		}
	}
	if got, err := npcArgumentFile("option:1|file:event/task|file:event/later"); err != nil || got != "npc/event/task" {
		t.Fatal(got, err)
	}
}
