package battlenet

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type Parameter[T Real] struct {
	Rows   int `json:"rows"`
	Cols   int `json:"cols"`
	Values []T `json:"values"`
}

type Config struct {
	// Empty preserves the original learned GRU-only plan representation.
	PlanFeatures string `json:"plan_features,omitempty"`
	// Empty preserves the original centralized plan. "member" is an offline
	// ablation: shared weights/observation, separate per-member plan prefixes.
	PlanScope         string `json:"plan_scope,omitempty"`
	EntityFeatures    int    `json:"entity_features"`
	CandidateFeatures int    `json:"candidate_features"`
	EventFeatures     int    `json:"event_features"`
	Width             int    `json:"width"`
	Heads             int    `json:"heads"`
	Layers            int    `json:"layers"`
	// Opaque application input contract, included in model/config digests.
	// Omission preserves serialized legacy models; numerical kernels do not
	// interpret this label or silently change its value.
	InputSchema string `json:"input_schema,omitempty"`
}

func (c Config) Validate() error {
	if c.PlanFeatures != "" && c.PlanFeatures != "target-counts-v1" {
		return fmt.Errorf("invalid network plan features %q", c.PlanFeatures)
	}
	if c.PlanScope != "" && c.PlanScope != "member" {
		return fmt.Errorf("invalid network plan scope %q", c.PlanScope)
	}
	if len(c.InputSchema) > 128 {
		return fmt.Errorf("network input schema is too long")
	}
	if c.EntityFeatures < 1 || c.EntityFeatures > 512 || c.CandidateFeatures < 1 || c.CandidateFeatures > 512 || c.EventFeatures < 1 || c.EventFeatures > 256 || c.Width < 4 || c.Width > 256 || c.Heads < 1 || c.Heads > 8 || c.Width%c.Heads != 0 || c.Layers < 1 || c.Layers > 4 {
		return fmt.Errorf("invalid commander network dimensions")
	}
	return nil
}

type Model[T Real] struct {
	Config     Config                  `json:"config"`
	Parameters map[string]Parameter[T] `json:"parameters"`
}

