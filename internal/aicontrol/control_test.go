package aicontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTakeoverCancelsBlockedWriteBeforeWaitingForLock(t *testing.T) {
	g := New()
	s, _, err := g.Switch(1, Agent, "start")
	if err != nil {
		t.Fatal(err)
	}
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- g.Dispatch(context.Background(), s.Generation, Agent, func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	<-started
	takeover := make(chan error, 1)
	go func() { _, err := g.Takeover("manual"); takeover <- err }()
	select {
	case err := <-takeover:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("takeover deadlocked behind cancelled write")
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("write not cancelled: %v", err)
	}
}

func TestTakeoverFencesDelayedAIAndCancelsPlan(t *testing.T) {
	g := New()
	s, plan, err := g.Switch(1, Quest, "start")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := g.Takeover("player takeover")
	if err != nil || manual.Mode != Manual {
		t.Fatal(manual, err)
	}
	if !errors.Is(plan.Err(), context.Canceled) {
		t.Fatal("old plan remains live")
	}
	writes := 0
	write := func(context.Context) error { writes++; return nil }
	if err := g.Dispatch(context.Background(), s.Generation, Quest, write); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := g.Dispatch(context.Background(), manual.Generation, Quest, write); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	if err := g.Dispatch(context.Background(), manual.Generation, Manual, write); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal(writes)
	}
}

func TestConcurrentStartsHaveSingleWinner(t *testing.T) {
	g := New()
	var wg sync.WaitGroup
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := g.Switch(1, Leveling, "start"); results <- err }()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d control owners", won)
	}
}

func TestPauseManualAndClose(t *testing.T) {
	g := New()
	state, _, _ := g.Switch(1, Paused, "budget")
	if err := g.Dispatch(context.Background(), state.Generation, Manual, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	g.Close()
	if _, err := g.Takeover("resume"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := g.Dispatch(context.Background(), state.Generation, Manual, func(context.Context) error { t.Fatal("closed action"); return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestTakeoverIsOrderedAfterInFlightWrite(t *testing.T) {
	g := New()
	state, _, _ := g.Switch(1, Agent, "")
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = g.Dispatch(context.Background(), state.Generation, Agent, func(context.Context) error { close(started); <-finish; return nil })
		close(done)
	}()
	<-started
	taken := make(chan State, 1)
	go func() { s, _ := g.Takeover("human"); taken <- s }()
	close(finish)
	<-done
	s := <-taken
	if s.Mode != Manual {
		t.Fatal(s)
	}
	if err := g.Dispatch(context.Background(), state.Generation, Agent, func(context.Context) error { t.Fatal("late action"); return nil }); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
