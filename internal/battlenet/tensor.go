// Package battlenet contains the small numerical kernel used by the local
// commander policy. Production uses float32; the identical generic operators
// support float64 finite-difference checks without a second implementation.
package battlenet

import "math"

type Real interface{ ~float32 | ~float64 }

type Tensor[T Real] struct {
	Rows, Cols int
	Data, Grad []T
}

type Graph[T Real] struct {
	Train    bool
	nodes    []*Tensor[T]
	backward []func()
}

func (g *Graph[T]) New(rows, cols int, data []T) *Tensor[T] {
	if rows < 1 || cols < 1 || data != nil && len(data) != rows*cols {
		panic("battlenet: invalid shape")
	}
	if data == nil {
		data = make([]T, rows*cols)
	}
	t := &Tensor[T]{Rows: rows, Cols: cols, Data: data}
	if g.Train {
		t.Grad = make([]T, len(data))
		g.nodes = append(g.nodes, t)
	}
	return t
}
func (g *Graph[T]) record(f func()) {
	if g.Train {
		g.backward = append(g.backward, f)
	}
}
func (g *Graph[T]) Backward(loss *Tensor[T]) {
	if !g.Train || len(loss.Data) != 1 {
		panic("battlenet: scalar training loss required")
	}
	for _, n := range g.nodes {
		clear(n.Grad)
	}
	loss.Grad[0] = 1
	for i := len(g.backward) - 1; i >= 0; i-- {
		g.backward[i]()
	}
}
func same[T Real](a, b *Tensor[T]) {
	if a.Rows != b.Rows || a.Cols != b.Cols {
		panic("battlenet: shape mismatch")
	}
}
func (g *Graph[T]) Add(a, b *Tensor[T]) *Tensor[T] {
	same(a, b)
	o := g.New(a.Rows, a.Cols, nil)
	for i := range o.Data {
		o.Data[i] = a.Data[i] + b.Data[i]
	}
	g.record(func() {
		for i, d := range o.Grad {
			a.Grad[i] += d
			b.Grad[i] += d
		}
	})
	return o
}
func (g *Graph[T]) Mul(a, b *Tensor[T]) *Tensor[T] {
	same(a, b)
	o := g.New(a.Rows, a.Cols, nil)
	for i := range o.Data {
		o.Data[i] = a.Data[i] * b.Data[i]
	}
	g.record(func() {
		for i, d := range o.Grad {
			a.Grad[i] += d * b.Data[i]
			b.Grad[i] += d * a.Data[i]
		}
	})
	return o
}
func (g *Graph[T]) Scale(a *Tensor[T], v T) *Tensor[T] {
	o := g.New(a.Rows, a.Cols, nil)
	for i := range o.Data {
		o.Data[i] = a.Data[i] * v
	}
	g.record(func() {
		for i, d := range o.Grad {
			a.Grad[i] += d * v
		}
	})
	return o
}
func (g *Graph[T]) unary(a *Tensor[T], forward func(float64) float64, derivative func(float64, float64) float64) *Tensor[T] {
	o := g.New(a.Rows, a.Cols, nil)
	for i, v := range a.Data {
		o.Data[i] = T(forward(float64(v)))
	}
	g.record(func() {
		for i, d := range o.Grad {
			a.Grad[i] += d * T(derivative(float64(a.Data[i]), float64(o.Data[i])))
		}
	})
	return o
}
func (g *Graph[T]) Tanh(a *Tensor[T]) *Tensor[T] {
	return g.unary(a, math.Tanh, func(x, y float64) float64 { return 1 - y*y })
}
func (g *Graph[T]) Sigmoid(a *Tensor[T]) *Tensor[T] {
	return g.unary(a, func(x float64) float64 {
		if x >= 0 {
			return 1 / (1 + math.Exp(-x))
		}
		v := math.Exp(x)
		return v / (1 + v)
	}, func(x, y float64) float64 { return y * (1 - y) })
}
func (g *Graph[T]) ReLU(a *Tensor[T]) *Tensor[T] {
	return g.unary(a, func(x float64) float64 { return math.Max(0, x) }, func(x, y float64) float64 {
		if x > 0 {
			return 1
		}
		return 0
	})
}
func (g *Graph[T]) Log(a *Tensor[T]) *Tensor[T] {
	return g.unary(a, math.Log, func(x, y float64) float64 { return 1 / x })
}
func (g *Graph[T]) Exp(a *Tensor[T]) *Tensor[T] {
	return g.unary(a, math.Exp, func(x, y float64) float64 { return y })
}
func (g *Graph[T]) Sum(a *Tensor[T]) *Tensor[T] {
	o := g.New(1, 1, nil)
	for _, v := range a.Data {
		o.Data[0] += v
	}
	g.record(func() {
		for i := range a.Grad {
			a.Grad[i] += o.Grad[0]
		}
	})
	return o
}
func (g *Graph[T]) MatMul(a, b *Tensor[T]) *Tensor[T] {
	if a.Cols != b.Rows {
		panic("battlenet: matmul shape mismatch")
	}
	o := g.New(a.Rows, b.Cols, nil)
	// Cache contiguous rows without changing the k reduction order. Existing
	// model and optimizer checkpoints must retain their numerical behavior.
	for i := 0; i < a.Rows; i++ {
		ar := a.Data[i*a.Cols : (i+1)*a.Cols]
		or := o.Data[i*b.Cols : (i+1)*b.Cols]
		for k, v := range ar {
			br := b.Data[k*b.Cols : (k+1)*b.Cols]
			for j, w := range br {
				or[j] += v * w
			}
		}
	}
	g.record(func() {
		// The two gradient accumulations alias for a shared operand. Preserve
		// their original interleaving in that case, including rounding order.
		if a == b {
			for i := 0; i < a.Rows; i++ {
				for k := 0; k < a.Cols; k++ {
					for j := 0; j < b.Cols; j++ {
						d := o.Grad[i*b.Cols+j]
						a.Grad[i*a.Cols+k] += d * b.Data[k*b.Cols+j]
						b.Grad[k*b.Cols+j] += d * a.Data[i*a.Cols+k]
					}
				}
			}
			return
		}
		for i := 0; i < a.Rows; i++ {
			ar := a.Data[i*a.Cols : (i+1)*a.Cols]
			ag := a.Grad[i*a.Cols : (i+1)*a.Cols]
			og := o.Grad[i*b.Cols : (i+1)*b.Cols]
			for k, v := range ar {
				br := b.Data[k*b.Cols : (k+1)*b.Cols]
				bg := b.Grad[k*b.Cols : (k+1)*b.Cols]
				// Retain the existing gradient and each j-ordered addition;
				// do not first sum from zero or change the reduction tree.
				total := ag[k]
				for j, d := range og {
					total += d * br[j]
					bg[j] += d * v
				}
				ag[k] = total
			}
		}
	})
	return o
}
func (g *Graph[T]) Transpose(a *Tensor[T]) *Tensor[T] {
	o := g.New(a.Cols, a.Rows, nil)
	for i := 0; i < a.Rows; i++ {
		for j := 0; j < a.Cols; j++ {
			o.Data[j*a.Rows+i] = a.Data[i*a.Cols+j]
		}
	}
	g.record(func() {
		for i := 0; i < a.Rows; i++ {
			for j := 0; j < a.Cols; j++ {
				a.Grad[i*a.Cols+j] += o.Grad[j*a.Rows+i]
			}
		}
	})
	return o
}

