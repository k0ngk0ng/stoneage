package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestLevelingKnowledgePreservesSourceVerification(t *testing.T) {
	b, game := gameFixture(t)
	var err error
	b.Knowledge, err = aiknowledge.LoadDataDir(context.Background(), filepath.Join(filepath.Join("..", ".."), "server", "legacy", "source", "2.5", "gmsv", "data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id       string
		verified bool
	}{{"28", true}, {"21", false}} {
		result, err := b.QueryKnowledge(context.Background(), b.Binding, aimcp.KnowledgeQuery{Kind: "leveling", ID: test.id})
		if err != nil || len(result.Entries) != 1 {
			t.Fatalf("area %s: %+v %v", test.id, result, err)
		}
		entry := result.Entries[0]
		var area aiknowledge.LevelingArea
		if err := json.Unmarshal(entry.Data, &area); err != nil {
			t.Fatal(err)
		}
		if entry.Verified != test.verified || area.Verified != entry.Verified || len(area.Evidence) == 0 || result.Revision != b.Knowledge.Fingerprint() {
			t.Fatalf("area %s lost source verification: %+v", test.id, entry)
		}
	}
	if game.writes != 0 {
		t.Fatal("knowledge query wrote to game")
	}
}
