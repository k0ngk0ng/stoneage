package battlenet

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestAdamCheckpointAndRollback(t *testing.T) {
	makeModel := func() *Model[float32] {
		return &Model[float32]{Parameters: map[string]Parameter[float32]{"x": {1, 2, []float32{2, -3}}}}
	}
	a := &Adam[float32]{}
	m := makeModel()
	for i := 0; i < 20; i++ {
		p := m.Parameters["x"]
		if _, e := a.Update(m, map[string][]float32{"x": {2 * p.Values[0], 2 * p.Values[1]}}, 1, .1, 1); e != nil {
			t.Fatal(e)
		}
	}
	if math.Abs(float64(m.Parameters["x"].Values[0])) >= 2 || math.Abs(float64(m.Parameters["x"].Values[1])) >= 3 {
		t.Fatal("optimizer did not descend")
	}
	raw, _ := json.Marshal(a)
	var resumed Adam[float32]
	if e := json.Unmarshal(raw, &resumed); e != nil {
		t.Fatal(e)
	}
	copyRaw, _ := json.Marshal(m)
	var copied Model[float32]
	if e := json.Unmarshal(copyRaw, &copied); e != nil {
		t.Fatal(e)
	}
	gradient := map[string][]float32{"x": {.3, -.4}}
	if _, e := a.Update(m, gradient, 1, .1, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := resumed.Update(&copied, gradient, 1, .1, 1); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(m, &copied) || !reflect.DeepEqual(a, &resumed) {
		t.Fatal("resuming optimizer changed update")
	}
	before, _ := json.Marshal(m)
	state, _ := json.Marshal(a)
	if _, e := a.Update(m, map[string][]float32{"x": {float32(math.NaN()), 1}}, 1, .1, 1); e == nil {
		t.Fatal("nonfinite update accepted")
	}
	after, _ := json.Marshal(m)
	newState, _ := json.Marshal(a)
	if string(before) != string(after) || string(state) != string(newState) {
		t.Fatal("failed update mutated state")
	}
}
func TestGAETerminalAndTruncation(t *testing.T) {
	a, r, e := Advantages([]float64{0, 1}, []float64{.2, .3}, 99, true, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if math.Abs(a[0]-.8) > 1e-12 || math.Abs(a[1]-.7) > 1e-12 || math.Abs(r[0]-1) > 1e-12 || math.Abs(r[1]-1) > 1e-12 {
		t.Fatal(a, r)
	}
	a, r, e = Advantages([]float64{0, 0}, []float64{.2, .3}, .7, false, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if math.Abs(a[0]-.5) > 1e-12 || math.Abs(r[0]-.7) > 1e-12 {
		t.Fatal(a, r)
	}
}
func TestPPOClippingSigns(t *testing.T) {
	for _, c := range []struct{ ratio, advantage, gradient float64 }{{1.5, 1, 0}, {.5, -1, 0}, {.5, 1, -.5}, {1.5, -1, 1.5}, {1, 1, -1}} {
		g := &Graph[float64]{Train: true}
		x := g.New(1, 1, []float64{math.Log(c.ratio)})
		loss, e := PPOLoss(g, x, 0, c.advantage, .2)
		if e != nil {
			t.Fatal(e)
		}
		g.Backward(loss)
		if math.Abs(x.Grad[0]-c.gradient) > 1e-12 {
			t.Fatal(c, x.Grad)
		}
	}
	g := &Graph[float32]{Train: true}
	x := g.New(1, 3, []float32{0, 0, 100})
	h := Entropy(g, x, []bool{true, true, false})
	g.Backward(h)
	if math.Abs(float64(h.Data[0])-math.Log(2)) > 1e-6 || x.Grad[2] != 0 {
		t.Fatal(h.Data, x.Grad)
	}
}
