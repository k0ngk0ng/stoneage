package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// OpeningSample binds a whole on-policy rollout to its independent action RNG.
// It is not a teacher demonstration or a replacement for collection evidence.
type OpeningSample struct {
	Episode    Trajectory `json:"episode"`
	ActionSeed int64      `json:"action_seed"`
	Opening    string     `json:"opening,omitempty"` // Logical collection group; identical setups may recur.
}

// OpeningReport identifies an experimental policy advantage separately from
// ordinary PPO reports. The CLI's default GAE training path is unchanged.
type OpeningReport struct {
	Method        string    `json:"method"`
	SamplesDigest string    `json:"samples_digest"`
	Replicates    int       `json:"replicates"`
	Groups        int       `json:"groups"`
	VaryingGroups int       `json:"varying_groups"`
	Config        PPOConfig `json:"config"`
	Training      Report    `json:"training"`
}

// TrainRepeatedOpenings uses R_i - mean(R_other_repeats) as the policy
// advantage for every step of an episode. Value targets remain the same GAE
// targets as Train, isolating the policy estimator in comparisons. The usual
// batch-wide per-turn normalization remains, so this is not a claim of an
// exactly unbiased episodic gradient after normalization/finite PPO updates.
//
// Every group must have exactly replicates completed games with the SAME
// scenario (including engine seed), side, behavior and opponent. Distinct RNG
// seeds and actual sampled actions are checked, not inferred from match IDs.
// Truncations are rejected; they are never converted to zero/loss labels.
// The CLI enables this estimator explicitly; model defaults are unchanged.
func TrainRepeatedOpenings(ctx context.Context, model *battlenet.Model[float32], optimizer *battlenet.Adam[float32], samples []OpeningSample, replicates int, config PPOConfig) (OpeningReport, error) {
	r := OpeningReport{Method: "opening-leave-one-out-v1", Replicates: replicates, Config: config}
	if e := config.validate(); e != nil {
		return r, e
	}
	if config.Gamma != 1 || optimizer == nil {
		return r, fmt.Errorf("opening return baseline requires gamma=1 and an optimizer")
	}
	episodes, advantages, summary, e := openingAdvantages(ctx, model, samples, replicates)
	if e != nil {
		return r, e
	}
	r.SamplesDigest, r.Groups, r.VaryingGroups = summary.digest, summary.groups, summary.varying
	r.Training, e = trainWithOpeningAdvantages(ctx, model, optimizer, episodes, config, advantages)
	return r, e
}

type openingSummary struct {
	digest          string
	groups, varying int
}

