package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTrainingDataBytes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "model"), []byte("model"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "unrelated"), []byte("do not count"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, alias} {
		got, err := trainingDataBytes(context.Background(), path)
		if err != nil || got != 5 {
			t.Fatal(path, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := trainingDataBytes(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := trainingDataBytes(context.Background(), filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := trainingDataBytes(context.Background(), filepath.Join(root, "model")); err == nil {
		t.Fatal("file accepted as root")
	}
	path := filepath.Join(t.TempDir(), "must-not-exist")
	if err := Run(context.Background(), RunOptions{Directory: path, StopAtDataBytes: -1}); err == nil {
		t.Fatal("negative threshold accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid threshold wrote data")
	}
}

func TestNativeStorageThresholdResume(t *testing.T) {
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
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.Mode, c.MaxTurns, c.BatchMatches = 2, 2, 2
	c.Warmup.Matches, c.Warmup.Epochs = 4, 1
	c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
	invoke := func(dir string, resume bool, limit int64, progress func(Progress) error) error {
		o := RunOptions{Directory: dir, Command: command, Workers: 2, Resume: resume, Batches: 1, StopAtDataBytes: limit, Progress: progress, Stderr: io.Discard}
		if !resume {
			o.Config = &c
		}
		return Run(ctx, o)
	}
	serial := t.TempDir()
	if err := invoke(serial, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	wantCP, want, err := LoadCheckpoint(serial)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"warmup_collected", "warmup_trained", "collected", "trained_candidate"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			padding := filepath.Join(root, "test-retained-file")
			const limit int64 = 1 << 30
			var stopped Progress
			err := invoke(root, false, limit, func(p Progress) error {
				if p.StopAtDataBytes != limit || p.DataBytes <= 0 {
					t.Fatal("missing resource measurement", p)
				}
				if p.Event == "storage_limit_reached" {
					stopped = p
					return nil
				}
				prepare := stage == "warmup_collected" && p.Event == "ready" ||
					stage == "warmup_trained" && p.Event == "warmup_collected" && p.WarmupGames == 4 ||
					stage == "collected" && p.Event == "warmup_trained" ||
					stage == "trained_candidate" && p.Event == "collected" && p.Games == 2
				if prepare {
					// Sparse logical length exercises the threshold without allocating a GiB.
					f, err := os.Create(padding)
					if err != nil {
						return err
					}
					err = f.Truncate(limit)
					closeErr := f.Close()
					if err != nil {
						return err
					}
					return closeErr
				}
				return nil
			})
			var limitErr *StorageLimitError
			if !errors.As(err, &limitErr) || stopped.StorageCheckEvent != stage || limitErr.Bytes < limit {
				t.Fatal("wrong threshold boundary", stage, stopped, err)
			}
			before, _, err := LoadCheckpoint(root)
			if err != nil {
				t.Fatal(err)
			}
			// No callback still enforces the threshold; no extra committed work.
			if err := invoke(root, true, limit, nil); !errors.As(err, &limitErr) {
				t.Fatal(err)
			}
			after, _, err := LoadCheckpoint(root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("blocked resume advanced checkpoint", err)
			}
			if info, err := os.Stat(padding); err != nil || info.Size() != limit {
				t.Fatal("retained data removed", err)
			}
			if stage != "trained_candidate" {
				if err := invoke(root, true, math.MaxInt64, nil); err != nil {
					t.Fatal(err)
				}
			}
			gotCP, got, err := LoadCheckpoint(root)
			if err != nil || !reflect.DeepEqual(want, got) {
				t.Fatal("resource stop/resume changed complete model or Adam", err)
			}
			// Native restarts deliberately create new match/stream namespaces.
			// LoadCheckpoint validates their shard/league references; compare
			// numerical state and provenance, not routing-dependent content IDs.
			if len(gotCP.Trained) != len(wantCP.Trained) || len(gotCP.Warmup.Shards) != len(wantCP.Warmup.Shards) || len(gotCP.LeagueReports) != len(wantCP.LeagueReports) {
				t.Fatal("missing retained evidence")
			}
			wantCopy := wantCP
			wantWarmup, gotWarmup := *wantCP.Warmup, *gotCP.Warmup
			wantWarmup.Shards, gotWarmup.Shards = nil, nil
			wantCopy.Warmup, gotCP.Warmup = &wantWarmup, &gotWarmup
			wantCopy.Trained, gotCP.Trained = nil, nil
			wantCopy.LeagueReports, gotCP.LeagueReports = nil, nil
			if !reflect.DeepEqual(wantCopy, gotCP) {
				t.Fatal("resource stop/resume changed numerical reports, schedule or provenance")
			}
		})
	}
}