func NewModel[T Real](c Config, seed int64) (*Model[T], error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	m := &Model[T]{Config: c, Parameters: map[string]Parameter[T]{}}
	rng := rand.New(rand.NewSource(seed))
	linear := func(name string, in, out int) {
		values := make([]T, in*out)
		scale := math.Sqrt(2 / float64(in+out))
		for i := range values {
			values[i] = T(rng.NormFloat64() * scale)
		}
		m.Parameters[name+".w"] = Parameter[T]{in, out, values}
		m.Parameters[name+".b"] = Parameter[T]{1, out, make([]T, out)}
	}
	norm := func(name string) {
		v := make([]T, c.Width)
		for i := range v {
			v[i] = 1
		}
		m.Parameters[name+".scale"] = Parameter[T]{1, c.Width, v}
		m.Parameters[name+".bias"] = Parameter[T]{1, c.Width, make([]T, c.Width)}
	}
	gru := func(name string, input int) {
		linear(name+".gates", input+c.Width, c.Width*2)
		linear(name+".candidate", input+c.Width, c.Width)
	}
	linear("entity.0", c.EntityFeatures, c.Width)
	linear("entity.1", c.Width, c.Width)
	for i := 0; i < c.Layers; i++ {
		p := fmt.Sprintf("block.%d", i)
		for _, part := range []string{"q", "k", "v", "out"} {
			linear(p+"."+part, c.Width, c.Width)
		}
		linear(p+".ff0", c.Width, c.Width*2)
		linear(p+".ff1", c.Width*2, c.Width)
		norm(p + ".norm0")
		norm(p + ".norm1")
	}
	gru("history", c.Width+c.EventFeatures)
	gru("plan", c.Width)
	linear("candidate.0", c.CandidateFeatures, c.Width)
	linear("candidate.1", c.Width, c.Width)
	linear("plan.context", c.Width*3, c.Width)
	linear("score.0", c.Width*5, c.Width)
	linear("score.1", c.Width, 1)
	linear("value.0", c.Width*2, c.Width)
	linear("value.1", c.Width, 1)
	if c.PlanFeatures == "target-counts-v1" {
		// No random draws: matched-seed architectures have identical original
		// parameters and initial logits. The new projection learns from zero.
		m.Parameters["score.plan.w"] = Parameter[T]{PlanTargetFeatures, c.Width, make([]T, PlanTargetFeatures*c.Width)}
	}
	return m, nil
}
func (m *Model[T]) Validate() error {
	expected, e := NewModel[T](m.Config, 0)
	if e != nil {
		return e
	}
	if len(expected.Parameters) != len(m.Parameters) {
		return fmt.Errorf("network parameter set mismatch")
	}
	for name, want := range expected.Parameters {
		p, ok := m.Parameters[name]
		if !ok || p.Rows != want.Rows || p.Cols != want.Cols || len(p.Values) != p.Rows*p.Cols {
			return fmt.Errorf("invalid parameter shape: %s", name)
		}
		for _, v := range p.Values {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return fmt.Errorf("nonfinite parameter: %s", name)
			}
		}
	}
	return nil
}
func (m *Model[T]) Count() int {
	n := 0
	for _, p := range m.Parameters {
		n += len(p.Values)
	}
	return n
}
func (m *Model[T]) Names() []string {
	names := make([]string, 0, len(m.Parameters))
	for n := range m.Parameters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Bound uses one parameter node per graph even across recurrent time steps.
// The model is immutable for the lifetime of a Bound graph.
type Bound[T Real] struct {
	Model      *Model[T]
	Graph      *Graph[T]
	parameters map[string]*Tensor[T]
}

func (m *Model[T]) Bind(g *Graph[T]) *Bound[T] { return &Bound[T]{m, g, map[string]*Tensor[T]{}} }
func (b *Bound[T]) parameter(name string) *Tensor[T] {
	if p := b.parameters[name]; p != nil {
		return p
	}
	p, ok := b.Model.Parameters[name]
	if !ok {
		panic("battlenet: missing parameter " + name)
	}
	n := b.Graph.New(p.Rows, p.Cols, p.Values)
	b.parameters[name] = n
	return n
}
func (b *Bound[T]) linear(name string, x *Tensor[T]) *Tensor[T] {
	return b.Graph.Linear(x, b.parameter(name+".w"), b.parameter(name+".b"))
}
func (b *Bound[T]) norm(name string, x *Tensor[T]) *Tensor[T] {
	return b.Graph.LayerNorm(x, b.parameter(name+".scale"), b.parameter(name+".bias"), 1e-5)
}
func (b *Bound[T]) gru(name string, x, previous *Tensor[T]) *Tensor[T] {
	g := b.Graph
	d := b.Model.Config.Width
	gates := g.Sigmoid(b.linear(name+".gates", g.Concat(x, previous)))
	reset, update := g.Slice(gates, 0, 1, 0, d), g.Slice(gates, 0, 1, d, d)
	proposal := g.Tanh(b.linear(name+".candidate", g.Concat(x, g.Mul(reset, previous))))
	ones := make([]T, d)
	for i := range ones {
		ones[i] = 1
	}
	return g.Add(g.Mul(update, previous), g.Mul(g.Add(g.New(1, d, ones), g.Scale(update, -1)), proposal))
}
func (b *Bound[T]) attention(name string, x *Tensor[T]) *Tensor[T] {
	g := b.Graph
	c := b.Model.Config
	q, k, v := b.linear(name+".q", x), b.linear(name+".k", x), b.linear(name+".v", x)
	size := c.Width / c.Heads
	var joined *Tensor[T]
	for h := 0; h < c.Heads; h++ {
		query := g.Slice(q, 0, q.Rows, h*size, size)
		key := g.Slice(k, 0, k.Rows, h*size, size)
		value := g.Slice(v, 0, v.Rows, h*size, size)
		weights := g.Softmax(g.Scale(g.MatMul(query, g.Transpose(key)), T(1/math.Sqrt(float64(size)))), nil)
		head := g.MatMul(weights, value)
		if joined == nil {
			joined = head
		} else {
			joined = g.Concat(joined, head)
		}
	}
	x = b.norm(name+".norm0", g.Add(x, b.linear(name+".out", joined)))
	return b.norm(name+".norm1", g.Add(x, b.linear(name+".ff1", g.ReLU(b.linear(name+".ff0", x)))))
}

type Encoding[T Real] struct{ Entities, Global, Memory *Tensor[T] }

func (b *Bound[T]) Encode(entities, events, previous *Tensor[T]) Encoding[T] {
	g := b.Graph
	x := g.ReLU(b.linear("entity.0", entities))
	x = g.ReLU(b.linear("entity.1", x))
	for i := 0; i < b.Model.Config.Layers; i++ {
		x = b.attention(fmt.Sprintf("block.%d", i), x)
	}
	pooled := g.MeanRows(x)
	memory := b.gru("history", g.Concat(pooled, events), previous)
	return Encoding[T]{x, pooled, memory}
}

// EncodeSequence runs attention once, then consumes chronological effect rows
// and the final decision-summary row through the shared history GRU. All rows
// are observations available at this decision boundary, not intermediate
// battlefield snapshots. Context checks bound cancellation between effects.
func (b *Bound[T]) EncodeSequence(ctx context.Context, entities, events, previous *Tensor[T]) (Encoding[T], error) {
	if events.Rows < 1 || events.Cols != b.Model.Config.EventFeatures {
		return Encoding[T]{}, fmt.Errorf("invalid history sequence dimensions")
	}
	if e := ctx.Err(); e != nil {
		return Encoding[T]{}, e
	}
	g := b.Graph
	state := b.Encode(entities, g.Slice(events, 0, 1, 0, events.Cols), previous)
	for i := 1; i < events.Rows; i++ {
		if e := ctx.Err(); e != nil {
			return Encoding[T]{}, e
		}
		state.Memory = b.gru("history", g.Concat(state.Global, g.Slice(events, i, 1, 0, events.Cols)), state.Memory)
	}
	return state, nil
}
func (b *Bound[T]) Value(e Encoding[T]) *Tensor[T] {
	return b.Graph.Tanh(b.linear("value.1", b.Graph.ReLU(b.linear("value.0", b.Graph.Concat(e.Global, e.Memory)))))
}

// Score sees all entities, the actor, target and the already selected joint
// plan prefix. targets=-1 denotes a group/no individual target, using pooled
// state; the candidate features still distinguish different target semantics.
// It returns 1 x candidates logits and embeddings for AdvancePlan.
func (b *Bound[T]) Score(e Encoding[T], actor int, candidates *Tensor[T], targets []int, prefix *Tensor[T]) (*Tensor[T], *Tensor[T]) {
	return b.ScoreWithPlanCounts(e, actor, candidates, targets, prefix, nil)
}

const PlanTargetFeatures = 8

// ScoreWithPlanCounts adds candidate-relative public plan counts to the scorer,
// not to the value head, historical memory, mask or plan GRU embeddings.
func (b *Bound[T]) ScoreWithPlanCounts(e Encoding[T], actor int, candidates *Tensor[T], targets []int, prefix, counts *Tensor[T]) (*Tensor[T], *Tensor[T]) {
	g := b.Graph
	n := candidates.Rows
	if b.Model.Config.PlanFeatures == "target-counts-v1" {
		if counts == nil || counts.Rows != n || counts.Cols != PlanTargetFeatures {
			panic("battlenet: missing or incompatible plan counts")
		}
	} else if counts != nil {
		panic("battlenet: plan counts on a legacy network")
	}
	if len(targets) != n {
		panic("battlenet: missing candidate targets")
	}
	encoded := g.ReLU(b.linear("candidate.1", g.ReLU(b.linear("candidate.0", candidates))))
	targetRows := make([]*Tensor[T], n)
	for i, t := range targets {
		if t < 0 {
			targetRows[i] = e.Global
		} else {
			targetRows[i] = g.Slice(e.Entities, t, 1, 0, e.Entities.Cols)
		}
	}
	x := g.Concat(encoded, g.Broadcast(g.Slice(e.Entities, actor, 1, 0, e.Entities.Cols), n))
	x = g.Concat(x, g.Stack(targetRows...))
	// A plan prefix must remember WHO chose WHAT against WHOM. Candidate
	// features alone can be identical for two different, equally healthy
	// targets and would make coordinated focus fire unrepresentable.
	planContext := g.Tanh(b.linear("plan.context", x))
	x = g.Concat(x, g.Broadcast(e.Memory, n))
	x = g.Concat(x, g.Broadcast(prefix, n))
	hidden := b.linear("score.0", x)
	if counts != nil {
		hidden = g.Add(hidden, g.MatMul(counts, b.parameter("score.plan.w")))
	}
	return g.Transpose(b.linear("score.1", g.ReLU(hidden))), planContext
}
func (b *Bound[T]) AdvancePlan(prefix, embedding *Tensor[T]) *Tensor[T] {
	return b.gru("plan", embedding, prefix)
}
func (b *Bound[T]) Gradients() map[string][]T {
	out := map[string][]T{}
	for name, p := range b.parameters {
		out[name] = append([]T(nil), p.Grad...)
	}
	return out
}
