package battletrain

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type evaluationCommit struct {
	Schema   string `json:"schema"`
	Evidence string `json:"evidence"`
	Index    int    `json:"index"`
	Shard    string `json:"shard"`
}

func (r *evaluationRecorder) commit(index int, shard string) error {
	if index < 0 || !digest(shard) || !digest(r.id) {
		return fmt.Errorf("invalid evaluation commit")
	}
	dir := filepath.Join(r.root, "commits")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeObject(filepath.Join(dir, fmt.Sprintf("%09d.json", index)),
		evaluationCommit{"commander-evaluation-commit-v1", r.id, index, shard})
}

// Both complete verification and recovery use this exact check. Recovery must
// not trust the fact that a shard's bytes have a valid checksum as proof that
// its actions came from the frozen policy.
func verifyEvaluationGame(ctx context.Context, pair []Trajectory, candidate battlepolicy.Artifact, opponents []Opponent, schedule evaluationSchedule, index int, g EvaluationGame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(schedule.Suite) == 0 || index < 0 || index >= len(opponents)*len(schedule.Suite) || len(pair) != 2 {
		return fmt.Errorf("invalid evaluation game scope")
	}
	oi, i := index/len(schedule.Suite), index%len(schedule.Suite)
	s := schedule.Suite[i]
	if g.Opponent != opponents[oi].Name || g.OpponentPolicy != schedule.Versions[oi] || g.CandidateSide != i%2 || !reflect.DeepEqual(s, g.Scenario) || g.Group != ScenarioGroup(s) {
		return fmt.Errorf("evaluation game differs from independent schedule")
	}
	own := Policy{Model: candidate.Network, Greedy: true}
	ownVersion, err := own.Version()
	if err != nil {
		return err
	}
	scenario, err := Digest(s)
	if err != nil {
		return err
	}
	meta := schedule.Report.Environment
	for side, t := range pair {
		version, other, kind, policy := ownVersion, schedule.Versions[oi], own.Kind(), own
		if side != g.CandidateSide {
			version, other, kind, policy = other, version, schedule.Policies[oi].Kind(), schedule.Policies[oi]
		}
		if t.Side != side || t.Policy != version || t.Opponent != other || t.PolicyKind != kind || t.Match != pair[1-side].Match || t.Scenario != scenario || !reflect.DeepEqual(t.Setup, s) || t.Group != g.Group || t.Rules != meta.Rules || t.Platform != meta.Platform || t.Environment != meta.Scenario || t.Terminated != g.Terminated || t.Truncated != g.Truncated || t.Winner != g.Winner || len(t.Steps) != g.Turns {
			return fmt.Errorf("evaluation result/identity differs from raw trajectories at game %d side %d", index, side)
		}
		var memory []float32
		for turn, step := range t.Steps {
			decision, err := policy.decide(ctx, step.Frame, memory, nil)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(decision.choices, step.Choices) {
				return fmt.Errorf("evaluation action differs from frozen policy at game %d side %d turn %d", index, side, turn)
			}
			if schedule.StrictPolicyStatistics && (math.Abs(float64(decision.value-step.Value)) > 1e-5 || decision.logProb != step.LogProb || !reflect.DeepEqual(decision.conditional, step.ConditionalLogProbs)) {
				return fmt.Errorf("evaluation value/probabilities differ from frozen policy at game %d side %d turn %d", index, side, turn)
			}
			memory = decision.memory
		}
	}
	return nil
}

type evaluationRecoveryKey struct {
	Scenario, Policy, Opponent string
	Side                       int
}

