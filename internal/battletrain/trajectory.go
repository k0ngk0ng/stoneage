// Package battletrain owns offline rollouts and recurrent policy updates.
// It does not log into production, submit live game actions or promote models.
package battletrain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const TrajectorySchema = "commander-trajectory-v2"

type Transition struct {
	Frame               battlepolicy.Frame `json:"frame"`
	Choices             []int              `json:"choices"`
	ConditionalLogProbs []float32          `json:"conditional_log_probs"`
	LogProb             float32            `json:"log_prob"`
	Value               float32            `json:"value"`
	Reward              float64            `json:"reward"`
	// These are the actual client inputs used to construct Frame, retained
	// for audit/re-encoding. Only this team's private observations are included.
	Observations []aigame.BattleView     `json:"observations"`
	History      aigame.BattleEventBatch `json:"history"`
}

type Trajectory struct {
	Schema            string                  `json:"schema"`
	Match             string                  `json:"match"`
	Group             string                  `json:"group"` // Config pair, repeats, swap sides share this group.
	Policy            string                  `json:"policy"`
	PolicyKind        string                  `json:"policy_kind"`
	Opponent          string                  `json:"opponent"`
	Rules             string                  `json:"rules"`
	Platform          string                  `json:"platform"`
	Environment       string                  `json:"environment"`
	Scenario          string                  `json:"scenario"`
	Setup             battleenv.Scenario      `json:"initial_conditions"`
	Side              int                     `json:"side"`
	Mode              int                     `json:"mode"`
	Steps             []Transition            `json:"steps"`
	Terminated        bool                    `json:"terminated"`
	Truncated         bool                    `json:"truncated"`
	Winner            int                     `json:"winner"`
	Bootstrap         float64                 `json:"bootstrap"`
	Next              *battlepolicy.Frame     `json:"next,omitempty"`
	FinalObservations []aigame.BattleView     `json:"final_observations"`
	FinalHistory      aigame.BattleEventBatch `json:"final_history"`
}

