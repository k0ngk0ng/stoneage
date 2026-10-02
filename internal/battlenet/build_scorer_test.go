package battlenet

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestBuildScorerGradients(t *testing.T) {
	m, _ := NewBuildScorer[float64](4, 73)
	loss := func(train bool) (float64, map[string]*Tensor[float64]) {
		g := &Graph[float64]{Train: train}
		p, bound := m.forward(g, g.New(2, 4, []float64{.1, .5, .15, .25, .6, .1, .2, .1}))
		difference := g.Add(p, g.New(2, 1, []float64{-.9, -.1}))
		l := g.Sum(g.Mul(difference, difference))
		if train {
			g.Backward(l)
		}
		return l.Data[0], bound
	}
	_, bound := loss(true)
	checked := 0
	for name, p := range m.Parameters {
		for i := 0; i < len(p.Values); i += max(1, len(p.Values)/7) {
			old := p.Values[i]
			const eps = 1e-6
			p.Values[i] = old + eps
			plus, _ := loss(false)
			p.Values[i] = old - eps
			minus, _ := loss(false)
			p.Values[i] = old
			want, got := (plus-minus)/(2*eps), bound[name].Grad[i]
			if math.Abs(want-got) > 1e-5*(1+math.Abs(want)) {
				t.Fatalf("%s[%d] gradient=%g numerical=%g", name, i, got, want)
			}
			checked++
		}
	}
	if checked < 30 {
		t.Fatal("insufficient scorer gradient coverage")
	}
}

func TestBuildScorerFitsAndRoundTrips(t *testing.T) {
	var features [][]float32
	var scores []float32
	for i := 0; i < 12; i++ {
		x := float32(i+1) / 16
		features = append(features, []float32{(1 - x) / 3, x, (1 - x) / 3, (1 - x) / 3})
		scores = append(scores, .1+.8*x)
	}
	m, e := FitBuildScorer(context.Background(), features, scores, 250, 11)
	if e != nil {
		t.Fatal(e)
	}
	mse := float64(0)
	for i, f := range features {
		p, e := m.Predict(f)
		if e != nil {
			t.Fatal(e)
		}
		mse += math.Pow(float64(p-scores[i]), 2) / float64(len(scores))
	}
	if mse > .001 {
		t.Fatal("scorer did not learn numeric labels", mse)
	}
	raw, _ := json.Marshal(m)
	var restored BuildScorer[float32]
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	for _, f := range features {
		a, _ := m.Predict(f)
		b, e := restored.Predict(f)
		if e != nil || a != b {
			t.Fatal("scorer serialization changed predictions", e)
		}
	}
	other, e := FitBuildScorer(context.Background(), features, scores, 250, 11)
	if e != nil || !reflect.DeepEqual(m, other) {
		t.Fatal("scorer refit is nondeterministic", e)
	}
	if _, e = m.Predict([]float32{0, 0, 0, float32(math.NaN())}); e == nil {
		t.Fatal("nonfinite input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = FitBuildScorer(ctx, features, scores, 1, 1); e == nil {
		t.Fatal("fit ignored cancellation")
	}
}