// Broadcast repeats a row for each entity. Gradients are summed, not averaged.
func (g *Graph[T]) Broadcast(a *Tensor[T], rows int) *Tensor[T] {
	if a.Rows != 1 {
		panic("battlenet: broadcast expects a row")
	}
	o := g.New(rows, a.Cols, nil)
	for i := 0; i < rows; i++ {
		copy(o.Data[i*a.Cols:], a.Data)
	}
	g.record(func() {
		for i, d := range o.Grad {
			a.Grad[i%a.Cols] += d
		}
	})
	return o
}
func (g *Graph[T]) Linear(a, w, b *Tensor[T]) *Tensor[T] {
	return g.Add(g.MatMul(a, w), g.Broadcast(b, a.Rows))
}

// Slice copies a rectangular selection; this keeps all graph buffers owned.
func (g *Graph[T]) Slice(a *Tensor[T], row, rows, col, cols int) *Tensor[T] {
	if row < 0 || col < 0 || rows < 1 || cols < 1 || row+rows > a.Rows || col+cols > a.Cols {
		panic("battlenet: slice outside tensor")
	}
	o := g.New(rows, cols, nil)
	for i := 0; i < rows; i++ {
		copy(o.Data[i*cols:(i+1)*cols], a.Data[(row+i)*a.Cols+col:(row+i)*a.Cols+col+cols])
	}
	g.record(func() {
		for i := 0; i < rows; i++ {
			for j := 0; j < cols; j++ {
				a.Grad[(row+i)*a.Cols+col+j] += o.Grad[i*cols+j]
			}
		}
	})
	return o
}
func (g *Graph[T]) Concat(a, b *Tensor[T]) *Tensor[T] {
	if a.Rows != b.Rows {
		panic("battlenet: concat row mismatch")
	}
	o := g.New(a.Rows, a.Cols+b.Cols, nil)
	for i := 0; i < a.Rows; i++ {
		copy(o.Data[i*o.Cols:], a.Data[i*a.Cols:(i+1)*a.Cols])
		copy(o.Data[i*o.Cols+a.Cols:], b.Data[i*b.Cols:(i+1)*b.Cols])
	}
	g.record(func() {
		for i := 0; i < a.Rows; i++ {
			for j := 0; j < o.Cols; j++ {
				if j < a.Cols {
					a.Grad[i*a.Cols+j] += o.Grad[i*o.Cols+j]
				} else {
					b.Grad[i*b.Cols+j-a.Cols] += o.Grad[i*o.Cols+j]
				}
			}
		}
	})
	return o
}

