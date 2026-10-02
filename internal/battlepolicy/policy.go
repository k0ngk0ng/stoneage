package battlepolicy

import (
	"context"
	"fmt"
	"math"
	"math/rand"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func NetworkConfig() battlenet.Config {
	return battlenet.Config{EntityFeatures: EntityFeatures, CandidateFeatures: CandidateFeatures, EventFeatures: EventFeatures, Width: 64, Heads: 4, Layers: 2, InputSchema: FeatureVersion}
}

func NetworkArchitecture(c battlenet.Config) string {
	if c.PlanFeatures == "target-counts-v1" {
		if c.PlanScope == "member" {
			return "independent-member-policy-v2"
		}
		return "commander-policy-v3"
	}
	if c.PlanScope == "member" {
		return "independent-member-policy-v1"
	}
	return "commander-policy-v2"
}

func SupportedFeatures(features string) bool {
	return features == FeatureVersion || features == RecipientFeatureVersion || features == LegacyFeatureVersion
}

func ActionsForFeatures(features string) string {
	if features == FeatureVersion {
		return ActionVersion
	}
	if features == LegacyFeatureVersion || features == RecipientFeatureVersion {
		return LegacyActionVersion
	}
	return ""
}

func ValidateFeatureEnvironment(features, scenario string) error {
	if !SupportedFeatures(features) || features == FeatureVersion && scenario != "controlled-battle-v8" {
		return fmt.Errorf("guardian-capable features require the v8 native skill contract")
	}
	return nil
}

// Models created before v7 omitted the input label. Keeping that omission
// intact preserves their content digests and explicitly selects v6 behavior.
func NetworkFeatures(c battlenet.Config) string {
	if c.InputSchema == "" {
		return LegacyFeatureVersion
	}
	return c.InputSchema
}

type Output struct {
	// Observation is encoded before any selected action or plan prefix. Value
	// calibration may cache Global/Memory only while these encoder weights stay
	// frozen. It must not substitute terminal outcomes into this representation.
	Observation         battlenet.Encoding[float32]
	Choices             []int
	ConditionalLogProbs []float32
	// Selected conditional log-probabilities retain their graph for supervised
	// per-action losses. PPO must still use the SUM in LogProb for the team.
	ConditionalLogProbTerms         []*battlenet.Tensor[float32]
	LogProb, Entropy, Value, Memory *battlenet.Tensor[float32]
}

func finite(x float32) bool { return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) }

func (f Frame) Validate() error {
	if !SupportedFeatures(f.Schema) || f.Actions != ActionsForFeatures(f.Schema) || f.Match == "" || f.Turn < 0 || f.Mode < 1 || f.Mode > 5 || f.Side < 0 || f.Side > 1 || len(f.Entities) < 2 || len(f.Entities) > 40 || len(f.Members) != f.Mode || len(f.Slots) < 1 || len(f.Slots) > f.Mode*2 {
		return fmt.Errorf("incompatible policy frame")
	}
	for _, row := range f.Entities {
		for _, x := range row {
			if !finite(x) {
				return fmt.Errorf("nonfinite entity feature")
			}
		}
	}
	for _, x := range f.Events {
		if !finite(x) {
			return fmt.Errorf("nonfinite history feature")
		}
	}
	if f.Events[127] != 1 || len(f.EventSequence) > MaxHistoryEffects {
		return fmt.Errorf("missing decision summary or oversized event sequence")
	}
	for _, event := range f.EventSequence {
		if event[127] != 0 {
			return fmt.Errorf("summary token inside effect sequence")
		}
		for _, x := range event {
			if !finite(x) {
				return fmt.Errorf("nonfinite ordered effect")
			}
		}
	}
	seen := map[[2]int]bool{}
	memberRows := map[int]bool{}
	if len(f.CompletedPlayers) != 0 && len(f.CompletedPlayers) != f.Mode {
		return fmt.Errorf("invalid completed-player routing")
	}
	completed := func(member int) bool {
		return member >= 0 && member < len(f.CompletedPlayers) && f.CompletedPlayers[member]
	}
	for member, row := range f.Members {
		if row < -1 || row >= len(f.Entities) || row == -1 && f.Turn == 0 || row >= 0 && (memberRows[row] || f.Entities[row][ePlayer] != 1 || f.Entities[row][eAlly] != 1) {
			return fmt.Errorf("invalid controlled member routing")
		}
		if row >= 0 {
			memberRows[row] = true
		}
		if completed(member) && (row < 0 || f.Entities[row][eDead] != 1 || f.Entities[row][eHP] != 0 || f.Entities[row][eHPKnown] != 1) {
			return fmt.Errorf("completed player is not publicly dead")
		}
	}
	for row, entity := range f.Entities {
		if entity[eBench] != 0 && (entity[eBench] != 1 || entity[eAlly] != 1 || entity[ePlayer] != 0 || entity[ePet] != 1) {
			return fmt.Errorf("invalid reserve entity")
		}
		if entity[ePlayer] == 1 && entity[eAlly] == 1 && !memberRows[row] {
			return fmt.Errorf("on-field player omitted from controlled member routing")
		}
	}
	for _, s := range f.Slots {
		actor := 0
		if s.Actor == "pet" {
			actor = 1
		} else if s.Actor != "player" {
			return fmt.Errorf("unknown planned actor")
		}
		key := [2]int{s.Member, actor}
		if s.Member < 0 || s.Member >= f.Mode || s.Entity < 0 || s.Entity >= len(f.Entities) || s.Observation == "" || len(s.Candidates) == 0 || len(s.Candidates) > 512 || seen[key] || actor == 0 && completed(s.Member) || actor == 1 && !seen[[2]int{s.Member, 0}] && !completed(s.Member) {
			return fmt.Errorf("invalid plan slot or order")
		}
		seen[key] = true
		if f.Entities[s.Entity][eBench] != 0 {
			return fmt.Errorf("reserve pet cannot act")
		}
		if f.Members[s.Member] < 0 || actor == 0 && s.Entity != f.Members[s.Member] {
			return fmt.Errorf("order for withdrawn or mismatched member")
		}
		valid := false
		ids := map[string]bool{}
		for _, c := range s.Candidates {
			if c.ID == "" || ids[c.ID] || c.Target < -1 || c.Target >= len(f.Entities) {
				return fmt.Errorf("invalid candidate routing")
			}
			ids[c.ID] = true
			valid = valid || c.Supported
			for _, x := range c.Features {
				if !finite(x) {
					return fmt.Errorf("nonfinite candidate feature")
				}
			}
		}
		if !valid {
			return fmt.Errorf("empty action support mask")
		}
	}
	for i := 0; i < f.Mode; i++ {
		if seen[[2]int{i, 0}] != (f.Members[i] >= 0 && !completed(i)) {
			return fmt.Errorf("incomplete team plan")
		}
	}
	return nil
}

