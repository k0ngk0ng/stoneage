package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type OpponentScore struct {
	Key       string `json:"key"`
	Wins      int    `json:"wins"`
	Losses    int    `json:"losses"`
	Draws     int    `json:"draws"`
	Truncated int    `json:"truncated"`
}

// Percentages choose an opponent category, before the existing within-category
// difficulty weighting. An empty historical pool transfers its share to self.
type OpponentMix struct {
	Rules   int `json:"rules"`
	History int `json:"history"`
	Self    int `json:"self"`
}

func (m OpponentMix) Validate() error {
	if m.Rules < 0 || m.Rules > 100 || m.History < 0 || m.History > 100 || m.Self < 0 || m.Self > 100 || m.Rules+m.History+m.Self != 100 {
		return fmt.Errorf("opponent mix requires integer rule,history,self percentages in 0..100 summing to 100")
	}
	return nil
}

func (c RunConfig) opponentMix() OpponentMix {
	if c.OpponentMix == nil {
		return OpponentMix{Rules: 30, History: 50, Self: 20}
	}
	return *c.OpponentMix
}

type LeagueGame struct {
	Shard       string `json:"shard"`
	Key         string `json:"opponent"`
	Version     string `json:"opponent_version"`
	LearnerSide int    `json:"learner_side"`
	Outcome     int    `json:"outcome"` // -2 cutoff, -1 defeat, 0 draw, 1 victory.
}

// Each immutable report describes one frozen learner's TRAINING encounters.
// It is not a held-out evaluation or a promotion certificate.
type LeagueReport struct {
	Schema   int             `json:"schema_version"`
	Batch    int             `json:"batch"`
	Learner  string          `json:"learner"`
	Learning string          `json:"learning_state"`
	Pool     []string        `json:"historical_pool"`
	Games    []LeagueGame    `json:"games"`
	Rows     []OpponentScore `json:"rows"`
}

type opponentChoice struct {
	Key, Rule, Learning string
	Side                int
}

type LeagueInspection struct {
	Purpose          string           `json:"purpose"`
	Sampling         string           `json:"sampling"`
	Mix              OpponentMix      `json:"opponent_mix"`
	CompletedBatches int              `json:"completed_batches"`
	RecentScores     []OpponentScore  `json:"recent_training_scores"`
	Weights          map[string]int64 `json:"within_family_weights"`
	Reports          []LeagueReport   `json:"training_matrix"`
}

func InspectLeague(ctx context.Context, root string) (LeagueInspection, error) {
	var out LeagueInspection
	if e := ctx.Err(); e != nil {
		return out, e
	}
	c, _, e := LoadCheckpoint(root)
	if e != nil {
		return out, e
	}
	reports, e := loadLeague(ctx, root, c)
	if e != nil {
		return out, e
	}
	out = LeagueInspection{Purpose: "training-diagnostics", Sampling: c.Config.OpponentSampling, Mix: c.Config.opponentMix(), CompletedBatches: c.CompletedBatches, RecentScores: c.OpponentScores, Weights: map[string]int64{}, Reports: reports}
	if out.Sampling == "" {
		out.Sampling = "uniform"
	}
	keys := []string{}
	for _, rule := range c.Config.ruleOpponents() {
		keys = append(keys, "rule:"+rule)
	}
	for _, id := range c.Opponents {
		keys = append(keys, "history:"+id)
	}
	for _, key := range keys {
		w := int64(1)
		if c.Config.OpponentSampling == "weakness-v1" {
			score := OpponentScore{}
			for _, s := range c.OpponentScores {
				if s.Key == key {
					score = s
					break
				}
			}
			w = difficultyWeight(score)
		}
		out.Weights[key] = w
	}
	return out, nil
}

func difficultyWeight(s OpponentScore) int64 {
	// Laplace smoothing and an explicit floor: unseen opponents get 3000,
	// repeatedly lost opponents approach 5000; easy ones retain >=1000.
	// Cutoffs are counted separately and never masquerade as defeats.
	n := int64(s.Wins + s.Losses + s.Draws)
	return 1000 + 4000*(2*int64(s.Losses)+int64(s.Draws)+2)/(2*n+4)
}

