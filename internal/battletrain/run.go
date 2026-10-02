package battletrain

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type RunConfig struct {
	InitialPolicyScale float64          `json:"initial_policy_scale,omitempty"`
	OpeningRollouts    int              `json:"opening_rollouts,omitempty"` // Zero preserves the original schedule.
	PolicyAdvantage    string           `json:"policy_advantage,omitempty"` // Empty is GAE; opening-loo is experimental.
	PetSkillMask       int              `json:"pet_skill_mask,omitempty"`   // Optional active set; zero preserves the original seven skills.
	Pairing            string           `json:"pairing,omitempty"`
	OpponentSampling   string           `json:"opponent_sampling,omitempty"` // Empty/uniform preserves old schedules.
	OpponentMix        *OpponentMix     `json:"opponent_mix,omitempty"`      // Nil preserves the original 30/50/20 category draw exactly.
	RuleOpponents      []string         `json:"rule_opponents,omitempty"`    // Nil preserves the pre-sustain schedule on resume.
	ReservePets        int              `json:"reserve_pets,omitempty"`      // Extra pets per member, 0..2.
	HealingItems       int              `json:"healing_items,omitempty"`     // Number of single-use template 1234 items per character, 0..15.
	HealingMagic       int              `json:"healing_magic,omitempty"`
	Experiment         string           `json:"experiment,omitempty"`
	InitialModel       string           `json:"initial_model,omitempty"`
	Seed               int64            `json:"seed"`
	Mode               int              `json:"mode"`
	Points             int              `json:"points"`
	PetPoints          int              `json:"pet_points"` // zero disables owned pets.
	Level              int              `json:"level"`
	MaxTurns           int              `json:"max_turns"`
	BatchMatches       int              `json:"batch_matches"`
	PPO                PPOConfig        `json:"ppo"`
	Network            battlenet.Config `json:"network"`
	Warmup             *WarmupConfig    `json:"warmup,omitempty"`
}

func DefaultRunConfig() RunConfig {
	return RunConfig{Pairing: BalancedPairing, Seed: 1, Mode: 1, Points: 120, PetPoints: 120, Level: 35, MaxTurns: 200, BatchMatches: 32, PPO: DefaultPPOConfig(), Network: battlepolicy.NetworkConfig(), Warmup: DefaultWarmupConfig(), RuleOpponents: battlepolicy.DefaultRuleNames(), OpponentSampling: "weakness-v1"}
}
func (c RunConfig) Validate() error {
	if err := validateInitialPolicyScale(c.InitialPolicyScale); err != nil {
		return err
	}
	if c.InitialPolicyScale != 0 && (c.InitialModel == "" || c.Warmup != nil && c.Warmup.Matches != 0) {
		return fmt.Errorf("initial policy scale requires parent-based PPO without imitation warmup")
	}
	if c.OpeningRollouts != 0 && (c.OpeningRollouts < 2 || c.OpeningRollouts > 64 || c.BatchMatches%c.OpeningRollouts != 0) {
		return fmt.Errorf("opening rollouts must be 2..64 and divide batch matches")
	}
	if c.PolicyAdvantage != "" && c.PolicyAdvantage != "opening-loo" {
		return fmt.Errorf("policy advantage must be gae or opening-loo")
	}
	if c.PolicyAdvantage == "opening-loo" && (c.OpeningRollouts < 2 || c.PPO.Gamma != 1) {
		return fmt.Errorf("opening-loo requires repeated openings and gamma=1")
	}
	if e := c.opponentMix().Validate(); e != nil {
		return e
	}
	if !battlepolicy.SupportedFeatures(battlepolicy.NetworkFeatures(c.Network)) {
		return fmt.Errorf("unsupported network input features")
	}
	if e := validatePairing(c.Pairing); e != nil {
		return e
	}
	if c.OpponentSampling != "" && c.OpponentSampling != "uniform" && c.OpponentSampling != "weakness-v1" {
		return fmt.Errorf("opponent sampling must be uniform or weakness-v1")
	}
	seenRules := map[string]bool{}
	for _, name := range c.RuleOpponents {
		if seenRules[name] || !battlepolicy.RuleSupported(name) {
			return fmt.Errorf("invalid/duplicate rule opponent %q", name)
		}
		seenRules[name] = true
	}
	if c.ReservePets < 0 || c.ReservePets > 2 || c.ReservePets > 0 && c.PetPoints == 0 {
		return fmt.Errorf("reserve pets must be 0..2 and require pet points")
	}
	if c.PetSkillMask != 0 && (c.PetPoints == 0 || !battleenv.ValidPetSkillMask(c.PetSkillMask)) {
		return fmt.Errorf("active pet skills require pet points and at most seven supported skills")
	}
	if c.HealingItems < 0 || c.HealingItems > 15 {
		return fmt.Errorf("healing items must be in 0..15")
	}
	if c.HealingMagic != 0 && c.HealingMagic != 10 && c.HealingMagic != 20 {
		return fmt.Errorf("healing magic must be 0, 10 or 20")
	}
	if c.Experiment != "" && !digest(c.Experiment) {
		return fmt.Errorf("invalid experiment identity")
	}
	if c.InitialModel != "" && (!digest(c.InitialModel) || c.Experiment == "") {
		return fmt.Errorf("initial model requires a frozen experiment")
	}
	if c.Mode < 1 || c.Mode > 5 || c.Points < 4 || c.Points > 10000 || c.PetPoints != 0 && (c.PetPoints < 4 || c.PetPoints > 10000) || c.Level < 1 || c.Level > 200 || c.MaxTurns < 1 || c.MaxTurns > 10000 || c.BatchMatches < 1 || c.BatchMatches > 1000 {
		return fmt.Errorf("invalid training scenario/batch configuration")
	}
	if e := c.PPO.validate(); e != nil {
		return e
	}
	if e := c.Warmup.validate(); e != nil {
		return e
	}
	if e := c.Network.Validate(); e != nil {
		return e
	}
	if c.Network.EntityFeatures != battlepolicy.EntityFeatures || c.Network.CandidateFeatures != battlepolicy.CandidateFeatures || c.Network.EventFeatures != battlepolicy.EventFeatures {
		return fmt.Errorf("incompatible network features")
	}
	return nil
}