func encodeFrame(ctx context.Context, bound *battlenet.Bound[float32], f Frame, previous *battlenet.Tensor[float32]) (battlenet.Encoding[float32], error) {
	var empty battlenet.Encoding[float32]
	if e := f.Validate(); e != nil {
		return empty, e
	}
	if bound == nil || bound.Model == nil || bound.Graph == nil {
		return empty, fmt.Errorf("missing bound network")
	}
	c := bound.Model.Config
	if e := c.Validate(); e != nil {
		return empty, e
	}
	if NetworkFeatures(c) != f.Schema {
		return empty, fmt.Errorf("frame features %q differ from model input %q", f.Schema, NetworkFeatures(c))
	}
	if c.EntityFeatures != EntityFeatures || c.CandidateFeatures != CandidateFeatures || c.EventFeatures != EventFeatures {
		return empty, fmt.Errorf("network feature dimensions incompatible")
	}
	if previous != nil {
		if previous.Rows != 1 || previous.Cols != c.Width || len(previous.Data) != c.Width {
			return empty, fmt.Errorf("invalid recurrent memory")
		}
		for _, x := range previous.Data {
			if !finite(x) {
				return empty, fmt.Errorf("nonfinite memory")
			}
		}
	}
	if e := ctx.Err(); e != nil {
		return empty, e
	}
	g := bound.Graph
	if previous == nil {
		previous = g.New(1, c.Width, nil)
	}
	entities := make([]float32, 0, len(f.Entities)*EntityFeatures)
	for _, row := range f.Entities {
		entities = append(entities, row[:]...)
	}
	events := make([]float32, 0, (len(f.EventSequence)+1)*EventFeatures)
	for _, event := range f.EventSequence {
		events = append(events, event[:]...)
	}
	events = append(events, f.Events[:]...)
	return bound.EncodeSequence(ctx, g.New(len(f.Entities), EntityFeatures, entities), g.New(len(f.EventSequence)+1, EventFeatures, events), previous)
}

// Recall reconstructs memory for a past decision without scoring a new plan
// for that past turn. The recurrent state depends only on public observations,
// never on the chosen actions or the plan prefix. Validate the whole saved frame
// just as Forward does; this is not a bypass for incomplete historical data.
func Recall(ctx context.Context, bound *battlenet.Bound[float32], f Frame, previous *battlenet.Tensor[float32]) (*battlenet.Tensor[float32], error) {
	state, err := encodeFrame(ctx, bound, f, previous)
	if err != nil {
		return nil, err
	}
	for _, x := range state.Memory.Data {
		if !finite(x) {
			return nil, fmt.Errorf("nonfinite recurrent output")
		}
	}
	return state.Memory, nil
}

