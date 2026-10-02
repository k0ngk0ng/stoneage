package battlenet

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// Preserve the original reduction and gradient accumulation order as a
// compatibility oracle. Existing model/checkpoint results must not drift.
func originalMatMul[T Real](g *Graph[T], a, b *Tensor[T]) *Tensor[T] {
	o := g.New(a.Rows, b.Cols, nil)
	for i := 0; i < a.Rows; i++ {
		for k := 0; k < a.Cols; k++ {
			v := a.Data[i*a.Cols+k]
			for j := 0; j < b.Cols; j++ {
				o.Data[i*b.Cols+j] += v * b.Data[k*b.Cols+j]
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

func checkMatMulEquivalence[T Real](t *testing.T) {
	for _, shape := range [][3]int{{1, 1, 1}, {2, 7, 5}, {1, 192, 64}, {20, 64, 128}, {40, 128, 64}, {8, 8, 8}} {
		for _, shared := range []bool{false, true} {
			if shared && (shape[0] != shape[1] || shape[1] != shape[2]) {
				continue
			}
			t.Run(fmt.Sprint(shape, shared), func(t *testing.T) {
				rng := rand.New(rand.NewSource(701))
				data := func(n int) []T {
					x := make([]T, n)
					for i := range x {
						x[i] = T(rng.NormFloat64() * math.Pow(10, float64(i%7-3)))
					}
					return x
				}
				av, bv, weights := data(shape[0]*shape[1]), data(shape[1]*shape[2]), data(shape[0]*shape[2])
				evaluate := func(old, train bool) [][]T {
					g := &Graph[T]{Train: train}
					a := g.New(shape[0], shape[1], append([]T(nil), av...))
					b := g.New(shape[1], shape[2], append([]T(nil), bv...))
					if shared {
						b = a
					}
					mul := g.MatMul
					if old {
						mul = func(a, b *Tensor[T]) *Tensor[T] { return originalMatMul(g, a, b) }
					}
					// Repeated operands exercise accumulation from multiple graph branches.
					out := g.Add(mul(a, b), mul(a, b))
					loss := g.Sum(g.Mul(out, g.New(shape[0], shape[2], weights)))
					if train {
						g.Backward(loss)
						g.Backward(loss)
					}
					return [][]T{out.Data, a.Grad, b.Grad, loss.Data}
				}
				for _, train := range []bool{false, true} {
					old, next := evaluate(true, train), evaluate(false, train)
					for i := range old {
						for j := range old[i] {
							// Float64 conversion is exact for float32, retaining signed zero.
							if math.Float64bits(float64(old[i][j])) != math.Float64bits(float64(next[i][j])) {
								t.Fatalf("train=%v buffer=%d entry=%d old=%v new=%v", train, i, j, old[i][j], next[i][j])
							}
						}
					}
				}
			})
		}
	}
}
func TestMatMulOriginalBitwiseCompatibility(t *testing.T) {
	t.Run("float32", checkMatMulEquivalence[float32])
	t.Run("float64", checkMatMulEquivalence[float64])
}

func BenchmarkMatMulRows(b *testing.B) {
	for _, shape := range [][3]int{{1, 192, 64}, {20, 64, 128}, {40, 128, 64}} {
		for _, train := range []bool{false, true} {
			for _, old := range []bool{true, false} {
				b.Run(fmt.Sprintf("%dx%dx%d/train=%v/original=%v", shape[0], shape[1], shape[2], train, old), func(b *testing.B) {
					av, bv := make([]float32, shape[0]*shape[1]), make([]float32, shape[1]*shape[2])
					for i := range av {
						av[i] = float32(math.Sin(float64(i)))
					}
					for i := range bv {
						bv[i] = float32(math.Cos(float64(i)))
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						g := &Graph[float32]{Train: train}
						a, c := g.New(shape[0], shape[1], av), g.New(shape[1], shape[2], bv)
						var out *Tensor[float32]
						if old {
							out = originalMatMul(g, a, c)
						} else {
							out = g.MatMul(a, c)
						}
						if train {
							g.Backward(g.Sum(out))
						}
					}
				})
			}
		}
	}
}
