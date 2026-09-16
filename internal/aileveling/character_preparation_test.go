package aileveling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type characterPreparationStub struct {
	ready  bool
	err    error
	calls  int
	onCall func()
}

func (p *characterPreparationStub) PrepareCharacter(context.Context, aigame.Snapshot) (bool, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall()
	}
	return p.ready, p.err
}

func TestCharacterPreparationRunsBeforeCompletionAtStart(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, _, _ := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation

	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 1, MaximumSeconds: 30})
	if receipt.Status != aimcp.ReceiptRunning {
		t.Fatalf("start receipt=%+v, want running until preparation is checked", receipt)
	}
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Completed {
		t.Fatalf("prepared completion checkpoint=%+v err=%v", checkpoint, err)
	}
	if preparation.calls != 1 {
		t.Fatalf("preparation calls=%d, want 1", preparation.calls)
	}
}

func TestCharacterPreparationFalseStopsTickWithoutRefreshingDeadline(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}}}
	c, _, store := newCoordinator(t, game, navigator)
	preparation := &characterPreparationStub{}
	c.CharacterPreparation = preparation
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }

	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, NoProgressTimeout: time.Minute})
	initial, err := store.Load(context.Background(), receipt.Handle)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(1 * time.Second)
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Running {
		t.Fatalf("waiting preparation checkpoint=%+v err=%v", checkpoint, err)
	}
	if preparation.calls != 1 || len(game.actions) != 0 {
		t.Fatalf("preparation calls=%d actions=%+v", preparation.calls, game.actions)
	}
	if !checkpoint.StepStartedAt.Equal(initial.StepStartedAt) {
		t.Fatalf("false preparation refreshed deadline: before=%v after=%v", initial.StepStartedAt, checkpoint.StepStartedAt)
	}
}

func TestCharacterPreparationIsSkippedWhileMovementIsSubmitted(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}}}
	c, _, _ := newCoordinator(t, game, navigator)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 3, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("movement checkpoint=%+v err=%v", checkpoint, err)
	}
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" || preparation.calls != 0 {
		t.Fatalf("submitted movement was not fenced: checkpoint=%+v calls=%d err=%v", checkpoint, preparation.calls, err)
	}
}

func TestCharacterPreparationIsSkippedDuringBattle(t *testing.T) {
	game := &fakeGame{snapshot: battleSnapshot()}
	c, _, _ := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 3, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" || preparation.calls != 0 {
		t.Fatalf("battle preparation checkpoint=%+v calls=%d err=%v", checkpoint, preparation.calls, err)
	}
	if len(game.actions) != 1 || game.actions[0].Kind != aigame.ActionBattle {
		t.Fatalf("battle actions=%+v", game.actions)
	}
}

func TestCharacterPreparationErrorPausesWithoutAnotherAction(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	preparation := &characterPreparationStub{err: errors.New("uncertain allocation")}
	c.CharacterPreparation = preparation
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err == nil || checkpoint.Status != automation.Paused {
		t.Fatalf("preparation error checkpoint=%+v err=%v", checkpoint, err)
	}
	if preparation.calls != 1 || len(game.actions) != 0 {
		t.Fatalf("preparation error calls=%d actions=%+v", preparation.calls, game.actions)
	}
}

func TestResumeTargetReachedStillRunsCharacterPreparation(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, gate, store := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	checkpoint, err := store.Load(context.Background(), receipt.Handle)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Status = automation.Paused
	checkpoint.Reason = "test pause"
	checkpoint.Revision++
	if err := store.Save(context.Background(), checkpoint, checkpoint.Revision-1); err != nil {
		t.Fatal(err)
	}

	game.mu.Lock()
	game.snapshot.Player.Level = 2
	game.snapshot.Revision++
	game.mu.Unlock()
	resumed, err := c.Resume(context.Background(), receipt.Handle, gate.State().Generation)
	if err != nil || resumed.Status != aimcp.ReceiptRunning {
		t.Fatalf("resume receipt=%+v err=%v", resumed, err)
	}
	if preparation.calls != 0 {
		t.Fatalf("Resume invoked preparation calls=%d, want Tick only", preparation.calls)
	}
	completed, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || completed.Status != automation.Completed || preparation.calls != 1 {
		t.Fatalf("post-resume preparation checkpoint=%+v calls=%d err=%v", completed, preparation.calls, err)
	}
}

