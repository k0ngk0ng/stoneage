package battlepolicy

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestOrderedEffectsChangeMemoryWithIdenticalSummary(t *testing.T) {
	team := fixture(1, 0)
	heal := 20
	batch := aigame.BattleEventBatch{Stream: "ordered", Cursor: 1, Observation: team[0], Events: []aigame.BattleEvent{{Sequence: 1, MatchID: team[0].MatchID, Effects: []aigame.BattleLogEntry{
		{Kind: "attack", Actor: 10, Target: 0, Damage: 20},
		{Kind: "BD", Actor: -1, Target: 0, Resource: "hp", Delta: &heal},
	}}}}
	a, err := Encode(team, History{Batch: &batch, First: true})
	if err != nil {
		t.Fatal(err)
	}
	batch.Events[0].Effects[0], batch.Events[0].Effects[1] = batch.Events[0].Effects[1], batch.Events[0].Effects[0]
	b, err := Encode(team, History{Batch: &batch, First: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.Events != b.Events || reflect.DeepEqual(a.EventSequence, b.EventSequence) {
		t.Fatal("event order lost or summary changed")
	}
	config := NetworkConfig()
	config.Width, config.Heads, config.Layers = 8, 2, 1
	m, err := battlenet.NewModel[float32](config, 91)
	if err != nil {
		t.Fatal(err)
	}
	forward := func(f Frame) Output {
		o, e := Forward(context.Background(), m.Bind(&battlenet.Graph[float32]{}), f, nil, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return o
	}
	x, y := forward(a), forward(b)
	if reflect.DeepEqual(x.Memory.Data, y.Memory.Data) {
		t.Fatal("swapping heal and hit didn't affect recurrent memory")
	}
	if again := forward(a); !reflect.DeepEqual(again.Memory.Data, x.Memory.Data) {
		t.Fatal("replay advanced persistent memory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Forward(ctx, m.Bind(&battlenet.Graph[float32]{}), a, nil, nil, nil); e != context.Canceled {
		t.Fatal("canceled decision ran", e)
	}
}

func TestOrderedEffectIdentityVisibilityAndStatus(t *testing.T) {
	delta, status := 7, 3
	for _, effect := range []aigame.BattleLogEntry{
		{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 4, Hit: 1, Hits: 2},
		{Kind: "counter", Actor: 10, Target: 0, Damage: 12},
		{Kind: "BD", Actor: -1, Target: 5, Resource: "hp", Delta: &delta},
		{Kind: "BM", Actor: -1, Target: 15, Status: &status},
		{Kind: "BG", Actor: 0, Target: -1},
	} {
		a := encodeEffect(effect, 0)
		swapped := effect
		if effect.Actor >= 0 {
			swapped.Actor = (effect.Actor + 10) % 20
		}
		if effect.Target >= 0 {
			swapped.Target = (effect.Target + 10) % 20
		}
		if b := encodeEffect(swapped, 1); a != b {
			t.Fatal("absolute side changed history features", effect.Kind)
		}
	}
	sleep := encodeEffect(aigame.BattleLogEntry{Kind: "BM", Target: 15, Status: &status}, 0)
	if sleep[99] != 1 || sleep[13] != 1 || sleep[8] != 0 {
		t.Fatal("status must be categorical, without an invented actor")
	}
	for _, flags := range []int{512, 1024} {
		x := encodeEffect(aigame.BattleLogEntry{Kind: "attack", Actor: 0, Target: 10, Damage: 90, Flags: flags}, 0)
		if x[9] != 1 || x[10] != 0 || x[11] != 0 || x[76] != 0 || x[6] != 1 {
			t.Fatal("unresolved recipient assigned damage to intended target")
		}
	}
	unknown := encodeEffect(aigame.BattleLogEntry{Kind: "unknown", Text: "actor zero?", Raw: "private-looking raw"}, 0)
	if unknown[6] != 1 || unknown[8] != 0 || unknown[9] != 0 {
		t.Fatal("unknown effect fabricated identities")
	}
}

func TestOrderedHistoryBoundariesAndMalformedFrame(t *testing.T) {
	team := fixture(1, 0)
	effects := make([]aigame.BattleLogEntry, MaxHistoryEffects+1)
	batch := aigame.BattleEventBatch{Stream: "ordered", Cursor: 2, Observation: team[0], Events: []aigame.BattleEvent{
		{Sequence: 1, MatchID: "previous", Effects: []aigame.BattleLogEntry{{Kind: "attack", Actor: 0, Target: 10, Damage: 20}}},
		{Sequence: 2, MatchID: team[0].MatchID, Effects: effects},
	}}
	if _, _, err := EncodeHistorySequence(team[0], History{Batch: &batch, First: true}); err == nil {
		t.Fatal("oversized history silently truncated")
	}
	batch.Events[1].Effects = effects[:1]
	frame, err := Encode(team, History{Batch: &batch, First: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.EventSequence) != 1 || frame.EventSequence[0][6] != 1 {
		t.Fatal("prior match included or unknown effect erased")
	}
	frame.EventSequence[0][0] = float32(math.NaN())
	if frame.Validate() == nil {
		t.Fatal("nonfinite event accepted")
	}
	frame.EventSequence[0][0] = 0
	frame.EventSequence[0][127] = 1
	if frame.Validate() == nil {
		t.Fatal("summary injected into chronological effects")
	}
}

func TestV6RecipientMetadataDoesNotReinterpretFeatures(t *testing.T) {
	team := fixture(1, 0)
	batch := aigame.BattleEventBatch{Stream: "recipients", Cursor: 1, Observation: team[0], Events: []aigame.BattleEvent{{Sequence: 1, MatchID: team[0].MatchID, Effects: []aigame.BattleLogEntry{
		{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 512},
		{Kind: "counter", Actor: 10, Target: 0, Damage: 12, Flags: 1024},
	}}}}
	before, err := EncodeVersion(team, History{Batch: &batch, First: true}, LegacyFeatureVersion)
	if err != nil {
		t.Fatal(err)
	}
	guardian, reflected := 15, 10
	batch.Events[0].Effects[0].Guardian = &guardian
	batch.Events[0].Effects[0].Recipient = &guardian
	batch.Events[0].Effects[1].Recipient = &reflected
	after, err := EncodeVersion(team, History{Batch: &batch, First: true}, LegacyFeatureVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("optional metadata changed the fixed v6 feature contract")
	}
	for _, event := range after.EventSequence {
		if event[9] != 1 || event[10] != 0 || event[11] != 0 || event[76] != 0 {
			t.Fatal("v6 must retain its original unresolved recipient semantics")
		}
	}
}
