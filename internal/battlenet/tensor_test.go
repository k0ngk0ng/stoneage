package battlenet

import (
	"math"
	"testing"
)

type tensorSpec struct {
	r, c   int
	values []float64
}

func TestOperatorGradients(t *testing.T) {
	cases := []struct {
		name   string
		inputs []tensorSpec
		build  func(*Graph[float64], []*Tensor[float64]) *Tensor[float64]
	}{
		{"linear", []tensorSpec{{2, 3, []float64{.2, -.3, .7, .4, .8, -.9}}, {3, 2, []float64{.1, .4, -.2, .6, .3, -.1}}, {1, 2, []float64{.2, -.1}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			y := g.Linear(x[0], x[1], x[2])
			return g.Sum(g.Mul(y, y))
		}},
		{"activations", []tensorSpec{{1, 4, []float64{-.3, .7, -1.2, 2.1}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			return g.Sum(g.Mul(g.Tanh(x[0]), g.Add(g.Sigmoid(x[0]), g.ReLU(x[0]))))
		}},
		{"softmax", []tensorSpec{{2, 3, []float64{.5, 3, -.1, -1, 4, .3}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			y := g.Softmax(x[0], []bool{true, false, true, true, true, false})
			return g.Sum(g.Mul(y, g.New(2, 3, []float64{1, 2, 3, 4, 5, 6})))
		}},
		{"log-softmax", []tensorSpec{{2, 3, []float64{.5, 3, -.1, -1, 4, .3}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			y := g.LogSoftmax(x[0], []bool{true, false, true, true, true, false})
			return g.Add(g.Slice(y, 0, 1, 2, 1), g.Slice(y, 1, 1, 0, 1))
		}},
		{"layernorm", []tensorSpec{{2, 3, []float64{.5, 3, -.1, -1, 4, .3}}, {1, 3, []float64{.7, 1.1, .3}}, {1, 3, []float64{.1, .2, .3}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			y := g.LayerNorm(x[0], x[1], x[2], 1e-5)
			return g.Sum(g.Mul(y, y))
		}},
		{"slice-concat-mean", []tensorSpec{{3, 2, []float64{.5, 3, -.1, -1, 4, .3}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			a := g.Slice(x[0], 0, 2, 0, 2)
			b := g.Slice(x[0], 1, 2, 0, 2)
			return g.Sum(g.Transpose(g.MeanRows(g.Concat(a, b))))
		}},
		{"log-exp", []tensorSpec{{1, 3, []float64{.3, .8, 1.5}}}, func(g *Graph[float64], x []*Tensor[float64]) *Tensor[float64] {
			return g.Sum(g.Log(g.Exp(g.Scale(x[0], .2))))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evaluate := func(train bool) (float64, []*Tensor[float64]) {
				g := &Graph[float64]{Train: train}
				var x []*Tensor[float64]
				for _, in := range tc.inputs {
					x = append(x, g.New(in.r, in.c, append([]float64(nil), in.values...)))
				}
				loss := tc.build(g, x)
				if train {
					g.Backward(loss)
				}
				return loss.Data[0], x
			}
			_, analytical := evaluate(true)
			const eps = 1e-5
			for i, in := range tc.inputs {
				for j, v := range in.values {
					tc.inputs[i].values[j] = v + eps
					plus, _ := evaluate(false)
					tc.inputs[i].values[j] = v - eps
					minus, _ := evaluate(false)
					tc.inputs[i].values[j] = v
					numerical := (plus - minus) / (2 * eps)
					actual := analytical[i].Grad[j]
					if math.IsNaN(actual) || math.Abs(actual-numerical) > 1e-6*(1+math.Abs(numerical)) {
						t.Fatalf("input %d element %d gradient=%g finite-difference=%g", i, j, actual, numerical)
					}
				}
			}
		})
	}
}

func TestFloat32StableLogSoftmax(t *testing.T) {
	g := &Graph[float32]{Train: true}
	x := g.New(1, 3, []float32{1000, -1000, 0})
	p := g.LogSoftmax(x, []bool{true, true, false})
	if p.Data[0] != 0 || p.Data[1] != -2000 || !math.IsInf(float64(p.Data[2]), -1) {
		t.Fatal(p.Data)
	}
	g.Backward(g.Scale(g.Slice(p, 0, 1, 1, 1), -1))
	if x.Grad[0] != 1 || x.Grad[1] != -1 || x.Grad[2] != 0 {
		t.Fatal(x.Grad)
	}
}
func TestEmptyMaskRejected(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("empty action mask accepted")
		}
	}()
	g := &Graph[float32]{}
	g.Softmax(g.New(1, 2, []float32{1, 2}), []bool{false, false})
}
