package aiservice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func TestAdultDraftContractsCoverEveryTaskDialog(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "ai", "catalogs", "drafts", "adult-ceremony-2.5.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc npcRegistryDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNPCRegistry(raw, doc.KnowledgeFingerprint); !errors.Is(err, ErrNPCUnverified) {
		t.Fatalf("draft must remain disabled: %v", err)
	}
	if len(doc.NPCs) != 3 {
		t.Fatal("expected guard, judge, messenger")
	}
	var specs []NPCSpec
	for _, e := range doc.NPCs {
		if e.Verified || e.ActorIDKnown || e.SourceFingerprint != doc.KnowledgeFingerprint {
			t.Fatal("draft enabled or incorrectly bound")
		}
		s := e.toNPCSpec()
		s.Verified = true
		specs = append(specs, s)
	}
	registry, err := NewNPCRegistry(specs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(root, "internal", "aiknowledge", "tasks", "adult-ceremony.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task aiknowledge.TaskDefinition
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	for _, step := range task.Steps {
		if step.Action.Skill == "move" {
			continue
		}
		var args struct {
			NPC      string          `json:"npc"`
			Floor    int             `json:"floor"`
			X        int             `json:"x"`
			Y        int             `json:"y"`
			Sequence json.RawMessage `json:"window_sequence"`
			Choice   json.RawMessage `json:"choice"`
		}
		if err := json.Unmarshal(step.Action.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		spec, ok := registry[args.NPC]
		if !ok {
			t.Fatalf("%s missing NPC", step.ID)
		}
		switch step.Action.Skill {
		case "npc.talk":
			if spec.Floor != args.Floor || spec.X != args.X || spec.Y != args.Y {
				t.Fatalf("%s wrong entity tile", step.ID)
			}
		case "npc.window":
			window, err := resolveNPCWindow(spec, args.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			choice, err := resolveNPCChoice(window.Choices, args.Choice)
			if err != nil {
				t.Fatal(err)
			}
			if !window.WindowObjectFromActor || window.Type != 0 || choice.MaximumCost != 0 {
				t.Fatalf("%s incorrect window contract", step.ID)
			}
		default:
			t.Fatalf("unexpected ceremony skill %s", step.Action.Skill)
		}
	}
}