// MeanRows is permutation invariant over existing entities; padding is removed
// before this operator. It is not allowed to turn missing entities into zeros.
func (g *Graph[T]) MeanRows(a *Tensor[T]) *Tensor[T] {
	o := g.New(1, a.Cols, nil)
	for i, v := range a.Data {
		o.Data[i%a.Cols] += v / T(a.Rows)
	}
	g.record(func() {
		for i := range a.Grad {
			a.Grad[i] += o.Grad[i%a.Cols] / T(a.Rows)
		}
	})
	return o
}

// Softmax supports a per-element legal mask. An empty action row is an error,
// never an invented uniform distribution over illegal choices.
func (g *Graph[T]) Softmax(a *Tensor[T], mask []bool) *Tensor[T] {
	if mask != nil && len(mask) != len(a.Data) {
		panic("battlenet: softmax mask mismatch")
	}
	o := g.New(a.Rows, a.Cols, nil)
	for row := 0; row < a.Rows; row++ {
		start := row * a.Cols
		mx := math.Inf(-1)
		for j := 0; j < a.Cols; j++ {
			if mask == nil || mask[start+j] {
				mx = math.Max(mx, float64(a.Data[start+j]))
			}
		}
		if math.IsInf(mx, -1) || math.IsNaN(mx) {
			panic("battlenet: empty or nonfinite softmax")
		}
		sum := 0.
		for j := 0; j < a.Cols; j++ {
			if mask == nil || mask[start+j] {
				v := math.Exp(float64(a.Data[start+j]) - mx)
				o.Data[start+j] = T(v)
				sum += v
			}
		}
		for j := 0; j < a.Cols; j++ {
			o.Data[start+j] /= T(sum)
		}
	}
	g.record(func() {
		for row := 0; row < a.Rows; row++ {
			start := row * a.Cols
			dot := T(0)
			for j := 0; j < a.Cols; j++ {
				dot += o.Grad[start+j] * o.Data[start+j]
			}
			for j := 0; j < a.Cols; j++ {
				a.Grad[start+j] += o.Data[start+j] * (o.Grad[start+j] - dot)
			}
		}
	})
	return o
}