func (c Checkpoint) opponentAt(game uint64) opponentChoice {
	game = c.Config.openingIndex(game)
	choice := opponentChoice{Key: "self", Side: int(game % 2)}
	random := gameSeed(c.Config.Seed, game, 7)
	// Keep the former ones digit as the tens digit: the default 30/50/20
	// thresholds then reproduce every legacy category choice, while the next
	// digit allows explicit non-multiple-of-ten percentages without a new RNG.
	bucket := int(10*(random%10) + (random/10)%10)
	mix := c.Config.opponentMix()
	keys := []string{}
	purpose := uint64(8)
	if bucket < mix.Rules {
		for _, name := range c.Config.ruleOpponents() {
			keys = append(keys, "rule:"+name)
		}
	} else if len(c.Opponents) > 0 && bucket < mix.Rules+mix.History {
		purpose = 5
		for _, id := range c.Opponents {
			keys = append(keys, "history:"+id)
		}
	} else {
		return choice
	}
	index := int(gameSeed(c.Config.Seed, game, purpose) % int64(len(keys)))
	if c.Config.OpponentSampling == "weakness-v1" {
		weights := make([]int64, len(keys))
		total := int64(0)
		for i, key := range keys {
			score := OpponentScore{}
			for _, s := range c.OpponentScores {
				if s.Key == key {
					score = s
					break
				}
			}
			weights[i] = difficultyWeight(score)
			total += weights[i]
		}
		point := gameSeed(c.Config.Seed, game, purpose) % total
		for i, w := range weights {
			if point < w {
				index = i
				break
			}
			point -= w
		}
	}
	choice.Key = keys[index]
	if strings.HasPrefix(choice.Key, "rule:") {
		choice.Rule = strings.TrimPrefix(choice.Key, "rule:")
	} else {
		choice.Learning = strings.TrimPrefix(choice.Key, "history:")
	}
	return choice
}

func scoreGames(games []LeagueGame) []OpponentScore {
	scores := map[string]OpponentScore{}
	for _, g := range games {
		s := scores[g.Key]
		s.Key = g.Key
		switch g.Outcome {
		case -2:
			s.Truncated++
		case -1:
			s.Losses++
		case 0:
			s.Draws++
		case 1:
			s.Wins++
		}
		scores[g.Key] = s
	}
	var out []OpponentScore
	for _, s := range scores {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func leagueScores(reports []LeagueReport) []OpponentScore {
	var games []LeagueGame
	for _, r := range reports[max(0, len(reports)-8):] {
		for _, g := range r.Games {
			if g.Key != "self" {
				games = append(games, g)
			}
		}
	}
	return scoreGames(games)
}

func (c Checkpoint) retainOpponent(latest string) []string {
	pool := c.Opponents
	if c.Config.PPO.UpdateGuard == "backtrack-v1" {
		// A rejected batch may leave the learning state unchanged. Keep one
		// sampling slot per state, refreshing its recency on reuse. Legacy
		// checkpoints retain their original pool and sampling schedule.
		pool = nil
		for _, id := range c.Opponents {
			if id != latest {
				pool = append(pool, id)
			}
		}
	}
	return retainOpponents(pool, latest, c.OpponentScores, c.Config.OpponentSampling == "weakness-v1")
}

func retainOpponents(pool []string, latest string, scores []OpponentScore, weakness bool) []string {
	pool = append(append([]string(nil), pool...), latest)
	if len(pool) <= 32 {
		return pool
	}
	if !weakness {
		return pool[len(pool)-32:]
	}
	// Preserve recent diversity plus difficult older snapshots. Ties prefer
	// the more recent snapshot; model files remain available on disk.
	cut := len(pool) - 16
	indices := make([]int, cut)
	weights := map[string]int64{}
	for _, s := range scores {
		weights[s.Key] = difficultyWeight(s)
	}
	weight := func(i int) int64 {
		if w, ok := weights["history:"+pool[i]]; ok {
			return w
		}
		return 3000
	}
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		a, b := indices[i], indices[j]
		if weight(a) == weight(b) {
			return a > b
		}
		return weight(a) > weight(b)
	})
	keep := map[int]bool{}
	for _, i := range indices[:16] {
		keep[i] = true
	}
	var out []string
	for i, id := range pool {
		if i >= cut || keep[i] {
			out = append(out, id)
		}
	}
	return out
}

func makeLeagueGame(c Checkpoint, game uint64, shard string, episodes []Trajectory, learner string) (LeagueGame, error) {
	choice := c.opponentAt(game)
	g := LeagueGame{Shard: shard, Key: choice.Key, LearnerSide: 1 - choice.Side}
	if len(episodes) != 2 {
		return g, fmt.Errorf("league requires both native perspectives")
	}
	own, other := episodes[g.LearnerSide], episodes[choice.Side]
	if own.Side != g.LearnerSide || other.Side != choice.Side || own.Policy != learner || own.PolicyKind != "network-sampled" || own.Opponent != other.Policy || other.Opponent != learner || own.Match != other.Match || own.Scenario != other.Scenario || own.Winner != other.Winner || own.Terminated != other.Terminated || own.Truncated != other.Truncated {
		return g, fmt.Errorf("league game does not match frozen policies/outcome")
	}
	if own.Rules != c.Environment.Rules || own.Platform != c.Environment.Platform || own.Environment != c.Environment.Scenario {
		return g, fmt.Errorf("league game environment mismatch")
	}
	expectedKind := "network-sampled"
	if choice.Rule != "" {
		expectedKind = "rule:" + choice.Rule
	}
	if other.PolicyKind != expectedKind || other.Rules != own.Rules || other.Platform != own.Platform || other.Environment != own.Environment || other.Group != own.Group {
		return g, fmt.Errorf("league opponent kind/environment mismatch")
	}
	g.Version = other.Policy
	if own.Truncated {
		g.Outcome = -2
	} else if own.Winner < 0 {
		g.Outcome = 0
	} else if own.Winner == g.LearnerSide {
		g.Outcome = 1
	} else {
		g.Outcome = -1
	}
	return g, nil
}

