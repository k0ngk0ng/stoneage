package battlepolicy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestRecallPreservesContinuousDecisions(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		for side := 0; side < 2; side++ {
			t.Run(fmt.Sprintf("mode%d/side%d", mode, side), func(t *testing.T) {
				config := NetworkConfig()
				config.Width, config.Heads, config.Layers = 8, 2, 1
				model, err := battlenet.NewModel[float32](config, 43)
				if err != nil {
					t.Fatal(err)
				}
				frame, err := Encode(fixture(mode, side), History{First: true})
				if err != nil {
					t.Fatal(err)
				}
				var full, recalled []float32
				for turn := 0; turn < 32; turn++ {
					frame.Turn = int32(turn)
					frame.Entities[0][eHP] = float32(32-turn) / 32
					frame.EventSequence = make([][EventFeatures]float32, turn%7)
					for i := range frame.EventSequence {
						frame.EventSequence[i][i] = float32(turn+i) / 64
					}
					g := &battlenet.Graph[float32]{}
					want, err := Forward(context.Background(), model.Bind(g), frame, g.New(1, config.Width, full), nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					g = &battlenet.Graph[float32]{}
					// The current decision after replay must match every output,
					// not merely the chosen action or an approximate hidden state.
					got, err := Forward(context.Background(), model.Bind(g), frame, g.New(1, config.Width, recalled), nil, nil)
					if err != nil || !reflect.DeepEqual(got.Choices, want.Choices) || !reflect.DeepEqual(got.ConditionalLogProbs, want.ConditionalLogProbs) || !reflect.DeepEqual(got.Value.Data, want.Value.Data) || !reflect.DeepEqual(got.Memory.Data, want.Memory.Data) {
						t.Fatal("replayed history changed current decision", turn, err)
					}
					g = &battlenet.Graph[float32]{}
					memory, err := Recall(context.Background(), model.Bind(g), frame, g.New(1, config.Width, recalled))
					if err != nil || !reflect.DeepEqual(memory.Data, want.Memory.Data) {
						t.Fatal("recall changed memory", turn, err)
					}
					full, recalled = append([]float32(nil), want.Memory.Data...), append([]float32(nil), memory.Data...)
				}
			})
		}
	}
}

func TestRecallRejectsIncompleteOrNonfiniteHistory(t *testing.T) {
	frame, err := Encode(fixture(2, 0), History{First: true})
	if err != nil {
		t.Fatal(err)
	}
	model, err := battlenet.NewModel[float32](NetworkConfig(), 43)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Recall(ctx, model.Bind(&battlenet.Graph[float32]{}), frame, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
	for _, mutate := range []func(*Frame){
		func(f *Frame) { f.Events[127] = 0 },
		func(f *Frame) { f.Slots = f.Slots[:1] },
		func(f *Frame) { f.Entities[0][0] = float32(math.NaN()) },
	} {
		f, err := Encode(fixture(2, 0), History{First: true})
		if err != nil {
			t.Fatal(err)
		}
		mutate(&f)
		if _, err := Recall(context.Background(), model.Bind(&battlenet.Graph[float32]{}), f, nil); err == nil {
			t.Fatal("incomplete or corrupt history accepted")
		}
	}
	g := &battlenet.Graph[float32]{}
	bad := g.New(1, model.Config.Width, nil)
	bad.Data[0] = float32(math.Inf(1))
	if _, err := Recall(context.Background(), model.Bind(g), frame, bad); err == nil {
		t.Fatal("nonfinite prior memory accepted")
	}
}