// RNGs are derived per game and purpose, so checkpoints need a seed/counter,
// not an opaque Go rand implementation state. In-flight games are discarded on
// interruption; completed shards and their next-game counter commit together.
func gameSeed(seed int64, game uint64, purpose uint64) int64 {
	var b [24]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(seed))
	binary.LittleEndian.PutUint64(b[8:16], game)
	binary.LittleEndian.PutUint64(b[16:], purpose)
	h := sha256.Sum256(b[:])
	return int64(binary.LittleEndian.Uint64(h[:8]) & 0x7fffffffffffffff)
}
func allocation(points int, rng *rand.Rand) battleenv.Build {
	b := battleenv.Build{1, 1, 1, 1}
	left := points - 4
	var weights [4]int
	total := 0
	style := rng.Intn(6)
	for i := range weights {
		weights[i] = 1 + rng.Intn(100)
		if style == i {
			weights[i] += 400
		}
		if style == 4 {
			weights[i] = 1
		}
		total += weights[i]
	}
	for i, w := range weights {
		n := (points - 4) * w / total
		b[i] += n
		left -= n
	}
	for ; left > 0; left-- {
		b[rng.Intn(4)]++
	}
	return b
}
func (c RunConfig) openingIndex(game uint64) uint64 {
	if c.OpeningRollouts > 0 {
		return game / uint64(c.OpeningRollouts)
	}
	return game
}

// Only PPO collection repeats openings. Warmup retains its teacher schedule.
func trainingScenario(c RunConfig, game uint64, experiment *Experiment) (battleenv.Scenario, string) {
	game = c.openingIndex(game)
	if experiment != nil {
		return experiment.trainingScenario(c.Seed, game)
	}
	return scenarioFor(c, game)
}

