package battlenet

import (
	"fmt"
	"math"
)

type Moments[T Real] struct {
	First  []T `json:"first"`
	Second []T `json:"second"`
}
type Adam[T Real] struct {
	Step    int                   `json:"step"`
	Moments map[string]Moments[T] `json:"moments"`
}

// Update averages the batch and clips the global gradient norm. It commits
// weights and optimizer state only after validating the whole update, so a
// single nonfinite sample cannot leave a half-updated model/checkpoint.
func (a *Adam[T]) Update(m *Model[T], gradients map[string][]T, batch int, rate, clip float64) (float64, error) {
	parameters, norm, e := a.UpdateParameters(m.Parameters, gradients, batch, rate, clip)
	if e == nil {
		m.Parameters = parameters
	}
	return norm, e
}

// UpdateParameters shares the exact optimizer with the small build scorer.
// It accepts named parameter tensors without assuming a commander topology.
func (a *Adam[T]) UpdateParameters(parameters map[string]Parameter[T], gradients map[string][]T, batch int, rate, clip float64) (map[string]Parameter[T], float64, error) {
	m := &Model[T]{Parameters: parameters}
	if len(parameters) == 0 {
		return nil, 0, fmt.Errorf("optimizer requires parameters")
	}
	for name, p := range parameters {
		if name == "" || p.Rows < 1 || p.Cols < 1 || p.Rows > 1000000 || p.Cols > 1000000 || p.Rows*p.Cols != len(p.Values) {
			return nil, 0, fmt.Errorf("invalid optimizer parameter shape: %s", name)
		}
	}
	if batch < 1 || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || clip <= 0 || math.IsNaN(clip) || math.IsInf(clip, 0) || a.Step < 0 || a.Step >= 1000000000 {
		return nil, 0, fmt.Errorf("invalid optimizer settings")
	}
	norm := 0.
	for name, g := range gradients {
		p, ok := m.Parameters[name]
		if !ok || len(g) != len(p.Values) {
			return nil, 0, fmt.Errorf("gradient shape mismatch: %s", name)
		}
	}
	for name := range a.Moments {
		if _, ok := m.Parameters[name]; !ok {
			return nil, 0, fmt.Errorf("unknown optimizer parameter: %s", name)
		}
	}
	for _, name := range m.Names() {
		g := gradients[name]
		for _, v := range g {
			x := float64(v) / float64(batch)
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return nil, 0, fmt.Errorf("nonfinite gradient: %s", name)
			}
			norm += x * x
		}
	}
	if len(gradients) == 0 || math.IsInf(norm, 0) {
		return nil, 0, fmt.Errorf("empty or overflowing gradient")
	}
	norm = math.Sqrt(norm)
	scale := 1 / float64(batch)
	if norm > clip {
		scale *= clip / norm
	}
	next := a.Step + 1
	correction1 := 1 - math.Pow(.9, float64(next))
	correction2 := 1 - math.Pow(.999, float64(next))
	weights := map[string]Parameter[T]{}
	moments := map[string]Moments[T]{}
	for _, name := range m.Names() {
		p := m.Parameters[name]
		old, exists := a.Moments[name]
		if exists && (len(old.First) != len(p.Values) || len(old.Second) != len(p.Values)) {
			return nil, 0, fmt.Errorf("optimizer state shape mismatch: %s", name)
		}
		if !exists && a.Step != 0 {
			return nil, 0, fmt.Errorf("missing resumed optimizer state: %s", name)
		}
		nm := Moments[T]{make([]T, len(p.Values)), make([]T, len(p.Values))}
		w := Parameter[T]{p.Rows, p.Cols, make([]T, len(p.Values))}
		gradient := gradients[name]
		for i, v := range p.Values {
			g, f, s := 0., 0., 0.
			if gradient != nil {
				g = float64(gradient[i]) * scale
			}
			if exists {
				f = float64(old.First[i])
				s = float64(old.Second[i])
			}
			if s < 0 || math.IsNaN(f) || math.IsNaN(s) || math.IsInf(f, 0) || math.IsInf(s, 0) {
				return nil, 0, fmt.Errorf("invalid optimizer moments: %s", name)
			}
			f = .9*f + .1*g
			s = .999*s + .001*g*g
			value := float64(v) - rate*(f/correction1)/(math.Sqrt(s/correction2)+1e-8)
			nm.First[i] = T(f)
			nm.Second[i] = T(s)
			w.Values[i] = T(value)
			for _, n := range []T{nm.First[i], nm.Second[i], w.Values[i]} {
				if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
					return nil, 0, fmt.Errorf("nonfinite optimizer update: %s", name)
				}
			}
		}
		weights[name] = w
		moments[name] = nm
	}
	a.Moments = moments
	a.Step = next
	return weights, norm, nil
}