// LogSoftmax avoids log(0) on very unlikely but legal sampled actions. Masked
// entries are -Inf and must not be selected as a loss target.
func (g *Graph[T]) LogSoftmax(a *Tensor[T], mask []bool) *Tensor[T] {
	p := g.Softmax(a, mask)
	// Use stable logits directly rather than the rounded/underflowed probability.
	o := g.New(a.Rows, a.Cols, nil)
	for row := 0; row < a.Rows; row++ {
		start := row * a.Cols
		mx := math.Inf(-1)
		for j := 0; j < a.Cols; j++ {
			if mask == nil || mask[start+j] {
				mx = math.Max(mx, float64(a.Data[start+j]))
			}
		}
		sum := 0.
		for j := 0; j < a.Cols; j++ {
			if mask == nil || mask[start+j] {
				sum += math.Exp(float64(a.Data[start+j]) - mx)
			}
		}
		z := mx + math.Log(sum)
		for j := 0; j < a.Cols; j++ {
			if mask != nil && !mask[start+j] {
				o.Data[start+j] = T(math.Inf(-1))
			} else {
				o.Data[start+j] = T(float64(a.Data[start+j]) - z)
			}
		}
	}
	// Backprop to logits directly. p's own graph has no incoming derivative.
	g.record(func() {
		for row := 0; row < a.Rows; row++ {
			start := row * a.Cols
			sum := T(0)
			for j := 0; j < a.Cols; j++ {
				if mask == nil || mask[start+j] {
					sum += o.Grad[start+j]
				}
			}
			for j := 0; j < a.Cols; j++ {
				if mask == nil || mask[start+j] {
					a.Grad[start+j] += o.Grad[start+j] - p.Data[start+j]*sum
				}
			}
		}
	})
	return o
}
func (g *Graph[T]) LayerNorm(a, scale, bias *Tensor[T], epsilon float64) *Tensor[T] {
	if scale.Rows != 1 || bias.Rows != 1 || scale.Cols != a.Cols || bias.Cols != a.Cols || epsilon <= 0 {
		panic("battlenet: invalid layer norm")
	}
	o := g.New(a.Rows, a.Cols, nil)
	normalized := make([]T, len(a.Data))
	inv := make([]T, a.Rows)
	for i := 0; i < a.Rows; i++ {
		mean, variance := 0., 0.
		for j := 0; j < a.Cols; j++ {
			mean += float64(a.Data[i*a.Cols+j]) / float64(a.Cols)
		}
		for j := 0; j < a.Cols; j++ {
			d := float64(a.Data[i*a.Cols+j]) - mean
			variance += d * d / float64(a.Cols)
		}
		inv[i] = T(1 / math.Sqrt(variance+epsilon))
		for j := 0; j < a.Cols; j++ {
			k := i*a.Cols + j
			normalized[k] = T(float64(a.Data[k])-mean) * inv[i]
			o.Data[k] = normalized[k]*scale.Data[j] + bias.Data[j]
		}
	}
	g.record(func() {
		for i := 0; i < a.Rows; i++ {
			sum, projected := T(0), T(0)
			for j := 0; j < a.Cols; j++ {
				k := i*a.Cols + j
				d := o.Grad[k] * scale.Data[j]
				sum += d
				projected += d * normalized[k]
				scale.Grad[j] += o.Grad[k] * normalized[k]
				bias.Grad[j] += o.Grad[k]
			}
			for j := 0; j < a.Cols; j++ {
				k := i*a.Cols + j
				a.Grad[k] += inv[i] * (o.Grad[k]*scale.Data[j] - (sum+normalized[k]*projected)/T(a.Cols))
			}
		}
	})
	return o
}

func (g *Graph[T]) Stack(parts ...*Tensor[T]) *Tensor[T] {
	if len(parts) == 0 {
		panic("battlenet: empty stack")
	}
	cols, rows := parts[0].Cols, 0
	for _, p := range parts {
		if p.Cols != cols {
			panic("battlenet: stack column mismatch")
		}
		rows += p.Rows
	}
	o := g.New(rows, cols, nil)
	offset := 0
	for _, p := range parts {
		copy(o.Data[offset:], p.Data)
		offset += len(p.Data)
	}
	g.record(func() {
		offset := 0
		for _, p := range parts {
			for i := range p.Grad {
				p.Grad[i] += o.Grad[offset+i]
			}
			offset += len(p.Data)
		}
	})
	return o
}
