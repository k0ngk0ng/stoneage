package battletrain

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type Policy struct {
	Model  *battlenet.Model[float32]
	Rule   string
	Greedy bool
	// Only rules need an explicit observation contract; network policies bind
	// it in their configuration. Rule behavior ignores history features.
	Features string
}

func (p Policy) features() string {
	if p.Model != nil {
		return battlepolicy.NetworkFeatures(p.Model.Config)
	}
	if p.Features != "" {
		return p.Features
	}
	return battlepolicy.FeatureVersion
}

func (p Policy) Kind() string {
	if p.Rule != "" {
		return "rule:" + p.Rule
	}
	if p.Greedy {
		return "network-greedy"
	}
	return "network-sampled"
}
func (p Policy) Version() (string, error) {
	if !battlepolicy.SupportedFeatures(p.features()) || p.Model != nil && p.Features != "" {
		return "", fmt.Errorf("unsupported or overridden policy feature contract")
	}
	if p.Rule != "" {
		if p.Model != nil || p.Greedy || !battlepolicy.RuleSupported(p.Rule) {
			return "", fmt.Errorf("invalid rule policy")
		}
		return Digest(struct{ Schema, Rule string }{"observed-rule-v1", p.Rule})
	}
	id, e := ModelDigest(p.Model)
	if e != nil {
		return "", e
	}
	if p.Greedy {
		return Digest(struct{ Network, Mode string }{id, "conditional-greedy-v1"})
	}
	return id, nil
}

type policyDecision struct {
	choices        []int
	conditional    []float32
	logProb, value float32
	memory         []float32
}

func (p Policy) decide(ctx context.Context, f battlepolicy.Frame, memory []float32, rng *rand.Rand) (policyDecision, error) {
	if e := ctx.Err(); e != nil {
		return policyDecision{}, e
	}
	if p.Rule != "" {
		choices, e := battlepolicy.RuleChoices(f, p.Rule)
		return policyDecision{choices: choices, conditional: make([]float32, len(choices))}, e
	}
	if !p.Greedy && rng == nil {
		return policyDecision{}, fmt.Errorf("sampled policy requires RNG")
	}
	if p.Greedy {
		rng = nil
	}
	g := &battlenet.Graph[float32]{}
	o, e := battlepolicy.Forward(ctx, p.Model.Bind(g), f, g.New(1, p.Model.Config.Width, memory), nil, rng)
	if e != nil {
		return policyDecision{}, e
	}
	d := policyDecision{choices: o.Choices, conditional: o.ConditionalLogProbs, logProb: o.LogProb.Data[0], value: o.Value.Data[0], memory: append([]float32(nil), o.Memory.Data...)}
	// Greedy behavior is a point mass, not a sample from softmax. Its
	// trajectories carry a distinct identity and cannot enter on-policy PPO.
	if p.Greedy {
		d.logProb = 0
		d.conditional = make([]float32, len(d.choices))
	}
	return d, nil
}
func (p Policy) value(ctx context.Context, f battlepolicy.Frame, memory []float32) (float64, error) {
	if p.Rule != "" {
		return 0, nil
	}
	g := &battlenet.Graph[float32]{}
	o, e := battlepolicy.Forward(ctx, p.Model.Bind(g), f, g.New(1, p.Model.Config.Width, memory), nil, nil)
	if e != nil {
		return 0, e
	}
	return float64(o.Value.Data[0]), nil
}
