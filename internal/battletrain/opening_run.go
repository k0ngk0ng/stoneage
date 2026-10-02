package battletrain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// OpeningReceipt binds each committed experimental update to the ordered
// native matches, collection schedule, estimator and full optimizer states.
type OpeningReceipt struct {
	Schema int           `json:"schema_version"`
	Batch  int           `json:"batch"`
	Before string        `json:"before_learning"`
	After  string        `json:"after_learning"`
	Shards []string      `json:"shards"`
	Report OpeningReport `json:"report"`
}

func openingMethod(c RunConfig) string {
	if c.PolicyAdvantage == "opening-loo" {
		return "opening-leave-one-out-v1"
	}
	return "repeated-opening-gae-v1"
}

// Both experimental arms use this exact collector/validator. Ordinary GAE
// never enters here and retains cutoff bootstrapping and its old schedule.
func openingBatch(ctx context.Context, root string, c Checkpoint, model *battlenet.Model[float32], shards []string, first uint64, x *Experiment) ([]Trajectory, []float64, OpeningReport, error) {
	r := OpeningReport{Method: openingMethod(c.Config), Replicates: c.Config.OpeningRollouts, Config: c.Config.PPO}
	version, e := ModelDigest(model)
	if e != nil {
		return nil, nil, r, e
	}
	versions := map[string]string{}
	var samples []OpeningSample
	for i, id := range shards {
		if e := ctx.Err(); e != nil {
			return nil, nil, r, e
		}
		game := first + uint64(i)
		episodes, _, e := LoadShard(filepath.Join(root, "shards"), id)
		if e != nil {
			return nil, nil, r, e
		}
		g, e := makeLeagueGame(c, game, id, episodes, version)
		if e != nil {
			return nil, nil, r, e
		}
		want, e := opponentVersion(root, c.opponentAt(game), version, versions)
		if e != nil {
			return nil, nil, r, e
		}
		if g.Version != want {
			return nil, nil, r, fmt.Errorf("opening opponent differs from schedule")
		}
		scenario, group := trainingScenario(c.Config, game, x)
		scenarioID, _ := Digest(scenario)
		for _, tr := range episodes {
			if tr.Scenario != scenarioID || tr.Group != group {
				return nil, nil, r, fmt.Errorf("opening scenario differs from schedule")
			}
			if !tr.Terminated || tr.Truncated {
				return nil, nil, r, fmt.Errorf("repeated openings require complete terminal outcomes; preserved batch contains a cutoff")
			}
			if tr.Policy == version {
				samples = append(samples, OpeningSample{Episode: tr, ActionSeed: gameSeed(c.Config.Seed, game, uint64(3+tr.Side)), Opening: strconv.FormatUint(c.Config.openingIndex(game), 10)})
			}
		}
	}
	batch, advantages, summary, e := openingAdvantages(ctx, model, samples, c.Config.OpeningRollouts)
	r.SamplesDigest, r.Groups, r.VaryingGroups = summary.digest, summary.groups, summary.varying
	if c.Config.PolicyAdvantage == "" {
		advantages = nil
	}
	return batch, advantages, r, e
}

func saveOpeningReceipt(root string, r OpeningReceipt) (string, error) {
	id, e := Digest(r)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Join(root, "openings"), 0700); e != nil {
		return "", e
	}
	return id, writeObject(filepath.Join(root, "openings", id+".json"), r)
}

