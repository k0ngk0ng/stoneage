package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUnknownReviewPreservesOutcomeAndBudgetAtomically(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "review.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	input := testProfile()
	input.Status = ProfileStatusActive
	input.DailyTokenBudget = 100
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, day, 17)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PrepareTokenAttempt(ctx, reservation, TokenAttemptMetadata{Prompt: "observe game first"}); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkTokenAttemptUnknown(ctx, reservation, "missing response"); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetTokenAttempt(ctx, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pending_attempt_id": attempt.ID, "thread_id": "old-thread"})
	cp, err := store.SaveCheckpointCAS(ctx, profile.ID, 0, raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := ReviewUnknownAttemptRequest{ProfileID: profile.ID, AttemptID: attempt.ID, ExpectedUpdatedAt: attempt.UpdatedAt, ExpectedProfileVersion: profile.Version, ExpectedCheckpointVersion: cp.Version, CheckpointState: json.RawMessage(`{"model_turn_done":false}`), Actor: "operator", Reason: UnknownReviewReason}
	if err = store.ReviewUnknownAttempt(ctx, request); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("active review=%v", err)
	}
	paused := ProfileStatusPaused
	profile, err = store.UpdateProfileCAS(ctx, profile.ID, profile.Version, ProfilePatch{Status: &paused, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedProfileVersion = profile.Version
	stale := request
	stale.ExpectedCheckpointVersion++
	if err = store.ReviewUnknownAttempt(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale checkpoint=%v", err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, day)
	if err != nil || usage.ReservedTokens != 17 || usage.ChargedTokens != 0 {
		t.Fatalf("failed review changed budget: %+v %v", usage, err)
	}
	if _, err = store.GetUnknownAttemptReview(ctx, profile.ID, attempt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial audit=%v", err)
	}
	if err = store.ReviewUnknownAttempt(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReviewUnknownAttempt(ctx, request); err != nil {
		t.Fatalf("retry=%v", err)
	}
	usage, err = store.TokenUsage(ctx, profile.ID, day)
	if err != nil || usage.ReservedTokens != 0 || usage.ChargedTokens != 17 || usage.TotalTokens != 0 || usage.FailedAttempts != 1 {
		t.Fatalf("review usage=%+v err=%v", usage, err)
	}
	reviewed, err := store.GetTokenAttempt(ctx, attempt.ID)
	if err != nil || reviewed.State != TokenAttemptUnknown || reviewed.Outcome != nil || reviewed.SettledAt.IsZero() || reviewed.Prompt != attempt.Prompt {
		t.Fatalf("review fabricated result: %+v %v", reviewed, err)
	}
	if _, err = store.PendingTokenAttempt(ctx, profile.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pending=%v", err)
	}
	cp, err = store.GetCheckpoint(ctx, profile.ID)
	if err != nil || cp.Version != request.ExpectedCheckpointVersion+1 || string(cp.State) != string(request.CheckpointState) {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	if err = store.RecordTokenAttemptResult(ctx, reservation, TokenAttemptOutcome{TurnCompleted: true}, false); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("late result=%v", err)
	}
	changed := request
	changed.Actor = "another-operator"
	if err = store.ReviewUnknownAttempt(ctx, changed); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("different review=%v", err)
	}
}
