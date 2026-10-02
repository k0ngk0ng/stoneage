package battlepolicy

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestNativeRecipientFeatureProjection(t *testing.T) {
	root := os.Getenv("STONEAGE_BATTLE_RECIPIENT_DIR")
	if root == "" {
		t.Skip("explicit native recipient capture required")
	}
	raw, err := os.ReadFile(filepath.Join(root, "recipient-passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Cases []struct {
			Case, Packet  string
			Before, After []int
		}
	}
	if err = json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if len(captured.Cases) != 7 {
		t.Fatal("incomplete native effects")
	}
	for _, tc := range captured.Cases {
		t.Run(tc.Case, func(t *testing.T) {
			team := fixture(1, 0)
			replay, err := aigame.NewBattleReplay(team[0].MatchID, 1)
			if err != nil {
				t.Fatal(err)
			}
			packet, err := hex.DecodeString(tc.Packet)
			if err != nil {
				t.Fatal(err)
			}
			if err = replay.Apply("B", packet); err != nil {
				t.Fatal(err)
			}
			batch := replay.Events(0, "", 0)
			// Only the observer boundary/roster is synthetic here; effects must
			// come exclusively from the actual native movie, never engine HP.
			batch.Observation = team[0]
			frame, err := EncodeVersion(team, History{Batch: &batch, First: true}, FeatureVersion)
			if err != nil {
				t.Fatal(err)
			}
			var delta [20]float64
			for _, x := range frame.EventSequence {
				if x[10] == 0 {
					continue
				}
				for slot := 0; slot < 20; slot++ {
					if x[56+slot] == 1 {
						delta[slot] += math.Copysign(math.Expm1(math.Abs(float64(x[76]))*math.Log(1001)), float64(x[76]))
					}
				}
			}
			for slot, got := range delta {
				if math.Abs(got-float64(tc.After[slot]-tc.Before[slot])) > .001 {
					t.Fatalf("encoded delta slot %d: %g", slot, got)
				}
			}
			if tc.Case == "guardian_reflection" {
				x := frame.EventSequence[0]
				if x[56] != 1 || x[121] != 1 || x[126] != 1 || x[76] >= 0 {
					t.Fatal("combined effect lost", x)
				}
			}
		})
	}
}
