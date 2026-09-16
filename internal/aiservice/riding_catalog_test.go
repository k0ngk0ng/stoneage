package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// The draft is a review artifact: neither loading it nor including it in a
// deployment may silently enable a paid action before runtime acceptance.
func TestRidingDraftRemainsDisabled(t *testing.T) {
	path := filepath.Join("..", "..", "ai", "catalogs", "drafts", "riding-2.5.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document npcRegistryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNPCRegistry(raw, document.KnowledgeFingerprint); !errors.Is(err, ErrNPCUnverified) {
		t.Fatalf("draft enabled or invalid schema: %v", err)
	}
	if len(document.NPCs) != 4 {
		t.Fatalf("expected four trainers, got %d", len(document.NPCs))
	}
	var task aiknowledge.TaskDefinition
	raw, err = os.ReadFile(filepath.Join("..", "aiknowledge", "tasks", "riding-basic.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != aiknowledge.TaskUnverified || task.PreparationReviewed || task.ExecutionVerified {
		t.Fatal("source review must not enable quest execution")
	}
	for _, entry := range document.NPCs {
		if entry.Verified || entry.ActorIDKnown || entry.Name != "骑乘训练师" || entry.TalkRange != 1 {
			t.Fatalf("invalid draft trainer: %+v", entry)
		}
		if entry.SourceFingerprint != document.KnowledgeFingerprint {
			t.Fatal("stale draft binding")
		}
		// Promote only a local copy to exercise the production contract parser;
		// the on-disk document remains unverified and no session is constructed.
		spec := entry.toNPCSpec()
		spec.Verified = true
		registry, err := NewNPCRegistry([]NPCSpec{spec})
		if err != nil {
			t.Fatal(err)
		}
		normalized := registry[entry.Alias]
		for i, step := range task.Steps[2:] {
			var args struct {
				Sequence json.RawMessage `json:"window_sequence"`
				Choice   json.RawMessage `json:"choice"`
			}
			if err := json.Unmarshal(step.Action.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			window, err := resolveNPCWindow(normalized, args.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			choice, err := resolveNPCChoice(window.Choices, args.Choice)
			if err != nil {
				t.Fatal(err)
			}
			if !window.WindowObjectFromActor {
				t.Fatal("must resolve object from observed trainer")
			}
			switch i {
			case 0:
				if window.Type != 2 || choice.Data != "4" || choice.MaximumCost != 0 {
					t.Fatal("main menu must send SELECT data 4")
				}
			case 1:
				if window.Type != 2 || choice.Data != "1" || choice.MaximumCost != 0 {
					t.Fatal("course menu must send SELECT data 1")
				}
			case 2:
				if window.Type != 0 || choice.Button != 4 || choice.MaximumCost != 5000 {
					t.Fatal("confirmation must send YES and quote 5000")
				}
			}
		}
	}
}

func TestPackagedCatalogsMatchEffectiveKnowledge(t *testing.T) {
	root := filepath.Join("..", "..")
	data := filepath.Join(root, "runtime", "legacy-server", "gmsv", "data")
	if _, err := os.Stat(filepath.Join(data, "enemybase.txt")); os.IsNotExist(err) {
		t.Skip("effective data not installed")
	}
	knowledge, err := aiknowledge.LoadDataDir(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ai", "catalogs")
	fingerprint := knowledge.Fingerprint()
	healing, err := LoadHealingItemsForData(filepath.Join(dir, "healing-items-2.5.json"), fingerprint, filepath.Join(data, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	npcs, err := LoadNPCRegistry(filepath.Join(dir, "shops-2.5.json"), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStockItems(filepath.Join(dir, "stock-items-2.5.json"), fingerprint, npcs, healing); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"riding-2.5.json", "adult-ceremony-2.5.json"} {
		if _, err := LoadNPCRegistry(filepath.Join(dir, "drafts", name), fingerprint); !errors.Is(err, ErrNPCUnverified) {
			t.Fatalf("%s must be bound but disabled: %v", name, err)
		}
	}
}
