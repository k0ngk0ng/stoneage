package arenaagent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

// Training remains in the installed Go client. The environment file contains
// an argv array, never a shell command assembled from player/config values.
func trainCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai train", flag.ContinueOnError)
	f.SetOutput(out)
	c := battletrain.DefaultRunConfig()
	environment := trainingEnvironmentFlag(f)
	directory := f.String("data-dir", "", "persistent training directory (may be a mounted Docker volume)")
	experimentPath := f.String("experiment", "", "frozen experiment manifest; use only its training split and scenario settings")
	mixedPath := f.String("mixed-experiment", "", "frozen mixed experiment; shared model/Adam, fresh per-mode PPO batches; no imitation warmup")
	fromModel := f.String("from-model", "", "declared parent weights for the experiment's target mode; fresh optimizer, default no warmup")
	initialPolicyScale := f.Float64("initial-policy-scale", 1, "experimental parent PPO initialization: scale final score weights once, 0<value<=1; default 1 unchanged; no imitation warmup; frozen on resume")
	demonstrations := f.String("demonstrations", "", "recorded demonstration JSONL+manifest; offline imitation, no engine; new data-dir required")
	batchEpisodes := f.Int("batch-episodes", 8, "demonstration imitation: complete episodes per update, 0 for full batch")
	gradientClip := f.Float64("gradient-clip", 1, "demonstration imitation: positive gradient norm limit")
	actionWeighting := f.String("action-weighting", "none", "demonstration imitation: none or sqrt-action-frequency-v1; frozen training-only class weights")
	resume := f.Bool("resume", false, "resume committed progress, settings, optimizer and opponent pool from --data-dir")
	batches := f.Int("batches", 1, "additional optimizer batches to complete")
	workers := f.Int("workers", 1, "native collection processes, 1..8; optimizer remains serial; may change on resume")
	stopAtDataBytes := f.Int64("stop-at-data-bytes", 0, "native training retained-file byte threshold; 0 disables; checked after checkpoints, may overshoot; preserves all data; may change on resume")
	output := f.String("output", "", "new candidate model path; default: content-addressed file under data-dir/models")
	f.StringVar(&c.OpponentSampling, "opponent-sampling", c.OpponentSampling, "uniform or weakness-v1: emphasize difficult opponents using committed training results")
	mixText := f.String("opponent-mix", "30,50,20", "rule,history,self integer percentages summing to 100; fixed on resume; empty history transfers to self")
	f.Int64Var(&c.Seed, "seed", c.Seed, "training seed (saved for reproducible resume)")
	f.IntVar(&c.PPO.Epochs, "epochs", c.PPO.Epochs, "PPO epochs per batch; demonstrations: additional full epochs, default 1; legacy SQLite default: 80")
	f.IntVar(&c.BatchMatches, "batch-matches", c.BatchMatches, "complete sampled matches per optimizer batch")
	openingRollouts := f.Int("opening-rollouts", 1, "experimental independent rollouts per identical opening; 1 disables, 2..64 must divide batch-matches; requires terminal outcomes")
	policyAdvantage := f.String("policy-advantage", "gae", "gae or experimental opening-loo (requires opening-rollouts >=2); frozen on resume")
	planScope := f.String("plan-scope", "team", "team commander (default) or member: offline independently deciding member baseline with shared weights and full observations; frozen on resume")
	planFeatures := f.String("plan-features", "none", "none (original plan GRU) or target-counts-v1: experimental explicit earlier-choice counts; new architecture, frozen on resume")
	f.IntVar(&c.Mode, "mode", c.Mode, "team size 1..5; each mode needs separate evaluation")
	f.IntVar(&c.Points, "points", c.Points, "equal integer allocation budget per player")
	f.IntVar(&c.PetPoints, "pet-points", c.PetPoints, "equal allocation budget per controlled pet; 0 disables pets")
	f.IntVar(&c.HealingMagic, "healing-magic", c.HealingMagic, "fixed healing armour: 0 none, 10 single target, 20 side; supplies 100 MP")
	f.IntVar(&c.HealingItems, "healing-items", c.HealingItems, "single-use small meats (template 1234) per character, 0..15")
	f.IntVar(&c.ReservePets, "reserve-pets", c.ReservePets, "extra pets per member, 0..2; each retains the active pet point budget")
	petSkillFlags(f, &c.PetSkillMask)
	f.IntVar(&c.Level, "level", c.Level, "controlled scenario level")
	f.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "collection cutoff (bootstrapped, not labeled a draw)")
	f.IntVar(&c.PPO.SequenceLength, "sequence-length", c.PPO.SequenceLength, "continuous turns per backpropagation segment")
	f.Float64Var(&c.PPO.LearningRate, "learning-rate", c.PPO.LearningRate, "Adam learning rate")
	f.Float64Var(&c.PPO.TargetKL, "target-kl", c.PPO.TargetKL, "PPO joint KL limit; new runs backtrack oversized updates, resume preserves stored guard")
	f.Float64Var(&c.PPO.Lambda, "gae-lambda", c.PPO.Lambda, "GAE trace parameter in [0,1]; 1 uses full collected returns, resume preserves stored value")
	f.Float64Var(&c.PPO.EntropyWeight, "entropy-weight", c.PPO.EntropyWeight, "nonnegative PPO conditional-entropy bonus; 0 disables the bonus, not action sampling")
	f.IntVar(&c.Warmup.Matches, "warmup-matches", c.Warmup.Matches, "rule-teacher matches before PPO; multiple of 4, 0 disables warmup")
	f.IntVar(&c.Warmup.Epochs, "warmup-epochs", c.Warmup.Epochs, "behavior-cloning epochs over teacher matches (saved separately from PPO batches)")
	f.IntVar(&c.Warmup.Update.BatchEpisodes, "warmup-batch-episodes", c.Warmup.Update.BatchEpisodes, "complete recurrent trajectories per imitation update, 0 keeps legacy full-batch order")
	f.Float64Var(&c.Warmup.Update.LearningRate, "warmup-learning-rate", c.Warmup.Update.LearningRate, "Adam learning rate for behavior cloning")
	f.StringVar(&c.Warmup.Update.ActionWeighting, "warmup-action-weighting", "none", "none or sqrt-action-frequency-v1: experimental training-only action class weighting; fixed on resume")
	var teachers, ruleOpponents pathsFlag
	f.Var(&ruleOpponents, "rule-opponent", "rule opponent for PPO sampling (repeatable): basic, focus, guard-break, defensive, sustain, control, independent-control; default: original five")
	f.Var(&teachers, "warmup-teacher", "frozen rule teacher (repeatable): basic, focus, guard-break, defensive, sustain, control, independent-control; default: original five")
	var databases pathsFlag
	f.Var(&databases, "database", "legacy SQLite dataset; selects the v1 linear trainer (repeatable)")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional training arguments")
	}
	provided := map[string]bool{}
	f.Visit(func(item *flag.Flag) { provided[item.Name] = true })
	if math.IsNaN(*initialPolicyScale) || math.IsInf(*initialPolicyScale, 0) || *initialPolicyScale <= 0 || *initialPolicyScale > 1 || float32(*initialPolicyScale) <= 0 {
		return fmt.Errorf("--initial-policy-scale must be finite in (0,1]")
	}
	if provided["initial-policy-scale"] && !*resume && *fromModel == "" {
		return fmt.Errorf("--initial-policy-scale requires --from-model")
	}
	if *initialPolicyScale != 1 {
		c.InitialPolicyScale = *initialPolicyScale
	}
	for _, value := range []*string{actionWeighting, &c.Warmup.Update.ActionWeighting} {
		if *value == "none" {
			*value = ""
		}
		if *value != "" && *value != battletrain.SqrtActionFrequency {
			return fmt.Errorf("action weighting must be none or sqrt-action-frequency-v1")
		}
	}
	mixedResume := false
	if *resume && *directory != "" {
		var err error
		mixedResume, err = hasMixedCheckpoint(*directory)
		if err != nil {
			return err
		}
	}
	if provided["mixed-experiment"] || mixedResume {
		return trainMixedCommand(ctx, mixedCLIOptions{Directory: *directory, Environment: *environment, Experiment: *mixedPath, FromModel: *fromModel, Output: *output, Resume: *resume, Batches: *batches, Workers: *workers, StopAtDataBytes: *stopAtDataBytes, Config: c, Rules: ruleOpponents, Mix: *mixText, PlanScope: *planScope, PlanFeatures: *planFeatures, Provided: provided}, out)
	}
	demonstrationResume := false
	if *resume && *directory != "" {
		var err error
		demonstrationResume, err = hasDemonstrationCheckpoint(*directory)
		if err != nil {
			return err
		}
	}
	if provided["demonstrations"] || demonstrationResume {
		epochs := c.PPO.Epochs
		if !provided["epochs"] {
			epochs = 1
		}
		return trainDemonstrationsCommand(ctx, *directory, *demonstrations, *resume, epochs, c.Seed,
			battletrain.ImitationConfig{ActionWeighting: *actionWeighting, BatchEpisodes: *batchEpisodes, SequenceLength: c.PPO.SequenceLength, LearningRate: c.PPO.LearningRate, GradientClip: *gradientClip}, provided, out)
	}
	for _, flag := range []string{"batch-episodes", "gradient-clip", "action-weighting"} {
		if provided[flag] {
			return fmt.Errorf("--%s requires --demonstrations training", flag)
		}
	}
	if len(databases) > 0 {
		for name := range provided {
			if name != "database" && name != "output" && name != "seed" && name != "epochs" {
				return fmt.Errorf("--%s cannot be combined with legacy --database training", name)
			}
		}
		epochs := c.PPO.Epochs
		if !provided["epochs"] {
			epochs = 80
		}
		v, e := Train(databases, *output, c.Seed, epochs)
		if e != nil {
			return e
		}
		_, e = out.Write(append(enc(v), '\n'))
		return e
	}
	if *workers < 1 || *workers > battletrain.MaxCollectionWorkers {
		return fmt.Errorf("--workers must be 1..%d", battletrain.MaxCollectionWorkers)
	}
	if *stopAtDataBytes < 0 {
		return fmt.Errorf("--stop-at-data-bytes must be nonnegative")
	}
	if *environment == "" || *directory == "" {
		return fmt.Errorf("PPO training requires --environment and --data-dir; existing SQLite training uses --database")
	}
	if *resume {
		for name := range provided {
			switch name {
			case "environment", "data-dir", "resume", "batches", "output", "workers", "stop-at-data-bytes":
			default:
				return fmt.Errorf("--%s cannot change stored settings during --resume", name)
			}
		}
	}
	if provided["opponent-mix"] {
		parts := strings.Split(*mixText, ",")
		if len(parts) != 3 {
			return fmt.Errorf("--opponent-mix requires rule,history,self integer percentages")
		}
		mix := &battletrain.OpponentMix{}
		values := []*int{&mix.Rules, &mix.History, &mix.Self}
		for i, part := range parts {
			value, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return fmt.Errorf("--opponent-mix requires rule,history,self integer percentages")
			}
			*values[i] = value
		}
		if err := mix.Validate(); err != nil {
			return err
		}
		c.OpponentMix = mix
	}
	if *openingRollouts < 1 || *openingRollouts > 64 {
		return fmt.Errorf("--opening-rollouts must be 1..64")
	}
	if *openingRollouts > 1 {
		c.OpeningRollouts = *openingRollouts
	}
	switch *policyAdvantage {
	case "gae":
	case "opening-loo":
		c.PolicyAdvantage = *policyAdvantage
	default:
		return fmt.Errorf("--policy-advantage must be gae or opening-loo")
	}
	switch *planScope {
	case "team":
	case "member":
		c.Network.PlanScope = "member"
	default:
		return fmt.Errorf("--plan-scope must be team or member")
	}
	switch *planFeatures {
	case "none":
	case "target-counts-v1":
		c.Network.PlanFeatures = *planFeatures
	default:
		return fmt.Errorf("--plan-features must be none or target-counts-v1")
	}
	command, e := loadTrainingEnvironment(*environment)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	var cfg *battletrain.RunConfig
	var experiment *battletrain.Experiment
	var initial *battlepolicy.Artifact
	if !*resume {
		if *fromModel != "" {
			if *experimentPath == "" {
				return fmt.Errorf("--from-model requires an experiment created with the same parent")
			}
			a, e := battlepolicy.LoadArtifact(*fromModel)
			if e != nil {
				return e
			}
			initial = &a
			if provided["plan-features"] && c.Network.PlanFeatures != a.Network.Config.PlanFeatures {
				return fmt.Errorf("--plan-features cannot change the parent model's architecture; start a new training without --from-model")
			}
			if provided["plan-scope"] && c.Network.PlanScope != a.Network.Config.PlanScope {
				return fmt.Errorf("--plan-scope cannot change the parent model's architecture; train the baseline in a new directory without --from-model")
			}
			c.InitialModel, _ = battletrain.Digest(a)
			c.Network = a.Network.Config
			if !provided["warmup-matches"] {
				c.Warmup.Matches = 0
			}
		}
		if *experimentPath != "" {
			for _, name := range []string{"mode", "points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level", "max-turns"} {
				if provided[name] {
					return fmt.Errorf("--%s is frozen by --experiment", name)
				}
			}
			x, e := battletrain.LoadExperiment(*experimentPath)
			if e != nil {
				return e
			}
			experiment = &x
			c.Pairing = x.Pairing
			c.Experiment, _ = battletrain.Digest(x)
			c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = x.Mode, x.Points, x.PetPoints, x.Level, x.MaxTurns
			c.HealingMagic = x.HealingMagic
			c.HealingItems = x.HealingItems
			c.ReservePets = x.ReservePets
			c.PetSkillMask = x.PetSkillMask
		}
		if len(ruleOpponents) > 0 {
			c.RuleOpponents = append([]string(nil), ruleOpponents...)
		}
		if len(teachers) > 0 {
			c.Warmup.Teachers = append([]string(nil), teachers...)
		}
		c.Warmup.Update.SequenceLength = c.PPO.SequenceLength
		cfg = &c
	}
	e = battletrain.Run(ctx, battletrain.RunOptions{Directory: *directory, Command: command, Resume: *resume, Config: cfg, Experiment: experiment, InitialModel: initial, Batches: *batches, Workers: *workers, StopAtDataBytes: *stopAtDataBytes, Progress: func(p battletrain.Progress) error { _, e := out.Write(append(enc(p), '\n')); return e }})
	if e != nil {
		if errors.Is(e, context.Canceled) || ctx.Err() == context.Canceled {
			// A long experimental run may have substantial historical evidence.
			// Ctrl-C must not start an unbounded verification before returning.
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
			checkpoint, _, loadErr := battletrain.LoadCheckpointContext(statusCtx, *directory)
			statusCancel()
			warmupGames, warmupEpochs := 0, 0
			if checkpoint.Warmup != nil {
				warmupGames, warmupEpochs = len(checkpoint.Warmup.Shards), checkpoint.Warmup.CompletedEpochs
			}
			_, writeErr := out.Write(append(enc(Object{"event": "training_interrupted", "data_dir": *directory, "checkpoint_available": loadErr == nil, "completed_games": checkpoint.NextGame, "pending_matches": len(checkpoint.Pending), "warmup_games": warmupGames, "warmup_epochs": warmupEpochs}), '\n'))
			return writeErr
		}
		return e
	}
	path, e := battletrain.ExportCheckpointCandidateContext(ctx, *directory, "", *output)
	if e != nil {
		return e
	}
	_, e = out.Write(append(enc(Object{"event": "candidate_saved", "model": path, "status": "candidate", "arena_certified": false}), '\n'))
	return e
}

func loadTrainingEnvironment(path string) ([]string, error) {
	var env struct {
		Schema  int      `json:"schema_version"`
		Command []string `json:"command"`
	}
	envFile, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer envFile.Close()
	info, e := envFile.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("environment configuration too large or not a regular file")
	}
	dec := json.NewDecoder(io.LimitReader(envFile, (1<<20)+1))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&env); e != nil {
		return nil, e
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("trailing environment configuration")
	}
	if env.Schema != 1 || len(env.Command) == 0 || len(env.Command) > 128 {
		return nil, fmt.Errorf("environment needs schema_version=1 and a command argv array")
	}
	for i, arg := range env.Command {
		if len(arg) > 8192 || i == 0 && arg == "" {
			return nil, fmt.Errorf("invalid environment argv")
		}
	}
	return env.Command, nil
}