func Digest(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func ModelDigest(m *battlenet.Model[float32]) (string, error) {
	return battlepolicy.NetworkDigest(m)
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && len(s) == 64
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }

func (t Trajectory) Validate() error {
	if t.PolicyKind != "network-sampled" && t.PolicyKind != "network-greedy" && !(strings.HasPrefix(t.PolicyKind, "rule:") && battlepolicy.RuleSupported(strings.TrimPrefix(t.PolicyKind, "rule:"))) {
		return fmt.Errorf("unknown behavior policy kind")
	}
	if e := t.Setup.ValidateEnvironment(t.Environment); e != nil {
		return e
	}
	setup, e := Digest(t.Setup)
	if e != nil {
		return e
	}
	if setup != t.Scenario || t.Setup.Mode != t.Mode || len(t.Steps) > t.Setup.MaxTurns {
		return fmt.Errorf("initial conditions/scenario digest mismatch")
	}
	if t.Group != ScenarioGroup(t.Setup) {
		return fmt.Errorf("experiment group does not match canonical initial configuration")
	}
	if e := (battleenv.Metadata{Rules: t.Rules, Platform: t.Platform, Scenario: t.Environment}).Validate(); e != nil {
		return e
	}
	if len(t.Steps) > 0 {
		if e := battlepolicy.ValidateFeatureEnvironment(t.Steps[0].Frame.Schema, t.Environment); e != nil {
			return e
		}
	}
	if t.Schema != TrajectorySchema || t.Match == "" || t.Group == "" || !digest(t.Policy) || !digest(t.Opponent) || !digest(t.Rules) || !digest(t.Scenario) || t.Side < 0 || t.Side > 1 || t.Mode < 1 || t.Mode > 5 || len(t.Steps) == 0 || len(t.Steps) > 10000 || t.Terminated == t.Truncated || t.Winner < -1 || t.Winner > 1 || !finite(t.Bootstrap) {
		return fmt.Errorf("invalid trajectory envelope")
	}
	if t.Terminated && (t.Bootstrap != 0 || t.Next != nil) || t.Truncated && (t.Next == nil || t.Winner != -1) {
		return fmt.Errorf("terminal/bootstrap mismatch")
	}
	for i, s := range t.Steps {
		f := s.Frame
		if f.Schema != t.Steps[0].Frame.Schema {
			return fmt.Errorf("mixed feature contracts within trajectory")
		}
		if e := f.Validate(); e != nil {
			return e
		}
		if f.Match != t.Match || f.Mode != t.Mode || f.Side != t.Side || f.Turn != int32(i) || f.Events[12] != 0 || f.Events[13] != float32(boolInt(i == 0)) {
			return fmt.Errorf("incomplete/mixed trajectory at step %d", i)
		}
		if len(s.Choices) != len(f.Slots) || len(s.ConditionalLogProbs) != len(f.Slots) || !finite(float64(s.LogProb)) || !finite(float64(s.Value)) || math.Abs(float64(s.Value)) > 1.00001 || !finite(s.Reward) {
			return fmt.Errorf("invalid transition numbers")
		}
		if _, e := f.Selections(s.Choices); e != nil {
			return e
		}
		sum := float32(0)
		for _, p := range s.ConditionalLogProbs {
			if !finite(float64(p)) || p > 1e-5 {
				return fmt.Errorf("invalid action probability")
			}
			sum += p
		}
		if math.Abs(float64(sum-s.LogProb)) > 1e-4 {
			return fmt.Errorf("joint log probability mismatch")
		}
		want := 0.
		if i == len(t.Steps)-1 && t.Terminated && t.Winner >= 0 {
			want = -1
			if t.Winner == t.Side {
				want = 1
			}
		}
		if s.Reward != want {
			return fmt.Errorf("reward is not the authoritative terminal outcome")
		}
	}
	if t.Next != nil {
		if e := t.Next.Validate(); e != nil {
			return e
		}
		if t.Next.Schema != t.Steps[0].Frame.Schema || t.Next.Match != t.Match || t.Next.Side != t.Side || t.Next.Mode != t.Mode || t.Next.Turn != int32(len(t.Steps)) || t.Next.Events[12] != 0 {
			return fmt.Errorf("invalid truncation successor")
		}
	}
	var stream string
	var cursor uint64
	for i, s := range t.Steps {
		encoded, e := battlepolicy.EncodeVersion(s.Observations, battlepolicy.History{Batch: &s.History, PreviousStream: stream, PreviousCursor: cursor, First: i == 0}, s.Frame.Schema)
		if e != nil {
			return fmt.Errorf("step %d observation provenance: %w", i, e)
		}
		if !reflect.DeepEqual(encoded, s.Frame) {
			return fmt.Errorf("step %d features differ from client observations", i)
		}
		stream, cursor = s.History.Stream, s.History.Cursor
	}
	if len(t.FinalObservations) != t.Mode || t.FinalHistory.Gap || t.FinalHistory.Stream != stream || t.FinalHistory.Cursor < cursor {
		return fmt.Errorf("missing final observation or history")
	}
	observer := t.FinalObservations[0]
	for _, v := range t.FinalObservations {
		if v.MatchID != t.Match || v.Turn != int32(len(t.Steps)) || v.Mode != t.Mode || int(v.Battle.MyNo)/10 != t.Side {
			return fmt.Errorf("mixed final observation")
		}
		if v.Battle.MyNo < observer.Battle.MyNo {
			observer = v
		}
	}
	finalFeatures, e := battlepolicy.EncodeHistoryVersion(observer, battlepolicy.History{Batch: &t.FinalHistory, PreviousStream: stream, PreviousCursor: cursor}, t.Steps[0].Frame.Schema)
	if e != nil {
		return e
	}
	if finalFeatures[12] != 0 {
		return fmt.Errorf("incomplete final history")
	}
	if t.Truncated {
		encoded, e := battlepolicy.EncodeVersion(t.FinalObservations, battlepolicy.History{Batch: &t.FinalHistory, PreviousStream: stream, PreviousCursor: cursor}, t.Next.Schema)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(encoded, *t.Next) {
			return fmt.Errorf("bootstrap features differ from successor observations")
		}
	}
	return nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
