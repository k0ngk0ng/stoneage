package battlenet

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func tinyLoss(m *Model[float64], train bool) (float64, map[string][]float64) {
	g := &Graph[float64]{Train: train}
	b := m.Bind(g)
	entities := g.New(3, 5, []float64{.1, .4, .3, .5, .7, .8, .1, -.2, .3, .4, -.2, .8, .4, .6, .3})
	events := g.New(2, 3, []float64{.1, -.3, .6, .4, .2, -.1})
	encoded, err := b.EncodeSequence(context.Background(), entities, events, g.New(1, 4, nil))
	if err != nil {
		panic(err)
	}
	encoded, err = b.EncodeSequence(context.Background(), entities, events, encoded.Memory)
	if err != nil {
		panic(err)
	}
	candidates := g.New(3, 4, []float64{.1, .2, .4, .7, -.1, .3, .2, .4, .4, .1, -.2, .6})
	prefix := g.New(1, 4, nil)
	logits, embeddings := b.Score(encoded, 0, candidates, []int{0, 2, -1}, prefix)
	loss := g.Scale(g.Slice(g.LogSoftmax(logits, nil), 0, 1, 1, 1), -1)
	prefix = b.AdvancePlan(prefix, g.Slice(embeddings, 1, 1, 0, 4))
	logits, _ = b.Score(encoded, 1, candidates, []int{0, 2, -1}, prefix)
	loss = g.Add(loss, g.Scale(g.Slice(g.LogSoftmax(logits, nil), 0, 1, 2, 1), -.7))
	value := b.Value(encoded)
	loss = g.Add(loss, g.Mul(value, value))
	if train {
		g.Backward(loss)
	}
	return loss.Data[0], b.Gradients()
}