func openingAdvantages(ctx context.Context, model *battlenet.Model[float32], samples []OpeningSample, replicates int) ([]Trajectory, []float64, openingSummary, error) {
	var summary openingSummary
	fail := func(e error) ([]Trajectory, []float64, openingSummary, error) { return nil, nil, summary, e }
	if replicates < 2 || replicates > 64 || len(samples) == 0 || len(samples)%replicates != 0 {
		return fail(fmt.Errorf("complete opening groups of 2..64 independent rollouts required"))
	}
	version, e := ModelDigest(model)
	if e != nil {
		return fail(e)
	}
	type group struct {
		indices  []int
		initial  string
		sum      float64
		outcomes map[float64]bool
		seeds    map[int64]bool
	}
	groups := map[string]*group{}
	var order []string
	type evidence struct {
		Trajectory string `json:"trajectory"`
		Seed       int64  `json:"action_seed"`
		Opening    string `json:"opening,omitempty"`
	}
	provenance := make([]evidence, 0, len(samples))
	episodes := make([]Trajectory, len(samples))
	advantages := make([]float64, len(samples))
	seen := map[string]bool{}
	for i, sample := range samples {
		if e = ctx.Err(); e != nil {
			return fail(e)
		}
		t := sample.Episode
		if e = t.Validate(); e != nil {
			return fail(e)
		}
		if !t.Terminated || t.Truncated {
			return fail(fmt.Errorf("opening baseline requires complete terminal outcomes"))
		}
		if t.Policy != version || t.PolicyKind != "network-sampled" {
			return fail(fmt.Errorf("opening baseline requires current on-policy samples"))
		}
		identity := fmt.Sprintf("%s:%d", t.Match, t.Side)
		if seen[identity] {
			return fail(fmt.Errorf("duplicate opening rollout"))
		}
		seen[identity] = true
		// Scenario is a digest of the entire Setup, verified by Validate above.
		key, e := Digest(struct {
			Scenario, Policy, Opponent, Rules, Platform, Environment, Opening string
			Side                                                              int
		}{t.Scenario, t.Policy, t.Opponent, t.Rules, t.Platform, t.Environment, sample.Opening, t.Side})
		if e != nil {
			return fail(e)
		}
		initial, e := openingFrameDigest(t.Steps[0].Frame)
		if e != nil {
			return fail(e)
		}
		g := groups[key]
		if g == nil {
			g = &group{initial: initial, outcomes: map[float64]bool{}, seeds: map[int64]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		if g.initial != initial {
			return fail(fmt.Errorf("same opening has different public input"))
		}
		if g.seeds[sample.ActionSeed] {
			return fail(fmt.Errorf("duplicate action RNG seed within opening"))
		}
		g.seeds[sample.ActionSeed] = true
		g.indices = append(g.indices, i)
		if len(g.indices) > replicates {
			return fail(fmt.Errorf("opening has too many rollouts"))
		}
		ret := t.Steps[len(t.Steps)-1].Reward
		g.sum += ret
		g.outcomes[ret] = true
		// Verify independent sampling rather than accepting forced actions with
		// merely plausible likelihoods. Rebuild memory with the frozen network.
		rng := rand.New(rand.NewSource(sample.ActionSeed))
		var memory []float32
		for _, s := range t.Steps {
			graph := &battlenet.Graph[float32]{}
			out, err := battlepolicy.Forward(ctx, model.Bind(graph), s.Frame, graph.New(1, model.Config.Width, memory), nil, rng)
			if err != nil {
				return fail(err)
			}
			if !reflect.DeepEqual(out.Choices, s.Choices) || !reflect.DeepEqual(out.ConditionalLogProbs, s.ConditionalLogProbs) || out.LogProb.Data[0] != s.LogProb || out.Value.Data[0] != s.Value {
				return fail(fmt.Errorf("opening action/probability/value differs from seeded behavior"))
			}
			memory = append([]float32(nil), out.Memory.Data...)
		}
		id, err := Digest(t)
		if err != nil {
			return fail(err)
		}
		provenance = append(provenance, evidence{id, sample.ActionSeed, sample.Opening})
		episodes[i] = t
	}
	for _, key := range order {
		g := groups[key]
		if len(g.indices) != replicates {
			return fail(fmt.Errorf("incomplete opening group"))
		}
		summary.groups++
		if len(g.outcomes) > 1 {
			summary.varying++
		}
		for _, i := range g.indices {
			t := episodes[i]
			ret := t.Steps[len(t.Steps)-1].Reward
			advantages[i] = ret - (g.sum-ret)/float64(replicates-1)
		}
	}
	summary.digest, e = Digest(provenance)
	if e != nil {
		return fail(e)
	}
	return episodes, advantages, summary, nil
}

func openingFrameDigest(f battlepolicy.Frame) (string, error) {
	// Routing IDs differ for real repetitions. Clone before dropping them;
	// every numeric feature, candidate mask/order and history value is retained.
	b, e := json.Marshal(f)
	if e != nil {
		return "", e
	}
	var copy battlepolicy.Frame
	if e = json.Unmarshal(b, &copy); e != nil {
		return "", e
	}
	f = copy
	f.Match = ""
	for i := range f.Slots {
		f.Slots[i].Observation = ""
		for j := range f.Slots[i].Candidates {
			f.Slots[i].Candidates[j].ID = ""
		}
	}
	return Digest(f)
}
