package battletrain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

// Workers affect collection throughput, never game seeds, policy generations,
// optimizer order or serialized model configuration. Zero retains the old API.
const MaxCollectionWorkers = 8

type collectionJob struct {
	scenario battleenv.Scenario
	policies [2]Policy
	random   [2]*rand.Rand
	group    string
}
type collectionWorkers struct{ engines []*battleenv.Native }

type collectionLog struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *collectionLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func startCollectionWorkers(ctx context.Context, first *battleenv.Native, command []string, count int, stderr io.Writer) (*collectionWorkers, error) {
	p := &collectionWorkers{engines: []*battleenv.Native{first}}
	for len(p.engines) < count {
		engine, err := battleenv.Start(ctx, command, stderr)
		if err != nil {
			return nil, errors.Join(err, p.closeAdditional())
		}
		p.engines = append(p.engines, engine)
		if engine.Metadata() != first.Metadata() {
			return nil, errors.Join(fmt.Errorf("collection worker rules/platform/scenario differ"), p.closeAdditional())
		}
	}
	return p, nil
}
func (p *collectionWorkers) closeAdditional() error {
	// Drain independent writers together rather than multiplying the shutdown
	// timeout by the worker count. Preserve every failure in stable worker order.
	errs := make([]error, len(p.engines)-1)
	var wg sync.WaitGroup
	for i, engine := range p.engines[1:] {
		wg.Add(1)
		go func(i int, engine *battleenv.Native) {
			defer wg.Done()
			if err := engine.Close(); err != nil {
				errs[i] = fmt.Errorf("collection worker %d shutdown: %w", i+1, err)
			}
		}(i, engine)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// A window holds at most one match per engine. No later window starts before
// the caller has committed this window in game order. Failed windows publish
// no results; earlier committed matches remain resumable. Every goroutine has
// exited before returning, so optimization cannot race with policy inference.
func collectWindow(ctx context.Context, n int, collect func(context.Context, int) ([2]Trajectory, error)) ([][2]Trajectory, error) {
	if n < 1 || n > MaxCollectionWorkers {
		return nil, fmt.Errorf("invalid collection window")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n == 1 {
		tr, err := collect(ctx, 0)
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		return [][2]Trajectory{tr}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([][2]Trajectory, n)
	var wg sync.WaitGroup
	var once sync.Once
	var firstError error
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tr, err := collect(ctx, i)
			if err != nil {
				once.Do(func() { firstError = err; cancel() })
				return
			}
			results[i] = tr
		}(i)
	}
	wg.Wait()
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
func (p *collectionWorkers) collect(ctx context.Context, jobs []collectionJob) ([][2]Trajectory, error) {
	if len(jobs) > len(p.engines) {
		return nil, fmt.Errorf("collection window exceeds available workers")
	}
	return collectWindow(ctx, len(jobs), func(ctx context.Context, i int) ([2]Trajectory, error) {
		j := jobs[i]
		return CollectPolicies(ctx, p.engines[i], j.scenario, j.policies, j.random, j.group, p.engines[i].Metadata().Rules)
	})
}