func TestCommanderNetworkGradients(t *testing.T) {
	m, e := NewModel[float64](Config{EntityFeatures: 5, CandidateFeatures: 4, EventFeatures: 3, Width: 4, Heads: 2, Layers: 1}, 7)
	if e != nil {
		t.Fatal(e)
	}
	_, grad := tinyLoss(m, true)
	checked := 0
	for _, name := range m.Names() {
		p := m.Parameters[name]
		for i := 0; i < len(p.Values); i += max(1, len(p.Values)/5) {
			old := p.Values[i]
			const eps = 1e-5
			p.Values[i] = old + eps
			plus, _ := tinyLoss(m, false)
			p.Values[i] = old - eps
			minus, _ := tinyLoss(m, false)
			p.Values[i] = old
			want := (plus - minus) / (2 * eps)
			got := grad[name][i]
			if math.IsNaN(got) || math.Abs(want-got) > 5e-4*(1+math.Abs(want)) {
				t.Fatalf("%s[%d]: derivative=%g finite-difference=%g", name, i, got, want)
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatal("insufficient gradient coverage")
	}
}

func TestCommanderFloat32SaveAndInference(t *testing.T) {
	m, e := NewModel[float32](Config{EntityFeatures: 64, CandidateFeatures: 48, EventFeatures: 16, Width: 64, Heads: 4, Layers: 2}, 42)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Validate(); e != nil {
		t.Fatal(e)
	}
	if m.Count() > 1000000 {
		t.Fatal("exceeded small model budget", m.Count())
	}
	t.Log("parameters", m.Count())
	data, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	var restored Model[float32]
	if e = json.Unmarshal(data, &restored); e != nil {
		t.Fatal(e)
	}
	if e = restored.Validate(); e != nil {
		t.Fatal(e)
	}
	eval := func(m *Model[float32]) []float32 {
		g := &Graph[float32]{}
		b := m.Bind(g)
		x := make([]float32, 4*64)
		for i := range x {
			x[i] = float32(math.Sin(float64(i)))
		}
		state := b.Encode(g.New(4, 64, x), g.New(1, 16, nil), g.New(1, 64, nil))
		scores, _ := b.Score(state, 0, g.New(3, 48, make([]float32, 3*48)), []int{1, 3, -1}, g.New(1, 64, nil))
		return append(append([]float32(nil), scores.Data...), b.Value(state).Data...)
	}
	a, b := eval(m), eval(&restored)
	for i := range a {
		if a[i] != b[i] || math.IsNaN(float64(a[i])) || math.IsInf(float64(a[i]), 0) {
			t.Fatal("serialization changed inference", a, b)
		}
	}
	p := restored.Parameters["score.1.b"]
	p.Values[0] = float32(math.NaN())
	if restored.Validate() == nil {
		t.Fatal("invalid weights accepted")
	}
}

func TestEntityPermutation(t *testing.T) {
	m, _ := NewModel[float64](Config{EntityFeatures: 5, CandidateFeatures: 4, EventFeatures: 3, Width: 4, Heads: 2, Layers: 1}, 7)
	eval := func(permuted bool) float64 {
		g := &Graph[float64]{}
		b := m.Bind(g)
		a := []float64{.1, .4, .3, .5, .7}
		z := []float64{.8, .1, -.2, .3, .4}
		actor, target := 0, 1
		x := append(append([]float64(nil), a...), z...)
		if permuted {
			x = append(append([]float64(nil), z...), a...)
			actor, target = 1, 0
		}
		state := b.Encode(g.New(2, 5, x), g.New(1, 3, nil), g.New(1, 4, nil))
		score, _ := b.Score(state, actor, g.New(1, 4, []float64{.1, .2, .3, .4}), []int{target}, g.New(1, 4, nil))
		return score.Data[0]
	}
	if math.Abs(eval(false)-eval(true)) > 1e-10 {
		t.Fatal("entity ordering changed equivalent decision")
	}
}

func TestPlanMemoryIncludesChosenActorAndTarget(t *testing.T) {
	m, _ := NewModel[float32](Config{EntityFeatures: 5, CandidateFeatures: 4, EventFeatures: 3, Width: 8, Heads: 2, Layers: 1}, 83)
	g := &Graph[float32]{}
	b := m.Bind(g)
	state := b.Encode(g.New(3, 5, []float32{1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0}), g.New(1, 3, nil), g.New(1, 8, nil))
	_, embeddings := b.Score(state, 0, g.New(2, 4, nil), []int{1, 2}, g.New(1, 8, nil))
	a := b.AdvancePlan(g.New(1, 8, nil), g.Slice(embeddings, 0, 1, 0, 8))
	z := b.AdvancePlan(g.New(1, 8, nil), g.Slice(embeddings, 1, 1, 0, 8))
	difference := float32(0)
	for i := range a.Data {
		difference += float32(math.Abs(float64(a.Data[i] - z.Data[i])))
	}
	if difference <= 1e-6 {
		t.Fatal("identical action features erased the chosen target from joint plan memory")
	}
}

// BenchmarkCommander5v5 measures numerical inference only. It excludes game
// transport/feature extraction and is not an end-to-end response-time claim.
func BenchmarkCommander5v5(b *testing.B) {
	m, _ := NewModel[float32](Config{EntityFeatures: 64, CandidateFeatures: 48, EventFeatures: 16, Width: 64, Heads: 4, Layers: 2}, 42)
	entities := make([]float32, 20*64)
	candidates := make([]float32, 32*48)
	targets := make([]int, 32)
	for i := range entities {
		entities[i] = float32(math.Sin(float64(i)))
	}
	for i := range candidates {
		candidates[i] = float32(math.Cos(float64(i)))
	}
	for i := range targets {
		targets[i] = i % 20
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := &Graph[float32]{}
		bound := m.Bind(g)
		state := bound.Encode(g.New(20, 64, entities), g.New(1, 16, nil), g.New(1, 64, nil))
		prefix := g.New(1, 64, nil)
		for actor := 0; actor < 10; actor++ {
			logits, embeddings := bound.Score(state, actor, g.New(32, 48, candidates), targets, prefix)
			selected := 0
			for j, v := range logits.Data {
				if v > logits.Data[selected] {
					selected = j
				}
			}
			prefix = bound.AdvancePlan(prefix, g.Slice(embeddings, selected, 1, 0, 64))
		}
		_ = bound.Value(state)
	}
}
