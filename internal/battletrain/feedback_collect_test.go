package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestFeedbackCollectionScheduleAndValidation(t *testing.T) {
	x, e, artifacts := feedbackStorageFixture(t)
	a := artifacts[e.Source.Policy]
	c := FeedbackCollectionConfig{Seed: 731, Matches: 16, Teacher: "sustain", Opponents: []string{"basic", "control"}}
	if err := c.validate(x); err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for _, f := range x.groups("train") {
		groups[f.Group] = true
	}
	for game := 0; game < c.Matches; game++ {
		j := feedbackCollectionJob(x, a, c, game)
		repeat := feedbackCollectionJob(x, a, c, game)
		if !reflect.DeepEqual(j.scenario, repeat.scenario) || !groups[j.group] || j.policies[game%2].Model != a.Network || j.policies[1-game%2].Rule != c.Opponents[game/familyGames(x.Pairing)] || j.random[game%2].Int63() != repeat.random[game%2].Int63() {
			t.Fatal("unstable feedback schedule or policy/side mismatch")
		}
	}
	for _, kind := range []string{"no-games", "unpaired", "oversized", "teacher", "opponent", "duplicate", "unvisited-opponent", "workers", "negative-limit", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			bad := c
			bad.Opponents = append([]string(nil), c.Opponents...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := filepath.Join(t.TempDir(), "uncreated")
			o := FeedbackCollectionOptions{Directory: root, Experiment: &x, Model: &a, Config: &bad}
			switch kind {
			case "no-games":
				bad.Matches = 0
			case "unpaired":
				bad.Matches = 3
			case "oversized":
				bad.Matches = 264
			case "teacher":
				bad.Teacher = "unknown"
			case "opponent":
				bad.Opponents = []string{"unknown"}
			case "duplicate":
				bad.Opponents = []string{"basic", "basic"}
			case "unvisited-opponent":
				bad.Matches = 8
			case "workers":
				o.Workers = MaxCollectionWorkers + 1
			case "negative-limit":
				o.StopAtDataBytes = -1
			case "cancel":
				cancel()
			}
			if err := CollectFeedback(ctx, o); err == nil {
				t.Fatal("invalid collection accepted")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid collection created files")
			}
		})
	}
}

func TestFeedbackCollectionReadyCheckpointAndStopWithoutWorker(t *testing.T) {
	x, e, artifacts := feedbackStorageFixture(t)
	a := artifacts[e.Source.Policy]
	cfg := FeedbackCollectionConfig{Seed: 71, Matches: 8, Teacher: "control", Opponents: []string{"basic"}}
	ctx, root := context.Background(), filepath.Join(t.TempDir(), "run")
	before, _ := Digest(a)
	var progress FeedbackCollectionProgress
	err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: root, Experiment: &x, Model: &a, Config: &cfg, StopAtDataBytes: 1, Progress: func(p FeedbackCollectionProgress) error { progress = p; return nil }})
	var limit *StorageLimitError
	if !errors.As(err, &limit) || progress.StorageTriggerStage != "feedback_collection_ready" || progress.Games != 0 {
		t.Fatal("did not stop before starting absent worker", err)
	}
	c, gotX, gotA, err := LoadFeedbackCollection(ctx, root)
	if err != nil || len(c.Games) != 0 || !reflect.DeepEqual(gotX, x) {
		t.Fatal("ready checkpoint not recoverable", err)
	}
	after, _ := Digest(gotA)
	if before != after {
		t.Fatal("initial behavior changed")
	}
	if _, err := FeedbackCollectionInputs(root, c); err == nil {
		t.Fatal("partial round accepted for training")
	}
	stop := errors.New("stop at ready")
	if err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: root, Resume: true, Progress: func(p FeedbackCollectionProgress) error {
		if p.Event == "feedback_collection_ready" {
			if err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: root, Resume: true}); err == nil {
				t.Fatal("concurrent writer acquired lock")
			}
			return stop
		}
		return nil
	}}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	pointer, _ := os.ReadFile(filepath.Join(root, FeedbackCollectionPointer))
	for _, options := range []FeedbackCollectionOptions{
		{Directory: root, Resume: true, Config: &cfg},
		{Directory: root, Resume: true, Model: &a},
		{Directory: root, Resume: true, Experiment: &x},
		{Directory: root, Config: &cfg, Model: &a, Experiment: &x},
	} {
		if err := CollectFeedback(ctx, options); err == nil {
			t.Fatal("collection override accepted")
		}
	}
	current, _ := os.ReadFile(filepath.Join(root, FeedbackCollectionPointer))
	if string(pointer) != string(current) {
		t.Fatal("rejection changed collection progress")
	}
}