func scenarioFor(c RunConfig, game uint64) (battleenv.Scenario, string) {
	size := uint64(familyGames(c.Pairing))
	rng := rand.New(rand.NewSource(gameSeed(c.Seed, game/size, 1)))
	s := battleenv.Scenario{Seed: 1 + int(gameSeed(c.Seed, game/(size/2), 2)%2147483647), Level: c.Level, HealingMagic: c.HealingMagic, HealingItems: c.HealingItems, MaxTurns: c.MaxTurns, Mode: c.Mode}
	for i := 0; i < 2*c.Mode; i++ {
		s.Builds = append(s.Builds, allocation(c.Points, rng))
		if c.PetPoints > 0 {
			s.PetBuilds = append(s.PetBuilds, allocation(c.PetPoints, rng))
		}
	}
	if c.ReservePets > 0 {
		s.Reserves = randomReserves(2*c.Mode, c.ReservePets, c.PetPoints, rng, c.PetSkillMask)
	}
	applyPetSkills(&s, c.PetSkillMask)
	if pairingSwap(c.Pairing, game) {
		swap := func(b []battleenv.Build) {
			if len(b) == 0 {
				return
			}
			for i := 0; i < c.Mode; i++ {
				b[i], b[i+c.Mode] = b[i+c.Mode], b[i]
			}
		}
		s.Builds = append([]battleenv.Build(nil), s.Builds...)
		s.PetBuilds = append([]battleenv.Build(nil), s.PetBuilds...)
		swap(s.Builds)
		swap(s.PetBuilds)
		for i := 0; i < c.Mode && len(s.Reserves) > 0; i++ {
			s.Reserves[i], s.Reserves[i+c.Mode] = s.Reserves[i+c.Mode], s.Reserves[i]
		}
	}
	return s, ScenarioGroup(s)
}

// ScenarioGroup ignores seeds, collection cutoffs and which side is first.
// Thus coincidentally regenerated configurations cannot leak across splits by
// receiving different group labels. Slot order within each team is preserved.
func ScenarioGroup(s battleenv.Scenario) string {
	s.Seed, s.MaxTurns = 0, 0
	s.Builds = append([]battleenv.Build(nil), s.Builds...)
	s.PetBuilds = append([]battleenv.Build(nil), s.PetBuilds...)
	s.Reserves = battleenv.CloneReserves(s.Reserves)
	s.PetSkillMasks = append([]int(nil), s.PetSkillMasks...)
	allDefault := len(s.PetSkillMasks) > 0
	for _, mask := range s.PetSkillMasks {
		allDefault = allDefault && mask == 127
	}
	if allDefault {
		s.PetSkillMasks = nil // Explicit default and omitted skills are one family.
	}
	if s.Mode >= 1 && len(s.Builds) == 2*s.Mode && (len(s.PetBuilds) == 0 || len(s.PetBuilds) == 2*s.Mode) {
		keys := [2]string{}
		for side := 0; side < 2; side++ {
			var pets []battleenv.Build
			if len(s.PetBuilds) > 0 {
				pets = s.PetBuilds[side*s.Mode : (side+1)*s.Mode]
			}
			keys[side], _ = Digest(struct{ Players, Pets []battleenv.Build }{s.Builds[side*s.Mode : (side+1)*s.Mode], pets})
			if len(s.Reserves) == 2*s.Mode {
				keys[side], _ = Digest(struct {
					Players, Pets []battleenv.Build
					Reserves      [][]battleenv.ReservePet
				}{s.Builds[side*s.Mode : (side+1)*s.Mode], pets, s.Reserves[side*s.Mode : (side+1)*s.Mode]})
			}
			if len(s.PetSkillMasks) == 2*s.Mode {
				keys[side], _ = Digest(struct {
					Base   string
					Skills []int
				}{keys[side], s.PetSkillMasks[side*s.Mode : (side+1)*s.Mode]})
			}
		}
		if keys[0] > keys[1] {
			for i := 0; i < s.Mode; i++ {
				if len(s.PetSkillMasks) == 2*s.Mode {
					s.PetSkillMasks[i], s.PetSkillMasks[i+s.Mode] = s.PetSkillMasks[i+s.Mode], s.PetSkillMasks[i]
				}
				if len(s.Reserves) == 2*s.Mode {
					s.Reserves[i], s.Reserves[i+s.Mode] = s.Reserves[i+s.Mode], s.Reserves[i]
				}
				s.Builds[i], s.Builds[i+s.Mode] = s.Builds[i+s.Mode], s.Builds[i]
				if len(s.PetBuilds) > 0 {
					s.PetBuilds[i], s.PetBuilds[i+s.Mode] = s.PetBuilds[i+s.Mode], s.PetBuilds[i]
				}
			}
		}
	}
	id, _ := Digest(s)
	return id
}

