package battletrain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func datasetTeacherEpisodes(t *testing.T) []Trajectory {
	t.Helper()
	var episodes []Trajectory
	for i := 0; i < 5; i++ {
		tr := teacherTrajectory(t, testModel(t), i+2)
		b, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		b = bytes.ReplaceAll(b, []byte(`"test"`), []byte(fmt.Sprintf(`"dataset-%d"`, i)))
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatal(err)
		}
		if i%2 == 1 {
			p := Policy{Rule: "focus"}
			tr.Policy, _ = p.Version()
			tr.PolicyKind = p.Kind()
			for j := range tr.Steps {
				tr.Steps[j].Choices, err = battlepolicy.RuleChoices(tr.Steps[j].Frame, p.Rule)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tr.Validate(); err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, tr)
	}
	return episodes
}

func TestTeacherDatasetMatchesLegacyEpochsAndRestore(t *testing.T) {
	for _, batch := range []int{0, 2, 8} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			episodes := datasetTeacherEpisodes(t)
			original, _ := json.Marshal(episodes)
			var data teacherDataset
			for _, tr := range episodes {
				if err := data.add(context.Background(), tr); err != nil {
					t.Fatal(err)
				}
			}
			legacy, compact := testModel(t), testModel(t)
			lo, co := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
			config := DefaultWarmupConfig().Update
			config.BatchEpisodes, config.SequenceLength = batch, 2
			for epoch := 0; epoch < 3; epoch++ {
				want, err := legacyFullTrajectoryImitate(context.Background(), legacy, lo, episodes, config)
				if err != nil {
					t.Fatal(err)
				}
				got, err := data.update(context.Background(), compact, co, config)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(legacy, compact) || !reflect.DeepEqual(lo, co) {
					t.Fatal("changed report, model or Adam at epoch", epoch)
				}
				// Match an actual serialized restore while retaining prepared data.
				b, err := json.Marshal(LearningState{Schema: 1, Model: compact, Optimizer: co})
				if err != nil {
					t.Fatal(err)
				}
				var restored LearningState
				if err = json.Unmarshal(b, &restored); err != nil {
					t.Fatal(err)
				}
				compact, co = restored.Model, restored.Optimizer
			}
			var oldGroups, newGroups Checkpoint
			addTrainingGroups(&oldGroups, episodes)
			addTrainingGroupIDs(&newGroups, data.groups)
			if !reflect.DeepEqual(oldGroups, newGroups) {
				t.Fatal("changed training provenance")
			}
			after, _ := json.Marshal(episodes)
			if !bytes.Equal(original, after) {
				t.Fatal("mutated source trajectory")
			}
		})
	}
}

func TestTeacherDatasetRejectsInvalidSourceWithoutPartialAppend(t *testing.T) {
	for _, kind := range []string{"duplicate", "raw-frame-mismatch", "rules", "teacher", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			episodes := datasetTeacherEpisodes(t)
			var data teacherDataset
			if err := data.add(context.Background(), episodes[0]); err != nil {
				t.Fatal(err)
			}
			before := fmt.Sprintf("%#v", data)
			bad := episodes[1]
			ctx := context.Background()
			switch kind {
			case "duplicate":
				bad = episodes[0]
			case "raw-frame-mismatch":
				bad.Steps[0].Frame.Entities[0][5] += 1
			case "rules":
				bad.Rules = strings.Repeat("a", 64)
				if err := bad.Validate(); err != nil {
					t.Fatal("fixture itself invalid", err)
				}
			case "teacher":
				bad.Policy, _ = (Policy{Rule: "control"}).Version()
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := data.add(ctx, bad); err == nil {
				t.Fatal("accepted invalid source")
			}
			if before != fmt.Sprintf("%#v", data) {
				t.Fatal("failed append changed prepared data")
			}
		})
	}
}

func TestTeacherDatasetCancellationRollsBackLaterMinibatch(t *testing.T) {
	var data teacherDataset
	for _, tr := range datasetTeacherEpisodes(t) {
		if err := data.add(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
	}
	config := DefaultWarmupConfig().Update
	config.BatchEpisodes, config.SequenceLength = 1, 2
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, err := data.update(probe, testModel(t), &battlenet.Adam[float32]{}, config); err != nil {
		t.Fatal(err)
	}
	ctx := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls * 3 / 4}
	m, o := testModel(t), &battlenet.Adam[float32]{}
	before, _ := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: o})
	r, err := data.update(ctx, m, o, config)
	if !errors.Is(err, context.Canceled) || r.OptimizerUpdates < 1 || r.OptimizerUpdates >= 5 {
		t.Fatal("did not interrupt a later minibatch", r, err)
	}
	after, _ := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: o})
	if !bytes.Equal(before, after) {
		t.Fatal("cancellation leaked an update")
	}
}

// Optional measurement on already collected immutable data. Memory numbers are
// observations, never platform-dependent pass/fail thresholds or RSS claims.
func TestTeacherDatasetRecordedRetainedHeap(t *testing.T) {
	root := os.Getenv("STONEAGE_WARMUP_RESIDENT_DIRECTORY")
	if root == "" {
		t.Skip("explicit completed data directory required")
	}
	var pointer struct {
		Checkpoint string `json:"checkpoint"`
	}
	b, err := os.ReadFile(filepath.Join(root, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &pointer); err != nil {
		t.Fatal(err)
	}
	var cp Checkpoint
	b, err = os.ReadFile(filepath.Join(root, "checkpoints", pointer.Checkpoint+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.Warmup == nil || len(cp.Warmup.Shards) < 16 || cp.Warmup.CompletedEpochs < 1 {
		t.Fatal("requires completed warmup")
	}
	heap := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	base := heap()
	var episodes []Trajectory
	for _, id := range cp.Warmup.Shards[:16] {
		tr, _, err := LoadShard(filepath.Join(root, "shards"), id)
		if err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, tr...)
	}
	full := heap()
	runtime.KeepAlive(episodes)
	var data teacherDataset
	for _, tr := range episodes {
		if err := data.add(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
	}
	episodes = nil
	compact := heap()
	runtime.KeepAlive(data)
	if len(data.sequences) != 32 || data.turns == 0 || data.actions == 0 {
		t.Fatal("missing prepared data")
	}
	t.Logf("shards=16 episodes=%d turns=%d actions=%d baseline_heap=%d full_trajectory_heap=%d prepared_heap=%d retained_reduction_bytes=%d", len(data.sequences), data.turns, data.actions, base, full, compact, int64(full)-int64(compact))
}