// Forward binds decisions to one graph. Pass forced choices for recurrent PPO
// likelihood recomputation/behavior cloning; nil samples with rng, or uses
// conditional greedy selection when rng is nil. previous must be the memory
// BEFORE this actual decision turn, not after a repeated polling request.
// Sharing Bound across successive turns keeps the recurrent gradient path.
func Forward(ctx context.Context, bound *battlenet.Bound[float32], f Frame, previous *battlenet.Tensor[float32], forced []int, rng *rand.Rand) (Output, error) {
	if forced != nil && len(forced) != len(f.Slots) {
		return Output{}, fmt.Errorf("incomplete forced plan")
	}
	state, err := encodeFrame(ctx, bound, f, previous)
	if err != nil {
		return Output{}, err
	}
	g, c := bound.Graph, bound.Model.Config
	out := Output{Observation: state, Value: bound.Value(state), Memory: state.Memory, LogProb: g.New(1, 1, nil), Entropy: g.New(1, 1, nil)}
	prefix := g.New(1, c.Width, nil)
	memberPrefixes := make([]*battlenet.Tensor[float32], f.Mode)
	for i, s := range f.Slots {
		if e := ctx.Err(); e != nil {
			return Output{}, e
		}
		if c.PlanScope == "member" {
			prefix = memberPrefixes[s.Member]
			if prefix == nil {
				prefix = g.New(1, c.Width, nil)
			}
		}
		x := make([]float32, 0, len(s.Candidates)*CandidateFeatures)
		targets := make([]int, len(s.Candidates))
		mask := make([]bool, len(s.Candidates))
		for j, candidate := range s.Candidates {
			x = append(x, candidate.Features[:]...)
			targets[j] = candidate.Target
			mask[j] = candidate.Supported
		}
		var counts *battlenet.Tensor[float32]
		if c.PlanFeatures == "target-counts-v1" {
			values, err := PlanTargetCounts(f, i, out.Choices, c.PlanScope)
			if err != nil {
				return Output{}, err
			}
			counts = g.New(len(s.Candidates), battlenet.PlanTargetFeatures, values)
		}
		logits, embeddings := bound.ScoreWithPlanCounts(state, s.Entity, g.New(len(s.Candidates), CandidateFeatures, x), targets, prefix, counts)
		for _, n := range logits.Data {
			if !finite(n) {
				return Output{}, fmt.Errorf("nonfinite policy output")
			}
		}
		logp := g.LogSoftmax(logits, mask)
		chosen := -1
		if forced != nil {
			chosen = forced[i]
			if chosen < 0 || chosen >= len(mask) || !mask[chosen] {
				return Output{}, fmt.Errorf("forced action outside support mask")
			}
		} else if rng != nil {
			draw := rng.Float64()
			for j, p := range logp.Data {
				if !mask[j] {
					continue
				}
				chosen = j
				draw -= math.Exp(float64(p))
				if draw <= 0 {
					break
				}
			}
		} else {
			for j, v := range logp.Data {
				if mask[j] && (chosen < 0 || v > logp.Data[chosen]) {
					chosen = j
				}
			}
		}
		out.Choices = append(out.Choices, chosen)
		out.ConditionalLogProbs = append(out.ConditionalLogProbs, logp.Data[chosen])
		selected := g.Slice(logp, 0, 1, chosen, 1)
		out.ConditionalLogProbTerms = append(out.ConditionalLogProbTerms, selected)
		out.LogProb = g.Add(out.LogProb, selected)
		out.Entropy = g.Add(out.Entropy, battlenet.Entropy(g, logits, mask))
		prefix = bound.AdvancePlan(prefix, g.Slice(embeddings, chosen, 1, 0, c.Width))
		if c.PlanScope == "member" {
			memberPrefixes[s.Member] = prefix
		}
	}
	for _, tensor := range []*battlenet.Tensor[float32]{out.LogProb, out.Entropy, out.Value, out.Memory} {
		for _, n := range tensor.Data {
			if !finite(n) {
				return Output{}, fmt.Errorf("nonfinite recurrent output")
			}
		}
	}
	return out, nil
}

// Selections maps the joint plan back to the caller's member order, preserving
// the player-before-pet order required by the shared resolver.
func (f Frame) Selections(choices []int) ([][]aigame.BattleSelection, error) {
	if e := f.Validate(); e != nil {
		return nil, e
	}
	if len(choices) != len(f.Slots) {
		return nil, fmt.Errorf("incomplete plan")
	}
	out := make([][]aigame.BattleSelection, f.Mode)
	for i, s := range f.Slots {
		j := choices[i]
		if j < 0 || j >= len(s.Candidates) || !s.Candidates[j].Supported {
			return nil, fmt.Errorf("unsupported plan selection")
		}
		out[s.Member] = append(out[s.Member], aigame.BattleSelection{MatchID: f.Match, Turn: f.Turn, ObservationID: s.Observation, CandidateID: s.Candidates[j].ID})
	}
	return out, nil
}
