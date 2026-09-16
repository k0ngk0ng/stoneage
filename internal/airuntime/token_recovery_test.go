package airuntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenReservationHonorsActualUsageAfterDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "actual-usage.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	input := testProfile()
	input.Status = ProfileStatusActive
	input.DailyTokenBudget = 20
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, day, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{InputTokens: 18, OutputTokens: 2}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTokenAttempt(ctx, profile.ID, day, 1); !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("restart bypassed actual token budget: %v", err)
	}
}

func TestTokenAttemptRecoveryPreservesRequestAndSettlesOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attempts.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	input := testProfile()
	input.Status = ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, day, 12)
	if err != nil {
		t.Fatal(err)
	}
	metadata := TokenAttemptMetadata{Prompt: "continue the same confirmed game task", Resume: true, ThreadID: "thread-original"}
	if err := store.PrepareTokenAttempt(ctx, reservation, metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingTokenAttempt(ctx, profile.ID)
	if err != nil || pending.TokenReservation != reservation || pending.Prompt != metadata.Prompt || !pending.Resume || pending.ThreadID != metadata.ThreadID || pending.State != TokenAttemptDispatched {
		t.Fatalf("request changed across database reopen: %+v %v", pending, err)
	}
	for _, nextDay := range []time.Time{day, day.Add(24 * time.Hour)} {
		if _, err := store.BeginTokenAttempt(ctx, profile.ID, nextDay, 1); !errors.Is(err, ErrAttemptPending) {
			t.Fatalf("unsettled request allowed replacement reservation: %v", err)
		}
	}
	if err := store.MarkTokenAttemptUnknown(ctx, reservation, "transport interrupted before response"); err != nil {
		t.Fatal(err)
	}
	unknown, err := store.PendingTokenAttempt(ctx, profile.ID)
	if err != nil || unknown.State != TokenAttemptUnknown || unknown.ID != reservation.ID {
		t.Fatalf("unknown request lost its recovery identity: %+v %v", unknown, err)
	}
	outcome := TokenAttemptOutcome{ThreadID: metadata.ThreadID, TurnCompleted: true, Usage: TokenUsage{InputTokens: 2, OutputTokens: 3}}
	if err := store.RecordTokenAttemptResult(ctx, reservation, outcome, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTokenAttemptResult(ctx, reservation, outcome, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTokenAttemptResult(ctx, reservation, outcome, true); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("conflicting failure status accepted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{}, false); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("duplicate settlement: %v", err)
	}
	if _, err := store.PendingTokenAttempt(ctx, profile.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settled attempt is still pending: %v", err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, day)
	if err != nil || usage.ChargedTokens != 12 || usage.ReservedTokens != 0 || usage.TotalTokens != 5 || usage.Attempts != 1 {
		t.Fatalf("recovery charged more than one attempt: %+v %v", usage, err)
	}
	settled, err := store.GetTokenAttempt(ctx, reservation.ID)
	if err != nil || settled.Outcome == nil || settled.Outcome.ThreadID != metadata.ThreadID || settled.State != TokenAttemptSettled {
		t.Fatalf("settled result unavailable for checkpoint recovery: %+v %v", settled, err)
	}
}