func TestTargetReachedBattleWaitsForEOAndLeavingBattleBeforePreparation(t *testing.T) {
	snapshot := battleSnapshot()
	snapshot.Player.Level = 2
	game := &fakeGame{snapshot: snapshot}
	c, _, _ := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})

	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Running || preparation.calls != 0 || len(game.actions) != 0 {
		t.Fatalf("active battle target checkpoint=%+v calls=%d actions=%+v err=%v", checkpoint, preparation.calls, game.actions, err)
	}
	game.mu.Lock()
	game.snapshot.Battle.Result = "win"
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" || preparation.calls != 0 || len(game.actions) != 1 || game.actions[0].Kind != aigame.ActionBattleEnd {
		t.Fatalf("EO submission checkpoint=%+v calls=%d actions=%+v err=%v", checkpoint, preparation.calls, game.actions, err)
	}

	// A battle-phase snapshot with Active=false is still a transition. EO has
	// not been observed leaving the battle until the world phase arrives.
	game.mu.Lock()
	game.snapshot.Battle.Active = false
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Running || preparation.calls != 0 {
		t.Fatalf("battle transition checkpoint=%+v calls=%d err=%v", checkpoint, preparation.calls, err)
	}

	game.mu.Lock()
	game.snapshot.Phase = aigame.PhaseWorld
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Completed || preparation.calls != 1 {
		t.Fatalf("post-EO preparation checkpoint=%+v calls=%d err=%v", checkpoint, preparation.calls, err)
	}
}

func TestCharacterPreparationRunsAfterTimeBudgetChecks(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 1})
	now = now.Add(2 * time.Second)
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || preparation.calls != 0 || len(game.actions) != 0 {
		t.Fatalf("expired budget checkpoint=%+v calls=%d actions=%+v err=%v", checkpoint, preparation.calls, game.actions, err)
	}
}

func TestCharacterPreparationRunsAfterNoProgressBudgetChecks(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, _, _ := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	c.CharacterPreparation = preparation
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 1, MaximumSeconds: 30, NoProgressTimeout: time.Second})
	now = now.Add(2 * time.Second)
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || preparation.calls != 0 || len(game.actions) != 0 {
		t.Fatalf("expired progress budget checkpoint=%+v calls=%d actions=%+v err=%v", checkpoint, preparation.calls, game.actions, err)
	}
}

func TestCharacterPreparationTrueIsRecheckedAfterCallback(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, gate, _ := newCoordinator(t, game, nil)
	preparation := &characterPreparationStub{ready: true}
	preparation.onCall = func() {
		if _, err := gate.Takeover("manual takeover during preparation"); err != nil {
			t.Errorf("takeover: %v", err)
		}
	}
	c.CharacterPreparation = preparation
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 1, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err == nil || checkpoint.Status != automation.Paused || preparation.calls != 1 {
		t.Fatalf("late preparation result checkpoint=%+v calls=%d err=%v", checkpoint, preparation.calls, err)
	}
	if checkpoint.Status == automation.Completed || len(game.actions) != 0 {
		t.Fatalf("late preparation completed or acted: checkpoint=%+v actions=%+v", checkpoint, game.actions)
	}
}

func TestCharacterPreparationProgressStampIgnoresKnownOnlyChanges(t *testing.T) {
	snapshot := worldSnapshot()
	snapshot.Player.UnspentStatPoints = 2
	before := makeProgressStamp(snapshot)
	snapshot.Player.StatPointsKnown = true
	if got := makeProgressStamp(snapshot); got != before {
		t.Fatalf("stat known transition changed progress stamp: before=%+v after=%+v", before, got)
	}
	snapshot.Player.Strength++
	if got := makeProgressStamp(snapshot); got == before {
		t.Fatal("authoritative base attribute change did not update progress stamp")
	}
}
