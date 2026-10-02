package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCollectionWindowOrderedAndJoined(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan int, 3)
	finished := make(chan int, 3)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	type output struct {
		rows [][2]Trajectory
		err  error
	}
	done := make(chan output, 1)
	go func() {
		r, e := collectWindow(ctx, 3, func(ctx context.Context, i int) ([2]Trajectory, error) {
			started <- i
			select {
			case <-gates[i]:
			case <-ctx.Done():
				return [2]Trajectory{}, ctx.Err()
			}
			finished <- i
			return [2]Trajectory{{Side: i}}, nil
		})
		done <- output{r, e}
	}()
	for range gates {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not run concurrently")
		}
	}
	for _, i := range []int{2, 1, 0} {
		close(gates[i])
		select {
		case got := <-finished:
			if got != i {
				t.Fatal(got)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.rows) != 3 {
			t.Fatal(got.err)
		}
		for i, tr := range got.rows {
			if tr[0].Side != i {
				t.Fatal("completion order changed game order")
			}
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestCollectionWindowFailureCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fail := errors.New("native worker failed")
	ready := make(chan struct{})
	var returned atomic.Int32
	rows, err := collectWindow(ctx, 2, func(ctx context.Context, i int) ([2]Trajectory, error) {
		defer returned.Add(1)
		if i == 0 {
			close(ready)
			<-ctx.Done()
			return [2]Trajectory{}, ctx.Err()
		}
		<-ready
		return [2]Trajectory{}, fail
	})
	if !errors.Is(err, fail) || rows != nil || returned.Load() != 2 {
		t.Fatal("failed window leaked samples or worker", err, returned.Load())
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = collectWindow(ctx, 2, func(context.Context, int) ([2]Trajectory, error) {
		t.Error("started after cancellation")
		return [2]Trajectory{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, n := range []int{0, MaxCollectionWorkers + 1} {
		if _, err := collectWindow(context.Background(), n, nil); err == nil {
			t.Fatal("invalid window accepted")
		}
	}
}

func TestNativeCollectionWorkersPreserveLearningAndResume(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native worker required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.Network.PlanFeatures = "target-counts-v1"
	c.Mode, c.MaxTurns, c.BatchMatches = 2, 4, 3
	c.Warmup.Matches, c.Warmup.Epochs = 8, 1
	c.Warmup.Teachers = []string{"control", "sustain"}
	c.PPO.Epochs, c.PPO.SequenceLength = 1, 2
	c.OpponentMix = &OpponentMix{History: 100}
	serial, parallel, resumed := t.TempDir(), t.TempDir(), t.TempDir()
	invoke := func(dir string, workers int, resume bool, progress func(Progress) error) error {
		opts := RunOptions{Directory: dir, Command: command, Workers: workers, Resume: resume, Batches: 2, Progress: progress, Stderr: io.Discard}
		if !resume {
			opts.Config = &c
		}
		return Run(ctx, opts)
	}
	if err := invoke(serial, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := invoke(parallel, 2, false, nil); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("declared interruption after committed match")
	if err := invoke(resumed, 2, false, func(p Progress) error {
		if p.CollectionWorkers != 2 {
			t.Fatal("worker count not observable")
		}
		if p.Event == "warmup_collected" && p.WarmupGames == 1 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := invoke(resumed, 1, true, func(p Progress) error {
		if p.Event == "collected" && p.Games == 1 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := invoke(resumed, 2, true, nil); err != nil {
		t.Fatal(err)
	}
	wantCP, want, err := LoadCheckpoint(serial)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{parallel, resumed} {
		cp, got, err := LoadCheckpoint(dir)
		if err != nil || !reflect.DeepEqual(want, got) {
			t.Fatal("worker count/order changed complete model and Adam state", dir, err)
		}
		if cp.NextGame != wantCP.NextGame || cp.CompletedBatches != wantCP.CompletedBatches || !reflect.DeepEqual(cp.Config, wantCP.Config) || !reflect.DeepEqual(cp.TrainingGroups, wantCP.TrainingGroups) || !reflect.DeepEqual(cp.Opponents, wantCP.Opponents) || !reflect.DeepEqual(cp.OpponentScores, wantCP.OpponentScores) {
			t.Fatal("parallel collection changed schedule/provenance")
		}
	}
	t.Log("serial, two-worker and 2->1->2 interrupted runs have identical full learning states; short cutoff fixtures, not strength")
}

// Child process supplies handshake only, never a simulated training result.
func TestCollectionWorkerHandshakeChild(t *testing.T) {
	path := os.Getenv("STONEAGE_COLLECTION_TEST_PID_FILE")
	if path == "" {
		t.Skip("helper process")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(f, os.Getpid())
	f.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		os.Exit(2)
	}
	rules := strings.Repeat("a", 64)
	if len(strings.Fields(string(raw))) == 3 {
		rules = strings.Repeat("b", 64)
	}
	fmt.Printf("{\"schema_version\":1,\"ok\":true,\"ready\":true,\"rules_digest\":\"%s\",\"platform\":\"linux-arm64\",\"scenario\":\"controlled-battle-v8\"}\n", rules)
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
func TestCollectionWorkerMetadataMismatchClosesAdditionalProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness uses Unix signal zero")
	}
	path := filepath.Join(t.TempDir(), "pids")
	t.Setenv("STONEAGE_COLLECTION_TEST_PID_FILE", path)
	command := []string{os.Args[0], "-test.run=^TestCollectionWorkerHandshakeChild$"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	pool, err := startCollectionWorkers(ctx, first, command, 3, io.Discard)
	if err == nil || pool != nil || !strings.Contains(err.Error(), "rules/platform/scenario differ") {
		t.Fatal("mixed engines accepted", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(raw))
	if len(pids) != 3 {
		t.Fatal(pids)
	}
	for i, text := range pids {
		pid, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		err = process.Signal(syscall.Signal(0))
		process.Release()
		if i == 0 && err != nil {
			t.Fatal("helper closed caller-owned first engine", err)
		}
		if i > 0 && err == nil {
			t.Fatal("mismatched worker leaked process", pid)
		}
	}
}

func TestCollectionWindowCancellationAfterSuccessfulCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := collectWindow(ctx, 1, func(context.Context, int) ([2]Trajectory, error) { cancel(); return [2]Trajectory{{Side: 1}}, nil })
	if rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled window published results", err)
	}
}
