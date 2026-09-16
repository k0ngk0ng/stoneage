package playermanager

import (
	"context"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"hash/fnv"
	"testing"
)

func TestInitializeAIUsesIdentityFromConfirmedSave(t *testing.T) {
	const identity = "pc1_1234567890abcdef1234567890abcdef"
	for _, id := range []string{identity, "", "malformed"} {
		t.Run(id, func(t *testing.T) {
			plan := aiinitial.Resolved{CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}}
			m, archives, game := newOfflineManager(t, testArchive("lv=1"))
			saved := testArchive("lv=60", "nexp=0", "hp=100", "vi=19700", "str=0", "tou=0", "dx=0", "skup=0", "charid="+id)
			doc, err := playerdata.ParseSave(saved)
			if err != nil {
				t.Fatal(err)
			}
			hash := fnv.New64a()
			hash.Write(doc.Character.Bytes())
			game.call = func(request map[string]string) (map[string]string, error) {
				switch request["action"] {
				case "snapshot":
					return map[string]string{"account": "alice", "character": "Hero", "character_slot": "0", "sequence": "42", "revision": "0123456789abcdef"}, nil
				case "initialize_ai":
					archives.data[archiveKey{"alice", 0}] = saved
					return map[string]string{"save_data_hash": fmt.Sprintf("%016x", hash.Sum64())}, nil
				default:
					t.Fatalf("unexpected action %s", request["action"])
					return nil, nil
				}
			}
			actual, err := m.InitializeAI(context.Background(), "alice", 0, plan)
			if id == identity {
				if err != nil || actual.PersistentCharacterID != identity || !actual.Online {
					t.Fatalf("persisted identity lost: %+v %v", actual, err)
				}
			} else if err == nil {
				t.Fatal("accepted absent or invalid persisted identity")
			}
			if countGameAction(game, "initialize_ai") != 1 || countGameAction(game, "snapshot") != 1 || len(archives.writes) != 0 {
				t.Fatal("initialization retried or used later online projection")
			}
		})
	}
}