// Verify sampling and schedule from source trajectories, not just a receipt
// checksum. State identities and Adam increments are checked without rerunning
// every historical gradient update. This is provenance validation, not a
// cryptographic proof that arbitrary rewritten weights were trained honestly.
func loadOpeningReceipts(ctx context.Context, root string, c Checkpoint, x *Experiment) error {
	if c.Config.OpeningRollouts == 0 {
		return nil
	}
	league, e := loadLeague(ctx, root, c)
	if e != nil {
		return e
	}
	replay := Checkpoint{Config: c.Config, Environment: c.Environment}
	previous := ""
	for batch, id := range c.OpeningReports {
		if e := ctx.Err(); e != nil {
			return e
		}
		var r OpeningReceipt
		if e := readObject(filepath.Join(root, "openings", id+".json"), &r, 4<<20); e != nil {
			return e
		}
		actual, _ := Digest(r)
		start := batch * c.Config.BatchMatches
		if actual != id || r.Schema != 1 || r.Batch != batch || !digest(r.Before) || !digest(r.After) || batch > 0 && r.Before != previous || !reflect.DeepEqual(r.Shards, c.Trained[start:start+c.Config.BatchMatches]) {
			return fmt.Errorf("opening receipt differs from committed progress")
		}
		before, e := loadLearning(filepath.Join(root, "learning"), r.Before)
		if e != nil {
			return e
		}
		after, e := loadLearning(filepath.Join(root, "learning"), r.After)
		if e != nil {
			return e
		}
		if before.Model.Config != c.Config.Network || after.Model.Config != c.Config.Network {
			return fmt.Errorf("opening network configuration mismatch")
		}
		if batch == 0 {
			if before.Optimizer.Step != 0 {
				return fmt.Errorf("opening training must start with fresh optimizer")
			}
			want := ""
			if c.Warmup != nil && c.Warmup.LastReport != nil {
				want = c.Warmup.LastReport.AfterPolicy
			} else if c.Config.InitialModel != "" {
				parent, err := loadInitialModel(root, c.Config)
				if err != nil {
					return err
				}
				want = parent.WeightsDigest
				if c.Config.InitialPolicyScale != 0 {
					receipt, err := verifyPolicyInitialization(ctx, root, c.Initialization, c.Config, parent, c.Config.Network, c.Config.Seed, c.Config.InitialPolicyScale)
					if err != nil {
						return err
					}
					want = receipt.Weights
				}
			} else {
				m, err := battlenet.NewModel[float32](c.Config.Network, c.Config.Seed)
				if err != nil {
					return err
				}
				want, _ = ModelDigest(m)
			}
			actual, _ := ModelDigest(before.Model)
			if actual != want {
				return fmt.Errorf("opening initial policy mismatch")
			}
		}
		episodes, _, expected, e := openingBatch(ctx, root, replay, before.Model, r.Shards, uint64(start), x)
		if e != nil {
			return e
		}
		expected.Training = r.Report.Training
		if !reflect.DeepEqual(expected, r.Report) {
			return fmt.Errorf("opening report differs from sampled evidence")
		}
		p := r.Report.Training
		bv, _ := ModelDigest(before.Model)
		av, _ := ModelDigest(after.Model)
		turns := 0
		for _, tr := range episodes {
			turns += len(tr.Steps)
		}
		if p.BehaviorPolicy != bv || p.CandidatePolicy != av || p.Episodes != len(episodes) || p.TeamTurns != turns || len(p.Epochs) > c.Config.PPO.Epochs || after.Optimizer.Step-before.Optimizer.Step != len(p.Epochs) {
			return fmt.Errorf("opening update report/state mismatch")
		}
		if c.Config.OpponentSampling == "weakness-v1" {
			if league[batch].Learning != r.Before {
				return fmt.Errorf("opening and league policy mismatch")
			}
			replay.OpponentScores = leagueScores(league[:batch+1])
		}
		replay.Opponents = replay.retainOpponent(r.Before)
		previous = r.After
		if batch == c.CompletedBatches-1 && (previous != c.Learning || !reflect.DeepEqual(c.LastReport, &p)) {
			return fmt.Errorf("opening final state/report mismatch")
		}
	}
	if !reflect.DeepEqual(replay.Opponents, c.Opponents) {
		return fmt.Errorf("opening historical opponent pool mismatch")
	}
	return nil
}