func TestNativeFeedbackCollectionResumeAndTrainingParity(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	meta := engine.Metadata()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	ec := DefaultEvaluationConfig()
	ec.Mode, ec.MaxTurns, ec.HealingMagic, ec.HealingItems, ec.ReservePets = 2, 2, 20, 2, 2
	x, err := NewExperiment(ctx, meta, ec, [3]int{2, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	a := experimentCandidate(t, x)
	cfg := FeedbackCollectionConfig{Seed: 743, Matches: 16, Teacher: "sustain", Opponents: []string{"basic", "control"}}
	parent := t.TempDir()
	dirs := []string{filepath.Join(parent, "serial"), filepath.Join(parent, "parallel"), filepath.Join(parent, "resumed")}
	before, _ := Digest(a)
	var readyBytes int64
	for i, dir := range dirs {
		options := FeedbackCollectionOptions{Directory: dir, Command: command, Experiment: &x, Model: &a, Config: &cfg, Workers: 1, Stderr: io.Discard}
		if i > 0 {
			options.Workers = 2
		}
		stop := errors.New("stop after first game in worker window")
		options.Progress = func(p FeedbackCollectionProgress) error {
			if p.Event == "feedback_collection_ready" && i == 0 {
				var err error
				readyBytes, err = trainingDataBytes(ctx, dir)
				return err
			}
			if p.Event == "feedback_game_committed" && p.Games == 1 && i == 2 {
				return stop
			}
			return nil
		}
		err := CollectFeedback(ctx, options)
		if i == 2 {
			if !errors.Is(err, stop) {
				t.Fatal("partial-window interruption failed", err)
			}
			c, _, _, err := LoadFeedbackCollection(ctx, dir)
			if err != nil || len(c.Games) != 1 {
				t.Fatal("first committed game lost", err)
			}
			if _, err := FeedbackCollectionInputs(dir, c); err == nil {
				t.Fatal("incomplete collection accepted")
			}
			err = CollectFeedback(ctx, FeedbackCollectionOptions{Directory: dir, Command: command, Resume: true, Workers: 1, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	// Threshold after the first raw+label commit is also resumable, with a
	// different worker count and without altering the frozen match budget.
	stopped := filepath.Join(parent, "storage-stopped")
	var stage string
	err = CollectFeedback(ctx, FeedbackCollectionOptions{Directory: stopped, Command: command, Experiment: &x, Model: &a, Config: &cfg, Workers: 1, Stderr: io.Discard, StopAtDataBytes: readyBytes + 1, Progress: func(p FeedbackCollectionProgress) error {
		if p.Event == "storage_limit_reached" {
			stage = p.StorageTriggerStage
		}
		return nil
	}})
	var limit *StorageLimitError
	if !errors.As(err, &limit) || stage != "feedback_game_committed" {
		t.Fatal("wrong storage boundary", err, stage)
	}
	if err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: stopped, Command: command, Resume: true, Workers: 2, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	dirs = append(dirs, stopped)
	var expected LearningState
	for i, dir := range dirs {
		c, _, _, err := LoadFeedbackCollection(ctx, dir)
		if err != nil || len(c.Games) != 16 {
			t.Fatal("collection verification failed", err)
		}
		inputs, err := FeedbackCollectionInputs(dir, c)
		if err != nil {
			t.Fatal(err)
		}
		update := DefaultWarmupConfig().Update
		update.BatchEpisodes, update.SequenceLength = 4, 2
		train := filepath.Join(dir, "train")
		if err := RunFeedback(ctx, FeedbackRunOptions{Directory: train, Datasets: inputs, Initial: &a, Update: &update, Epochs: 1}); err != nil {
			t.Fatal(err)
		}
		_, state, examples, err := LoadFeedbackCheckpoint(ctx, train)
		if err != nil || len(examples) != 16 {
			t.Fatal(err)
		}
		if i == 0 {
			expected = state
		} else if !reflect.DeepEqual(state, expected) {
			t.Fatal("workers/resume changed numerical training", i)
		}
		// A fully committed repeat needs no executable and collects no games.
		if err := CollectFeedback(ctx, FeedbackCollectionOptions{Directory: dir, Resume: true}); err != nil {
			t.Fatal("completed collection tried to start a worker", err)
		}
	}
	after, _ := Digest(a)
	if before != after {
		t.Fatal("collection mutated caller's frozen model")
	}
	// Rehashing a modified future budget/teacher does not bypass the recipe
	// bound in already committed games.
	dir := dirs[0]
	c, _, _, err := LoadFeedbackCollection(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	original := c
	c.Config.Matches += 8
	if err := saveFeedbackCollection(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadFeedbackCollection(ctx, dir); err == nil {
		t.Fatal("rebound collection budget accepted")
	}
	if err := saveFeedbackCollection(dir, original); err != nil {
		t.Fatal(err)
	}
	// A different legal action with correctly recomputed probabilities can
	// pass generic sampled-trajectory validation. It must still fail this
	// collection's exact action-RNG binding, not merely a checksum check.
	c = original
	c.Games = append([]FeedbackCollectedGame(nil), original.Games...)
	pair, _, err := LoadShard(filepath.Join(dir, "games"), c.Games[0].Raw)
	if err != nil {
		t.Fatal(err)
	}
	changed := &pair[0]
	if len(changed.Steps[0].Frame.Slots[0].Candidates) < 2 {
		t.Fatal("fixture lacks alternative action")
	}
	changed.Steps[0].Choices[0] = (changed.Steps[0].Choices[0] + 1) % len(changed.Steps[0].Frame.Slots[0].Candidates)
	var memory []float32
	for i := range changed.Steps {
		s := &changed.Steps[i]
		g := &battlenet.Graph[float32]{}
		o, err := battlepolicy.Forward(ctx, a.Network.Bind(g), s.Frame, g.New(1, a.Network.Config.Width, memory), s.Choices, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.LogProb, s.Value, s.ConditionalLogProbs = o.LogProb.Data[0], o.Value.Data[0], o.ConditionalLogProbs
		memory = append([]float32(nil), o.Memory.Data...)
	}
	if changed.Truncated {
		changed.Bootstrap, err = (Policy{Model: a.Network}).value(ctx, *changed.Next, memory)
		if err != nil {
			t.Fatal(err)
		}
	}
	labels, err := LabelRuleFeedback(ctx, *changed, cfg.Teacher)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := SaveFeedbackDataset(ctx, filepath.Join(dir, "feedback-data"), x, []FeedbackExample{{Source: *changed, Targets: labels, Behavior: Policy{Model: a.Network}}}, map[string]battlepolicy.Artifact{changed.Policy: a})
	if err != nil {
		t.Fatal("alternate sample must pass generic feedback validation", err)
	}
	shard, err := SaveShard(filepath.Join(dir, "games"), pair)
	if err != nil {
		t.Fatal(err)
	}
	c.Games[0].Raw, c.Games[0].Dataset = shard.Digest, dataset
	if err := saveFeedbackCollection(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadFeedbackCollection(ctx, dir); err == nil || !strings.Contains(err.Error(), "frozen RNG") {
		t.Fatal("alternate sample bypassed seed binding or failed for an unrelated reason", err)
	}
	if err := saveFeedbackCollection(dir, original); err != nil {
		t.Fatal(err)
	}
	t.Log("four 16-game native collections: serial, two workers, partial-window resume and storage-stop resume; all learner-only aggregates produce identical model+Adam; short-cutoff functional fixtures, not strength")
}
