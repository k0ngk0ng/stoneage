package battlenet

import (
	"context"
	"fmt"
	"math"
	"math/rand"
)

// BuildScorer is a separate 64/64 MLP for screening integer own-team builds.
// Context (frozen combat policies, rules, budget, opponent distribution) is
// bound by its enclosing search artifact, never inferred from hidden enemies.
type BuildScorer[T Real] struct {
	Schema     string                  `json:"schema"`
	Inputs     int                     `json:"inputs"`
	Parameters map[string]Parameter[T] `json:"parameters"`
}

func NewBuildScorer[T Real](inputs int, seed int64) (*BuildScorer[T], error) {
	valid := false
	for mode := 1; mode <= 5; mode++ {
		// Player only; player + active pet; and one/two reserves with
		// four normalized allocations and seven legacy or eight explicitly
		// configured skill bits each. The enclosing search binds the layout.
		for _, width := range []int{4, 8, 19, 30, 20, 32} {
			valid = valid || inputs == mode*width
		}
	}
	if !valid {
		return nil, fmt.Errorf("build scorer requires a supported 1..5-member own-roster shape")
	}
	m := &BuildScorer[T]{Schema: "build-score-mlp-v1", Inputs: inputs, Parameters: map[string]Parameter[T]{}}
	rng := rand.New(rand.NewSource(seed))
	for i, shape := range [][2]int{{inputs, 64}, {64, 64}, {64, 1}} {
		name := fmt.Sprintf("build.%d", i)
		p := Parameter[T]{Rows: shape[0], Cols: shape[1], Values: make([]T, shape[0]*shape[1])}
		for j := range p.Values {
			p.Values[j] = T(rng.NormFloat64() * math.Sqrt(2/float64(shape[0]+shape[1])))
		}
		m.Parameters[name+".w"] = p
		m.Parameters[name+".b"] = Parameter[T]{1, shape[1], make([]T, shape[1])}
	}
	return m, nil
}

func (m *BuildScorer[T]) Validate() error {
	if m == nil || m.Schema != "build-score-mlp-v1" {
		return fmt.Errorf("invalid build scorer")
	}
	expected, e := NewBuildScorer[T](m.Inputs, 0)
	if e != nil {
		return e
	}
	if len(m.Parameters) != len(expected.Parameters) {
		return fmt.Errorf("build scorer parameter set mismatch")
	}
	for name, want := range expected.Parameters {
		p, ok := m.Parameters[name]
		if !ok || p.Rows != want.Rows || p.Cols != want.Cols || len(p.Values) != len(want.Values) {
			return fmt.Errorf("build scorer shape mismatch")
		}
		for _, v := range p.Values {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return fmt.Errorf("nonfinite build scorer")
			}
		}
	}
	return nil
}

func (m *BuildScorer[T]) forward(g *Graph[T], x *Tensor[T]) (*Tensor[T], map[string]*Tensor[T]) {
	bound := map[string]*Tensor[T]{}
	param := func(name string) *Tensor[T] {
		p := m.Parameters[name]
		t := g.New(p.Rows, p.Cols, p.Values)
		bound[name] = t
		return t
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("build.%d", i)
		x = g.Linear(x, param(name+".w"), param(name+".b"))
		if i < 2 {
			x = g.ReLU(x)
		}
	}
	return g.Sigmoid(x), bound
}

func (m *BuildScorer[T]) Predict(features []T) (T, error) {
	if e := m.Validate(); e != nil {
		return 0, e
	}
	if len(features) != m.Inputs {
		return 0, fmt.Errorf("build scorer input shape mismatch")
	}
	for _, v := range features {
		if v < 0 || v > 1 || math.IsNaN(float64(v)) {
			return 0, fmt.Errorf("build fractions must be finite and in 0..1")
		}
	}
	g := &Graph[T]{}
	p, _ := m.forward(g, g.New(1, m.Inputs, features))
	if math.IsNaN(float64(p.Data[0])) || math.IsInf(float64(p.Data[0]), 0) {
		return 0, fmt.Errorf("nonfinite build prediction")
	}
	return p.Data[0], nil
}

// FitBuildScorer creates fresh deterministic weights and fits complete-game
// aggregate scores. Validation/test outcomes must never be passed here.
func FitBuildScorer(ctx context.Context, features [][]float32, scores []float32, epochs int, seed int64) (*BuildScorer[float32], error) {
	if len(features) < 2 || len(features) != len(scores) || len(features) > 4096 || epochs < 1 || epochs > 1000 {
		return nil, fmt.Errorf("build fitting requires 2..4096 samples and 1..1000 epochs")
	}
	m, e := NewBuildScorer[float32](len(features[0]), seed)
	if e != nil {
		return nil, e
	}
	var x []float32
	for i, f := range features {
		if len(f) != m.Inputs || scores[i] < 0 || scores[i] > 1 || math.IsNaN(float64(scores[i])) {
			return nil, fmt.Errorf("invalid build training sample")
		}
		for _, v := range f {
			if v < 0 || v > 1 || math.IsNaN(float64(v)) {
				return nil, fmt.Errorf("invalid build feature")
			}
		}
		x = append(x, f...)
	}
	a := &Adam[float32]{}
	for epoch := 0; epoch < epochs; epoch++ {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		g := &Graph[float32]{Train: true}
		p, bound := m.forward(g, g.New(len(scores), m.Inputs, x))
		difference := g.Add(p, g.Scale(g.New(len(scores), 1, scores), -1))
		loss := g.Sum(g.Mul(difference, difference))
		g.Backward(loss)
		gradients := map[string][]float32{}
		for name, t := range bound {
			gradients[name] = t.Grad
		}
		m.Parameters, _, e = a.UpdateParameters(m.Parameters, gradients, len(scores), .001, .5)
		if e != nil {
			return nil, e
		}
	}
	return m, m.Validate()
}
