package aigame

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeDamageRecipientProjection(t *testing.T) {
	root := os.Getenv("STONEAGE_BATTLE_RECIPIENT_DIR")
	if root == "" {
		t.Skip("explicit native recipient capture required")
	}
	data, err := os.ReadFile(filepath.Join(root, "recipient-passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Case, Packet  string
			Before, After []int
		}
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) != 7 {
		t.Fatal("missing native scenarios")
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Case, func(t *testing.T) {
			raw, err := hex.DecodeString(tc.Packet)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := NewBattleReplay("recipient-"+tc.Case, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := replay.Apply("B", raw); err != nil {
				t.Fatal(err)
			}
			if len(tc.Before) != 20 || len(tc.After) != 20 {
				t.Fatal("invalid native HP evidence")
			}
			var delta [20]int
			hits, counters := 0, 0
			for _, event := range replay.Events(1, "", 0).Events {
				for _, effect := range event.Effects {
					if effect.Kind != "attack" && effect.Kind != "counter" {
						continue
					}
					hits++
					if effect.Kind == "counter" {
						counters++
						if effect.Recipient == nil || *effect.Recipient != 10 {
							t.Fatal("reflected counter recipient", effect)
						}
					}
					if effect.PetDamage != 0 {
						t.Fatal("fixture unexpectedly introduced riding damage")
					}
					if tc.Case == "vanish" {
						if effect.Recipient != nil {
							t.Fatal("vanished damage assigned recipient")
						}
						continue
					}
					if effect.Recipient == nil {
						t.Fatalf("missing native recipient: %+v", effect)
					}
					if tc.Case == "guardian" || tc.Case == "guardian_reflection" {
						if effect.Guardian == nil || *effect.Guardian != 15 {
							t.Fatal("missing native guardian")
						}
					} else if effect.Guardian != nil {
						t.Fatal("invented guardian")
					}
					amount := -effect.Damage
					if effect.Flags&2048 != 0 {
						amount = -amount
					}
					delta[*effect.Recipient] += amount
				}
			}
			wantHits := 1
			if tc.Case == "counter_reflection" {
				wantHits = 2
				if counters != 1 {
					t.Fatal("missing native counter")
				}
			}
			if hits != wantHits {
				t.Fatal("incorrect native hit count", hits)
			}
			for slot, change := range delta {
				if change != tc.After[slot]-tc.Before[slot] {
					t.Fatalf("slot %d: projected %d, actual %d, raw %s", slot, change, tc.After[slot]-tc.Before[slot], raw)
				}
			}
		})
	}
}
