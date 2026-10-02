package battlenet

import (
	"fmt"
	"math"
	"testing"
)

// Reference: cached forward with the original interleaved backward kernel.
func cachedForwardMatMul[T Real](g *Graph[T], a, b *Tensor[T]) *Tensor[T] {
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
		for i := 0; i < a.Rows; i++ {
			for k := 0; k < a.Cols; k++ {
				for j := 0; j < b.Cols; j++ {
					d := o.Grad[i*b.Cols+j]
					a.Grad[i*a.Cols+k] += d * b.Data[k*b.Cols+j]
					b.Grad[k*b.Cols+j] += d * a.Data[i*a.Cols+k]
				}
			}
		}
	})
	return o
}
func checkCandidateBackward[T Real](t *testing.T) {
	for _, s := range [][3]int{{1, 1, 1}, {2, 7, 5}, {1, 192, 64}, {20, 64, 128}, {40, 128, 64}, {8, 8, 8}} {
		for _, shared := range []bool{false, true} {
			if shared && (s[0] != s[1] || s[1] != s[2]) {
				continue
			}
			makeData := func(n int) []T {
				v := make([]T, n)
				for i := range v {
					v[i] = T(math.Sin(float64(i)) * math.Pow(10, float64(i%7-3)))
				}
				return v
			}
			eval := func(candidate bool) [][]T {
				g := &Graph[T]{Train: true}
				a, b := g.New(s[0], s[1], makeData(s[0]*s[1])), g.New(s[1], s[2], makeData(s[1]*s[2]))
				if shared {
					b = a
				}
				mul := func(a, b *Tensor[T]) *Tensor[T] { return cachedForwardMatMul(g, a, b) }
				if candidate {
					mul = func(a, b *Tensor[T]) *Tensor[T] { return g.MatMul(a, b) }
				}
				out := g.Add(mul(a, b), mul(a, b))
				loss := g.Sum(g.Mul(out, g.New(s[0], s[2], makeData(s[0]*s[2]))))
				g.Backward(loss)
				g.Backward(loss)
				return [][]T{out.Data, a.Grad, b.Grad, loss.Data}
			}
			a, b := eval(false), eval(true)
			for i := range a {
				for j := range a[i] {
					if math.Float64bits(float64(a[i][j])) != math.Float64bits(float64(b[i][j])) {
						t.Fatalf("shape=%v shared=%v buffer=%d entry=%d old=%v candidate=%v", s, shared, i, j, a[i][j], b[i][j])
					}
				}
			}
		}
	}
}
func TestCandidateBackwardBitwise(t *testing.T) {
	t.Run("float32", checkCandidateBackward[float32])
	t.Run("float64", checkCandidateBackward[float64])
}

func BenchmarkMatMulBackwardCandidate(b *testing.B) {
	for _, s := range [][3]int{{1, 192, 64}, {20, 64, 128}, {40, 128, 64}} {
		for _, candidate := range []bool{false, true} {
			b.Run(fmt.Sprintf("%dx%dx%d/candidate=%v", s[0], s[1], s[2], candidate), func(b *testing.B) {
				av, bv := make([]float32, s[0]*s[1]), make([]float32, s[1]*s[2])
				for i := range av {
					av[i] = float32(math.Sin(float64(i)))
				}
				for i := range bv {
					bv[i] = float32(math.Cos(float64(i)))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					g := &Graph[float32]{Train: true}
					a, c := g.New(s[0], s[1], av), g.New(s[1], s[2], bv)
					var out *Tensor[float32]
					if candidate {
						out = g.MatMul(a, c)
					} else {
						out = cachedForwardMatMul(g, a, c)
					}
					g.Backward(g.Sum(out))
				}
			})
		}
	}
}
