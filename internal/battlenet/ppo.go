package battlenet

import (
	"fmt"
	"math"
)

// Advantages operates on one contiguous trajectory segment. A true terminal
// has zero bootstrap. A collection truncation bootstraps from the next visible
// state, but the GAE recursion never crosses into the next match.
func Advantages(rewards, values []float64, bootstrap float64, terminated bool, gamma, lambda float64) (advantages, returns []float64, err error) {
	finite := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
	if len(rewards) == 0 || len(rewards) != len(values) || !finite(bootstrap) || !finite(gamma) || !finite(lambda) || gamma < 0 || gamma > 1 || lambda < 0 || lambda > 1 {
		return nil, nil, fmt.Errorf("invalid trajectory for GAE")
	}
	if terminated {
		bootstrap = 0
	}
	advantages = make([]float64, len(rewards))
	returns = make([]float64, len(rewards))
	next, gae := bootstrap, 0.
	for i := len(rewards) - 1; i >= 0; i-- {
		if !finite(rewards[i]) || !finite(values[i]) {
			return nil, nil, fmt.Errorf("nonfinite trajectory")
		}
		delta := rewards[i] + gamma*next - values[i]
		gae = delta + gamma*lambda*gae
		advantages[i] = gae
		returns[i] = gae + values[i]
		next = values[i]
	}
	return advantages, returns, nil
}

// PPOLoss consumes the SUM of conditional log probabilities for the whole
// joint plan, not separate per-member importance ratios. The selected clipped
// branch has zero gradient, including the easily missed negative-advantage case.
func PPOLoss[T Real](g *Graph[T], jointLogProb *Tensor[T], oldLogProb, advantage, clip float64) (*Tensor[T], error) {
	if len(jointLogProb.Data) != 1 || clip <= 0 || clip >= 1 || math.IsNaN(clip) || math.IsNaN(advantage) || math.IsInf(advantage, 0) || math.IsNaN(oldLogProb) || math.IsInf(oldLogProb, 0) {
		return nil, fmt.Errorf("invalid PPO sample")
	}
	delta := float64(jointLogProb.Data[0]) - oldLogProb
	if math.IsNaN(delta) || math.IsInf(delta, 0) || math.Abs(delta) > 60 {
		return nil, fmt.Errorf("PPO probability ratio outside numerical trust range")
	}
	ratio := math.Exp(delta)
	if advantage >= 0 && ratio > 1+clip || advantage < 0 && ratio < 1-clip {
		return g.New(1, 1, []T{T(-advantage * max(1-clip, min(1+clip, ratio)))}), nil
	}
	return g.Scale(g.Exp(g.Add(jointLogProb, g.New(1, 1, []T{T(-oldLogProb)}))), T(-advantage)), nil
}

// Entropy excludes impossible actions explicitly; 0 * -Inf is not a valid
// entropy implementation. Legal tiny probabilities use finite log-softmax.
func Entropy[T Real](g *Graph[T], logits *Tensor[T], mask []bool) *Tensor[T] {
	if logits.Rows != 1 {
		panic("battlenet: entropy expects one action distribution")
	}
	logp := g.LogSoftmax(logits, mask)
	p := g.Softmax(logits, mask)
	sum := g.New(1, 1, nil)
	for i := 0; i < logits.Cols; i++ {
		if mask == nil || mask[i] {
			sum = g.Add(sum, g.Mul(g.Slice(p, 0, 1, i, 1), g.Slice(logp, 0, 1, i, 1)))
		}
	}
	return g.Scale(sum, -1)
}