func opponentVersion(root string, choice opponentChoice, current string, cache map[string]string) (string, error) {
	if choice.Key == "self" {
		return current, nil
	}
	if version, ok := cache[choice.Key]; ok {
		return version, nil
	}
	version := ""
	var e error
	if choice.Rule != "" {
		version, e = (Policy{Rule: choice.Rule}).Version()
	} else {
		old, err := loadLearning(filepath.Join(root, "learning"), choice.Learning)
		if err != nil {
			return "", err
		}
		version, e = ModelDigest(old.Model)
	}
	if e == nil {
		cache[choice.Key] = version
	}
	return version, e
}

func saveLeagueReport(root string, r LeagueReport) (string, error) {
	id, e := Digest(r)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Join(root, "league"), 0700); e != nil {
		return "", e
	}
	if e = writeObject(filepath.Join(root, "league", id+".json"), r); e != nil {
		return "", e
	}
	return id, nil
}

// Replay the immutable TRAINING evidence on resume/export. Recompute choices,
// outcomes, pool retention and the recent difficulty window; do not trust a
// cached scoreboard or allow validation/test results to become training data.
func loadLeague(ctx context.Context, root string, c Checkpoint) ([]LeagueReport, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if c.Config.OpponentSampling != "weakness-v1" {
		return nil, nil
	}
	var reports []LeagueReport
	replay := Checkpoint{Config: c.Config, Environment: c.Environment}
	experiment, e := loadRunExperiment(root, c.Config)
	if e != nil {
		return nil, e
	}
	for batch, id := range c.LeagueReports {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var r LeagueReport
		if e := readObject(filepath.Join(root, "league", id+".json"), &r, 4<<20); e != nil {
			return nil, e
		}
		actual, _ := Digest(r)
		if actual != id || r.Schema != 1 || r.Batch != batch || !digest(r.Learner) || !digest(r.Learning) || len(r.Games) != c.Config.BatchMatches || !reflect.DeepEqual(r.Pool, replay.Opponents) {
			return nil, fmt.Errorf("league report differs from committed schedule")
		}
		learning, e := loadLearning(filepath.Join(root, "learning"), r.Learning)
		if e != nil {
			return nil, e
		}
		version, e := ModelDigest(learning.Model)
		if e != nil || version != r.Learner {
			return nil, fmt.Errorf("league learner identity mismatch")
		}
		versions := map[string]string{}
		for i, g := range r.Games {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			game := uint64(batch*c.Config.BatchMatches + i)
			if g.Shard != c.Trained[game] {
				return nil, fmt.Errorf("league references data outside trained prefix")
			}
			episodes, _, e := LoadShard(filepath.Join(root, "shards"), g.Shard)
			if e != nil {
				return nil, e
			}
			want, e := makeLeagueGame(replay, game, g.Shard, episodes, r.Learner)
			if e != nil || want != g {
				return nil, fmt.Errorf("league outcome differs from native trajectories: %v", e)
			}
			s, group := trainingScenario(c.Config, game, experiment)
			scenario, _ := Digest(s)
			if episodes[0].Scenario != scenario || episodes[0].Group != group {
				return nil, fmt.Errorf("league game is outside training schedule")
			}
			expected, e := opponentVersion(root, replay.opponentAt(game), r.Learner, versions)
			if e != nil {
				return nil, e
			}
			if g.Version != expected {
				return nil, fmt.Errorf("league opponent identity mismatch")
			}
		}
		if !reflect.DeepEqual(scoreGames(r.Games), r.Rows) {
			return nil, fmt.Errorf("league matrix differs from game outcomes")
		}
		reports = append(reports, r)
		replay.OpponentScores = leagueScores(reports)
		replay.Opponents = replay.retainOpponent(r.Learning)
	}
	if !reflect.DeepEqual(replay.OpponentScores, c.OpponentScores) || !reflect.DeepEqual(replay.Opponents, c.Opponents) {
		return nil, fmt.Errorf("checkpoint opponent pool/difficulty differs from training evidence")
	}
	return reports, nil
}