type RunOptions struct {
	InitialModel    *battlepolicy.Artifact
	Experiment      *Experiment // required only when creating an experiment-bound run.
	Directory       string
	Command         []string
	Resume          bool
	Config          *RunConfig // nil on resume: checkpoint settings are authoritative.
	Batches         int        // Additional completed batches in this invocation.
	Workers         int        // Collection processes only; zero/one preserves serial execution.
	StopAtDataBytes int64      // Optional logical-file threshold checked after committed checkpoints; zero disables.
	Stderr          io.Writer
	Progress        func(Progress) error
}
type Progress struct {
	DataBytes         int64            `json:"data_bytes,omitempty"`
	StopAtDataBytes   int64            `json:"stop_at_data_bytes,omitempty"`
	StorageCheckEvent string           `json:"storage_check_event,omitempty"`
	CollectionWorkers int              `json:"collection_workers,omitempty"`
	OpeningReport     string           `json:"opening_report,omitempty"`
	OpponentScores    []OpponentScore  `json:"opponent_scores,omitempty"`
	LeagueReport      string           `json:"league_report,omitempty"`
	Event             string           `json:"event"`
	CompletedBatches  int              `json:"completed_batches"`
	Games             uint64           `json:"games"`
	PendingMatches    int              `json:"pending_matches"`
	Learning          string           `json:"learning_state"`
	Report            *Report          `json:"report,omitempty"`
	WarmupGames       int              `json:"warmup_games"`
	WarmupEpochs      int              `json:"warmup_epochs"`
	WarmupReport      *ImitationReport `json:"warmup_report,omitempty"`
}