// restore performs a read-only preflight over ALL retained games before writing
// any adoption commits or allowing collection. Explicit ordinal commits resolve
// identical schedule positions; legacy/orphan shards must map unambiguously.
func (r *evaluationRecorder) restore(ctx context.Context, candidate battlepolicy.Artifact, opponents []Opponent, schedule evaluationSchedule) ([]EvaluationGame, error) {
	if !r.options.Resume {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	total := len(schedule.Suite) * len(opponents)
	committed := map[string]int{}
	ordinals := map[int]string{}
	entries, err := os.ReadDir(filepath.Join(r.root, "commits"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Atomic publisher temporary files are never treated as commits.
		if strings.HasPrefix(entry.Name(), ".") && !entry.IsDir() {
			continue
		}
		var c evaluationCommit
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected evaluation commit directory")
		}
		if err := readObject(filepath.Join(r.root, "commits", entry.Name()), &c, 4096); err != nil {
			return nil, err
		}
		if c.Schema != "commander-evaluation-commit-v1" || c.Evidence != r.id || c.Index < 0 || c.Index >= total || !digest(c.Shard) || entry.Name() != fmt.Sprintf("%09d.json", c.Index) {
			return nil, fmt.Errorf("invalid evaluation commit identity")
		}
		if _, ok := committed[c.Shard]; ok {
			return nil, fmt.Errorf("duplicate evaluation commit shard")
		}
		committed[c.Shard], ordinals[c.Index] = c.Index, c.Shard
	}
	manifestIDs, compressedIDs := map[string]bool{}, map[string]bool{}
	entries, err = os.ReadDir(filepath.Join(r.root, "shards"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !entry.IsDir() {
			continue
		}
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected evaluation shard directory")
		}
		switch {
		case strings.HasSuffix(name, ".manifest.json"):
			id := strings.TrimSuffix(name, ".manifest.json")
			if !digest(id) {
				return nil, fmt.Errorf("invalid evaluation shard name")
			}
			manifestIDs[id] = true
		case strings.HasSuffix(name, ".jsonl.gz"):
			id := strings.TrimSuffix(name, ".jsonl.gz")
			if !digest(id) {
				return nil, fmt.Errorf("invalid evaluation shard name")
			}
			compressedIDs[id] = true
		default:
			return nil, fmt.Errorf("unrecognized evaluation shard file; retained unchanged: %s", name)
		}
	}
	if !reflect.DeepEqual(manifestIDs, compressedIDs) {
		return nil, fmt.Errorf("uncommitted evaluation shard bytes/manifest are unmatched; retained unchanged")
	}
	if len(manifestIDs) > total {
		return nil, fmt.Errorf("more retained games than scheduled; refused replay")
	}
	for id := range committed {
		if !manifestIDs[id] {
			return nil, fmt.Errorf("committed evaluation shard is missing")
		}
	}
	own := Policy{Model: candidate.Network, Greedy: true}
	version, err := own.Version()
	if err != nil {
		return nil, err
	}
	positions := map[evaluationRecoveryKey][]int{}
	for oi := range opponents {
		for i, s := range schedule.Suite {
			d, err := Digest(s)
			if err != nil {
				return nil, err
			}
			k := evaluationRecoveryKey{d, version, schedule.Versions[oi], i % 2}
			positions[k] = append(positions[k], oi*len(schedule.Suite)+i)
		}
	}
	games := make([]EvaluationGame, len(manifestIDs))
	seen := make([]bool, len(games))
	matches := map[string]bool{}
	for id := range manifestIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pair, _, err := LoadShard(filepath.Join(r.root, "shards"), id)
		if err != nil {
			return nil, err
		}
		if len(pair) != 2 {
			return nil, fmt.Errorf("evaluation needs both perspectives")
		}
		index, known := committed[id]
		if !known {
			possible := map[int]bool{}
			for _, t := range pair {
				for _, p := range positions[evaluationRecoveryKey{t.Scenario, t.Policy, t.Opponent, t.Side}] {
					possible[p] = true
				}
			}
			if len(possible) != 1 {
				return nil, fmt.Errorf("legacy/orphan evaluation shard has ambiguous or unknown schedule position")
			}
			for p := range possible {
				index = p
			}
			if _, exists := ordinals[index]; exists {
				return nil, fmt.Errorf("duplicate retained evaluation schedule entry")
			}
		}
		if index >= len(games) || seen[index] {
			return nil, fmt.Errorf("retained evaluation games are not a unique contiguous prefix")
		}
		oi, i := index/len(schedule.Suite), index%len(schedule.Suite)
		t := pair[i%2]
		if matches[t.Match] {
			return nil, fmt.Errorf("duplicate evaluation match identity")
		}
		matches[t.Match] = true
		g := EvaluationGame{Shard: id, Opponent: opponents[oi].Name, OpponentPolicy: schedule.Versions[oi], Group: t.Group, Scenario: schedule.Suite[i], CandidateSide: i % 2, Turns: len(t.Steps), Winner: t.Winner, Terminated: t.Terminated, Truncated: t.Truncated}
		if err := verifyEvaluationGame(ctx, pair, candidate, opponents, schedule, index, g); err != nil {
			return nil, err
		}
		games[index], seen[index] = g, true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("missing evaluation prefix position %s", strconv.Itoa(i))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.preflightOnly {
		return games, nil
	}
	// New journals also adopt old valid data and the SaveShard→commit crash
	// window. This happens only after all data passed the preflight above.
	for i, g := range games {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := r.commit(i, g.Shard); err != nil {
			return nil, err
		}
	}
	if r.options.Restored != nil {
		if err := r.options.Restored(len(games), total); err != nil {
			return nil, err
		}
	}
	return games, nil
}
