package battlepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestV7RecipientHistoryAndSideNormalization(t *testing.T) {
	ptr := func(x int) *int { return &x }
	for _, tc := range []struct {
		name                       string
		effect                     aigame.BattleLogEntry
		recipient, guardian, delta int
		known                      bool
	}{
		{"ordinary", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Recipient: ptr(10)}, 10, -1, -25, true},
		{"guard", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 512, Recipient: ptr(15), Guardian: ptr(15)}, 15, 15, -25, true},
		{"reflect", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 1024, Recipient: ptr(0)}, 0, -1, -25, true},
		{"guard_reflect", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 1536, Recipient: ptr(0), Guardian: ptr(15)}, 0, 15, -25, true},
		{"counter", aigame.BattleLogEntry{Kind: "counter", Actor: 10, Target: 0, Damage: 25, Flags: 1024, Recipient: ptr(10)}, 10, -1, -25, true},
		{"absorb", aigame.BattleLogEntry{Kind: "attack", Actor: 10, Target: 0, Damage: 25, Flags: 2048, Recipient: ptr(0)}, 0, -1, 25, true},
		{"zero_guard", aigame.BattleLogEntry{Kind: "attack", Actor: 10, Target: 5, Damage: 25, Flags: 512, Recipient: ptr(0), Guardian: ptr(0)}, 0, 0, -25, true},
		{"missing", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25}, -1, -1, 0, false},
		{"invalid_slot", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Recipient: ptr(20)}, -1, -1, 0, false},
		{"dodge", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 32}, -1, -1, 0, true},
		{"vanish", aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 4096}, -1, -1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := fixture(1, 0)
			batch := aigame.BattleEventBatch{Stream: "versioned", Cursor: 1, Observation: team[0], Events: []aigame.BattleEvent{{Sequence: 1, MatchID: team[0].MatchID, Effects: []aigame.BattleLogEntry{tc.effect}}}}
			f, err := Encode(team, History{Batch: &batch, First: true})
			if err != nil {
				t.Fatal(err)
			}
			x := f.EventSequence[0]
			if x[76] != scale(int32(tc.delta)) || (x[11] == 1) != tc.known || (x[10] == 1) != (tc.recipient >= 0) || (x[126] == 1) != (tc.guardian >= 0) {
				t.Fatal("recipient/known/delta", x)
			}
			if tc.recipient >= 0 && x[56+tc.recipient] != 1 {
				t.Fatal("wrong recipient slot")
			}
			if tc.guardian >= 0 && x[106+tc.guardian] != 1 {
				t.Fatal("wrong guardian slot")
			}
			var sums [2]float32
			if tc.recipient >= 0 {
				sums[tc.recipient/10] = scale(int32(tc.delta))
			}
			if f.Events[4] != sums[0] || f.Events[5] != sums[1] {
				t.Fatal("summary attributed to intended target", f.Events)
			}
			if !tc.known && (x[6] != 1 || f.Events[10] == 0) {
				t.Fatal("missing recipient treated as known")
			}
			swapped := tc.effect
			swapped.Actor = (swapped.Actor + 10) % 20
			swapped.Target = (swapped.Target + 10) % 20
			if swapped.Recipient != nil && *swapped.Recipient < 20 {
				swapped.Recipient = ptr((*swapped.Recipient + 10) % 20)
			}
			if swapped.Guardian != nil {
				swapped.Guardian = ptr((*swapped.Guardian + 10) % 20)
			}
			if encodeEffect(swapped, 1) != x {
				t.Fatal("side changes normalized effect")
			}
		})
	}
}

func TestModelInputContractPinsEncodingAndDigest(t *testing.T) {
	config := NetworkConfig()
	config.Width, config.Heads, config.Layers = 8, 2, 1
	current, err := battlenet.NewModel[float32](config, 17)
	if err != nil {
		t.Fatal(err)
	}
	config.InputSchema = ""
	legacy, err := battlenet.NewModel[float32](config, 17)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Parameters, legacy.Parameters) {
		t.Fatal("schema changed numerical initialization")
	}
	a, err := NetworkDigest(current)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NetworkDigest(legacy)
	if err != nil || a == b {
		t.Fatal("input semantics missing from network identity", err)
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("input_schema")) {
		t.Fatal("legacy serialization rewritten")
	}
	for _, schema := range []string{LegacyFeatureVersion, RecipientFeatureVersion, FeatureVersion} {
		frame, err := EncodeVersion(fixture(1, 0), History{First: true}, schema)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range []*battlenet.Model[float32]{legacy, current} {
			_, err = Forward(context.Background(), model.Bind(&battlenet.Graph[float32]{}), frame, nil, nil, nil)
			if (err == nil) != (NetworkFeatures(model.Config) == schema) {
				t.Fatal("model/frame contract not enforced", schema, model.Config, err)
			}
		}
	}
	if _, err := EncodeVersion(fixture(1, 0), History{}, "future"); err == nil {
		t.Fatal("unknown feature version accepted")
	}
}