// Run is a recoverable offline training loop. Candidate weights are stored but
// never promoted to live play automatically. A single OS lock protects the
// data directory; a crashed process releases the lock without stale PID hacks.
func Run(ctx context.Context, o RunOptions) (resultErr error) {
	if o.StopAtDataBytes < 0 {
		return fmt.Errorf("stop-at-data-bytes must be nonnegative")
	}
	if o.Workers < 0 || o.Workers > MaxCollectionWorkers {
		return fmt.Errorf("collection workers must be 1..%d (zero uses one)", MaxCollectionWorkers)
	}
	if o.Workers == 0 {
		o.Workers = 1
	}
	if o.Directory == "" || len(o.Command) == 0 || o.Batches < 1 || o.Batches > 1000000 || o.Resume && o.Config != nil || !o.Resume && o.Config == nil {
		return fmt.Errorf("directory, environment command and batches required; resume uses stored settings")
	}
	if !o.Resume {
		if e := o.Config.Validate(); e != nil {
			return e
		}
	}
	if o.Resume && (o.Experiment != nil || o.InitialModel != nil) {
		return fmt.Errorf("resume uses the stored experiment")
	}
	if !o.Resume {
		if (o.InitialModel == nil) != (o.Config.InitialModel == "") {
			return fmt.Errorf("initial model and identity must be supplied together")
		}
		if (o.Experiment == nil) != (o.Config.Experiment == "") {
			return fmt.Errorf("experiment manifest and configuration identity must be supplied together")
		}
		if o.Experiment != nil {
			if e := o.Experiment.Validate(); e != nil {
				return e
			}
			id, _ := Digest(*o.Experiment)
			if id != o.Config.Experiment || !o.Experiment.matches(*o.Config) {
				return fmt.Errorf("training settings differ from frozen experiment")
			}
			if e := o.Experiment.validateInitialModel(o.InitialModel); e != nil {
				return e
			}
			if o.Experiment.InitialModel != o.Config.InitialModel {
				return fmt.Errorf("initial model config differs from experiment")
			}
		}
	}
	if e := os.MkdirAll(o.Directory, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(o.Directory, ".training.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = lockTraining(lock); e != nil {
		return fmt.Errorf("training directory is already in use: %w", e)
	}
	if o.Stderr == nil {
		log, e := os.OpenFile(filepath.Join(o.Directory, "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			return e
		}
		defer log.Close()
		o.Stderr = log
	}
	o.Stderr = &collectionLog{writer: o.Stderr}
	var c Checkpoint
	var learning LearningState
	if o.Resume {
		c, learning, e = loadCheckpoint(ctx, o.Directory)
		if e != nil {
			return e
		}
	} else {
		if _, e = os.Stat(filepath.Join(o.Directory, "latest.json")); e == nil {
			return fmt.Errorf("training state exists; use --resume")
		} else if !os.IsNotExist(e) {
			return e
		}
		if o.Experiment != nil {
			if _, e := SaveExperiment(filepath.Join(o.Directory, "experiments", o.Config.Experiment+".json"), *o.Experiment); e != nil {
				return e
			}
		}
		if o.InitialModel != nil {
			id, _ := Digest(*o.InitialModel)
			if id != o.Config.InitialModel || o.InitialModel.Network.Config != o.Config.Network {
				return fmt.Errorf("initial model identity/network mismatch")
			}
			if e = os.MkdirAll(filepath.Join(o.Directory, "initial-models"), 0700); e != nil {
				return e
			}
			if e = writeObject(filepath.Join(o.Directory, "initial-models", id+".json"), o.InitialModel); e != nil {
				return e
			}
		}
		learning, e = newInitialLearning(o.Config.Network, o.Config.Seed, o.InitialModel, o.Config.InitialPolicyScale)
		if e != nil {
			return e
		}
		features := battlepolicy.NetworkFeatures(o.Config.Network)
		c = Checkpoint{Schema: 2, Features: features, Actions: battlepolicy.ActionsForFeatures(features), Config: *o.Config}
		if c.Config.Pairing == BalancedPairing {
			c.Schema = 3
		}
		if c.Config.OpeningRollouts > 0 {
			c.Schema = 4
		}
		if c.Config.InitialPolicyScale != 0 {
			c.Schema = 5
		}
		if o.InitialModel != nil {
			c.TrainingGroups = append([]string(nil), o.InitialModel.TrainingGroups...)
		}
		if o.Experiment != nil {
			c.SelectionGroups = append([]string(nil), o.Experiment.SelectionGroups...)
		}
	}
	experiment, e := loadRunExperiment(o.Directory, c.Config)
	if e != nil {
		return e
	}
	engine, e := battleenv.Start(ctx, o.Command, o.Stderr)
	if e != nil {
		return e
	}
	defer func() { resultErr = errors.Join(resultErr, engine.Close()) }()
	meta := engine.Metadata()
	if e = meta.Validate(); e != nil {
		return e
	}
	if e = battlepolicy.ValidateFeatureEnvironment(c.Features, meta.Scenario); e != nil {
		return e
	}
	if c.Config.PetSkillMask != 0 && meta.Scenario != "controlled-battle-v8" {
		return fmt.Errorf("active pet skill configuration requires controlled-battle-v8")
	}
	if o.Resume && c.Environment != meta {
		return fmt.Errorf("engine rules/platform/scenario differ from checkpoint")
	}
	if experiment != nil && experiment.Environment != meta {
		return fmt.Errorf("engine rules/platform differ from frozen experiment")
	}
	initial, e := loadInitialModel(o.Directory, c.Config)
	if e != nil {
		return e
	}
	if initial != nil && !initial.CompatibleNativeEnvironment(meta) {
		return fmt.Errorf("initial model environment differs from training engine")
	}
	workers, e := startCollectionWorkers(ctx, engine, o.Command, o.Workers, o.Stderr)
	if e != nil {
		return e
	}
	defer func() { resultErr = errors.Join(resultErr, workers.closeAdditional()) }()
	if !o.Resume {
		c.Environment = meta
		c.Learning, e = saveLearning(filepath.Join(o.Directory, "learning"), learning)
		if e != nil {
			return e
		}
		if c.Config.InitialPolicyScale != 0 {
			c.Initialization, e = savePolicyInitialization(o.Directory, c.Config, initial, c.Config.InitialPolicyScale, learning)
			if e != nil {
				return e
			}
		}
		if e = saveCheckpoint(o.Directory, c); e != nil {
			return e
		}
	}
	leagueReports, e := loadLeague(ctx, o.Directory, c)
	if e != nil {
		return e
	}
	end := c.CompletedBatches + o.Batches
	if end > 1000000 {
		return fmt.Errorf("training batch counter limit exceeded")
	}
	emit := func(event string) error {
		p := Progress{CollectionWorkers: o.Workers, Event: event, CompletedBatches: c.CompletedBatches, Games: c.NextGame, PendingMatches: len(c.Pending), Learning: c.Learning, Report: c.LastReport}
		p.OpponentScores = c.OpponentScores
		if len(c.OpeningReports) > 0 {
			p.OpeningReport = c.OpeningReports[len(c.OpeningReports)-1]
		}
		if len(c.LeagueReports) > 0 {
			p.LeagueReport = c.LeagueReports[len(c.LeagueReports)-1]
		}
		if c.Warmup != nil {
			p.WarmupGames, p.WarmupEpochs, p.WarmupReport = len(c.Warmup.Shards), c.Warmup.CompletedEpochs, c.Warmup.LastReport
		}
		var limitErr error
		if o.StopAtDataBytes > 0 {
			bytes, err := trainingDataBytes(ctx, o.Directory)
			if err != nil {
				return fmt.Errorf("measure training data: %w", err)
			}
			p.DataBytes, p.StopAtDataBytes = bytes, o.StopAtDataBytes
			if bytes >= o.StopAtDataBytes {
				p.Event, p.StorageCheckEvent = "storage_limit_reached", event
				limitErr = &StorageLimitError{Bytes: bytes, Limit: o.StopAtDataBytes}
			}
		}
		if o.Progress != nil {
			if err := o.Progress(p); err != nil {
				return err
			}
		}
		return limitErr
	}
	if e = emit("ready"); e != nil {
		return e
	}
	if e = runWarmup(ctx, o.Directory, workers, &c, &learning, experiment, emit); e != nil {
		return e
	}
	opponents := map[string]*battlenet.Model[float32]{}
	for c.CompletedBatches < end {
		if e = ctx.Err(); e != nil {
			return e
		}
		for len(c.Pending) < c.Config.BatchMatches {
			jobs := make([]collectionJob, min(o.Workers, c.Config.BatchMatches-len(c.Pending)))
			for index := range jobs {
				game := c.NextGame + uint64(index)
				scenario, group := trainingScenario(c.Config, game, experiment)
				policies := [2]Policy{{Model: learning.Model}, {Model: learning.Model}}
				// Mix same-generation frozen self play with historical opponents;
				// keep only current-policy perspectives in the PPO batch below.
				choice := c.opponentAt(game)
				if choice.Rule != "" {
					policies[choice.Side] = Policy{Rule: choice.Rule, Features: c.Features}
				} else if choice.Learning != "" {
					id := choice.Learning
					if opponents[id] == nil {
						old, e := loadLearning(filepath.Join(o.Directory, "learning"), id)
						if e != nil {
							return e
						}
						opponents[id] = old.Model
					}
					policies[choice.Side] = Policy{Model: opponents[id]}
				}
				rng := [2]*rand.Rand{rand.New(rand.NewSource(gameSeed(c.Config.Seed, game, 3))), rand.New(rand.NewSource(gameSeed(c.Config.Seed, game, 4)))}
				jobs[index] = collectionJob{scenario: scenario, policies: policies, random: rng, group: group}
			}
			results, e := workers.collect(ctx, jobs)
			if e != nil {
				return e
			}
			for _, tr := range results {
				if e := ctx.Err(); e != nil {
					return e
				}
				shard, e := SaveShard(filepath.Join(o.Directory, "shards"), tr[:])
				if e != nil {
					return e
				}
				c.Pending = append(c.Pending, shard.Digest)
				c.NextGame++
				if e = saveCheckpoint(o.Directory, c); e != nil {
					return e
				}
				if e = emit("collected"); e != nil {
					return e
				}
			}
		}
		version, e := ModelDigest(learning.Model)
		if e != nil {
			return e
		}
		var batch []Trajectory
		versions := map[string]string{}
		league := LeagueReport{Schema: 1, Batch: c.CompletedBatches, Learner: version, Learning: c.Learning, Pool: append([]string(nil), c.Opponents...)}
		for pendingIndex, id := range c.Pending {
			episodes, _, e := LoadShard(filepath.Join(o.Directory, "shards"), id)
			if e != nil {
				return e
			}
			if c.Config.OpponentSampling == "weakness-v1" {
				game := c.NextGame - uint64(len(c.Pending)) + uint64(pendingIndex)
				result, e := makeLeagueGame(c, game, id, episodes, version)
				if e != nil {
					return e
				}
				expected, e := opponentVersion(o.Directory, c.opponentAt(game), version, versions)
				if e != nil {
					return e
				}
				if result.Version != expected {
					return fmt.Errorf("pending shard opponent differs from scheduled policy")
				}
				league.Games = append(league.Games, result)
			}
			for _, tr := range episodes {
				if tr.Rules != meta.Rules || tr.Platform != meta.Platform || tr.Environment != meta.Scenario {
					return fmt.Errorf("pending shard environment mismatch")
				}
				if experiment != nil {
					s, group := trainingScenario(c.Config, c.NextGame-uint64(len(c.Pending))+uint64(pendingIndex), experiment)
					scenarioID, _ := Digest(s)
					if tr.Group != group || tr.Scenario != scenarioID {
						return fmt.Errorf("pending shard differs from frozen experiment schedule")
					}
				}
				if tr.Policy == version {
					batch = append(batch, tr)
				}
			}
		}
		var openingReport OpeningReport
		var advantages []float64
		if c.Config.OpeningRollouts > 0 {
			batch, advantages, openingReport, e = openingBatch(ctx, o.Directory, c, learning.Model, c.Pending, c.NextGame-uint64(len(c.Pending)), experiment)
			if e != nil {
				return e
			}
		}
		report, e := trainWithOpeningAdvantages(ctx, learning.Model, learning.Optimizer, batch, c.Config.PPO, advantages)
		if e != nil {
			return e
		}
		addTrainingGroups(&c, batch)
		old := c.Learning
		c.Learning, e = saveLearning(filepath.Join(o.Directory, "learning"), learning)
		if e != nil {
			return e
		}
		if c.Config.OpeningRollouts > 0 {
			openingReport.Training = report
			id, err := saveOpeningReceipt(o.Directory, OpeningReceipt{Schema: 1, Batch: c.CompletedBatches, Before: old, After: c.Learning, Shards: append([]string(nil), c.Pending...), Report: openingReport})
			if err != nil {
				return err
			}
			c.OpeningReports = append(c.OpeningReports, id)
		}
		if c.Config.OpponentSampling == "weakness-v1" {
			league.Rows = scoreGames(league.Games)
			id, e := saveLeagueReport(o.Directory, league)
			if e != nil {
				return e
			}
			c.LeagueReports = append(c.LeagueReports, id)
			leagueReports = append(leagueReports, league)
			c.OpponentScores = leagueScores(leagueReports)
		}
		c.Opponents = c.retainOpponent(old)
		retained := map[string]bool{}
		for _, id := range c.Opponents {
			retained[id] = true
		}
		for id := range opponents {
			if !retained[id] {
				delete(opponents, id)
			}
		}
		c.Trained = append(c.Trained, c.Pending...)
		c.Pending = nil
		c.CompletedBatches++
		c.LastReport = &report
		if e = saveCheckpoint(o.Directory, c); e != nil {
			return e
		}
		if e = emit("trained_candidate"); e != nil {
			return e
		}
	}
	return nil
}

func addTrainingGroups(c *Checkpoint, episodes []Trajectory) {
	ids := make([]string, 0, len(episodes))
	for _, tr := range episodes {
		ids = append(ids, tr.Group)
	}
	addTrainingGroupIDs(c, ids)
}

func addTrainingGroupIDs(c *Checkpoint, ids []string) {
	groups := map[string]bool{}
	for _, id := range c.TrainingGroups {
		groups[id] = true
	}
	for _, id := range ids {
		groups[id] = true
	}
	c.TrainingGroups = nil
	for id := range groups {
		c.TrainingGroups = append(c.TrainingGroups, id)
	}
	sort.Strings(c.TrainingGroups)
}

// Old checkpoints omitted the pool; preserve their exact schedule instead of
// silently adding a new opponent when resuming with a newer CLI.
func (c RunConfig) ruleOpponents() []string {
	if len(c.RuleOpponents) == 0 {
		return []string{"basic", "focus", "guard-break", "defensive"}
	}
	return c.RuleOpponents
}
