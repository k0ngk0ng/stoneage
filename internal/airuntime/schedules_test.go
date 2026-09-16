package airuntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSchedulesAreProfileScopedAndIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	first := testProfile()
	first.ID = "schedule-profile-one"
	second := testProfile()
	second.ID = "schedule-profile-two"
	if _, err := store.CreateProfile(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProfile(ctx, second); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	input := ScheduleInput{Kind: "reminder", Title: "check mail", Prompt: "Read new mail and decide whether to reply.", RunAt: now.Add(time.Minute), IdempotencyKey: "mail-check"}
	created, err := store.CreateSchedule(ctx, first.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != SchedulePending || created.ProfileID != first.ID || created.ClaimToken != "" {
		t.Fatalf("created schedule = %#v", created)
	}
	repeated, err := store.CreateSchedule(ctx, first.ID, input)
	if err != nil || repeated.ID != created.ID {
		t.Fatalf("idempotent create = %#v, %v", repeated, err)
	}
	input.Prompt = "different prompt"
	if _, err := store.CreateSchedule(ctx, first.ID, input); !errors.Is(err, ErrScheduleConflict) {
		t.Fatalf("changed idempotent create = %v", err)
	}
	other, err := store.CreateSchedule(ctx, second.ID, ScheduleInput{
		Kind: "reminder", Prompt: "same key in another profile", RunAt: now.Add(time.Minute), IdempotencyKey: "mail-check",
	})
	if err != nil || other.ID == created.ID || other.ProfileID != second.ID {
		t.Fatalf("cross-profile key = %#v, %v", other, err)
	}
	if _, err := store.GetSchedule(ctx, second.ID, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile read = %v", err)
	}
	if due, err := store.ClaimDueSchedules(ctx, first.ID, now); err != nil || len(due) != 0 {
		t.Fatalf("early claim = %#v, %v", due, err)
	}
	now = now.Add(time.Minute)
	due, err := store.ClaimDueSchedules(ctx, first.ID, now)
	if err != nil || len(due) != 1 || due[0].ID != created.ID || due[0].Status != ScheduleDelivering || due[0].ClaimToken == "" {
		t.Fatalf("due claim = %#v, %v", due, err)
	}
	claim := due[0].ClaimToken
	if again, err := store.ClaimDueSchedules(ctx, first.ID, now); err != nil || len(again) != 0 {
		t.Fatalf("duplicate claim before lease = %#v, %v", again, err)
	}
	if _, err := store.CompleteSchedule(ctx, second.ID, created.ID, claim, map[string]any{"ok": true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile complete = %v", err)
	}
	if _, err := store.CompleteSchedule(ctx, first.ID, created.ID, "wrong", map[string]any{"ok": true}); !errors.Is(err, ErrScheduleConflict) {
		t.Fatalf("wrong claim complete = %v", err)
	}
	completed, err := store.CompleteSchedule(ctx, first.ID, created.ID, claim, map[string]any{"ok": true})
	if err != nil || completed.Status != ScheduleDelivered || completed.Occurrences != 1 {
		t.Fatalf("complete = %#v, %v", completed, err)
	}
	duplicate, err := store.CompleteSchedule(ctx, first.ID, created.ID, claim, map[string]any{"changed": true})
	if err != nil || duplicate.Status != ScheduleDelivered || duplicate.Occurrences != 1 {
		t.Fatalf("duplicate complete = %#v, %v", duplicate, err)
	}
	listed, err := store.ListSchedules(ctx, first.ID)
	if err != nil || len(listed) != 1 || listed[0].Status != ScheduleDelivered {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	if events, err := store.ListEvents(ctx, first.ID, 20); err != nil || len(events) != 4 {
		t.Fatalf("schedule audit events = %#v, %v", events, err)
	}
}

func TestRepeatingScheduleAndExpiredClaimRecoverAfterReopen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile := testProfile()
	profile.ID = "schedule-recovery"
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	schedule, err := store.CreateSchedule(ctx, profile.ID, ScheduleInput{
		Kind: "decision", Prompt: "Review the player's next activity.", RunAt: now.Add(time.Minute), RepeatInterval: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	claimed, err := store.ClaimDueSchedules(ctx, profile.ID, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("initial claim = %#v, %v", claimed, err)
	}
	claim := claimed[0].ClaimToken
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now.Add(ScheduleClaimLease() + time.Second) }
	reclaimed, err := reopened.ClaimDueSchedules(ctx, profile.ID, reopened.now())
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != schedule.ID || reclaimed[0].ClaimToken == claim {
		t.Fatalf("expired claim recovery = %#v, %v", reclaimed, err)
	}
	completed, err := reopened.CompleteSchedule(ctx, profile.ID, schedule.ID, reclaimed[0].ClaimToken, ScheduleCompletion{Outcome: []byte(`{"delivered":true}`)})
	if err != nil || completed.Status != SchedulePending || completed.Occurrences != 1 || !completed.RunAt.After(reopened.now()) {
		t.Fatalf("repeat completion = %#v, %v", completed, err)
	}
	if _, err := reopened.CancelSchedule(ctx, profile.ID, schedule.ID, "no longer needed"); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := reopened.CancelSchedule(ctx, profile.ID, schedule.ID, "retry"); err != nil || cancelled.Status != ScheduleCancelled {
		t.Fatalf("idempotent cancel = %#v, %v", cancelled, err)
	}
}

func TestScheduleInputRejectsUnsafeOrUnboundedValues(t *testing.T) {
	store := testStore(t)
	profile := testProfile()
	profile.ID = "schedule-validation"
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 14, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for name, input := range map[string]ScheduleInput{
		"missing prompt": {RunAt: now.Add(time.Minute)},
		"past":           {Prompt: "x", RunAt: now.Add(-time.Minute)},
		"short repeat":   {Prompt: "x", RunAt: now.Add(time.Minute), RepeatInterval: time.Second},
		"unsafe kind":    {Kind: "run command", Prompt: "x", RunAt: now.Add(time.Minute)},
		"long horizon":   {Prompt: "x", RunAt: now.Add(366 * 24 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateSchedule(context.Background(), profile.ID, input); !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFinishTokenAttemptAcknowledgesSchedulesAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profile := testProfile()
	profile.ID = "schedule-token-boundary"
	profile.Status = ProfileStatusActive
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	schedule, err := store.CreateSchedule(ctx, profile.ID, ScheduleInput{Kind: "reminder", Prompt: "Review plans.", RunAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, now, 5)
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.ClaimDueScheduledTasks(ctx, profile.ID, reservation.ID, now.Add(time.Minute), 8)
	if err != nil || len(due) != 1 || due[0].ID != schedule.ID || due[0].DeliveryAttemptID != reservation.ID {
		t.Fatalf("attempt-bound claim = %#v, %v", due, err)
	}
	if err := store.PrepareTokenAttempt(ctx, reservation, TokenAttemptMetadata{Prompt: "scheduled prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTokenAttemptResult(ctx, reservation, TokenAttemptOutcome{TurnCompleted: true, Usage: TokenUsage{InputTokens: 2, OutputTokens: 1}}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(ctx, reservation, TokenUsage{}, false); err != nil {
		t.Fatal(err)
	}
	settled, err := store.GetSchedule(ctx, profile.ID, schedule.ID)
	if err != nil || settled.Status != ScheduleDelivered || settled.Occurrences != 1 {
		t.Fatalf("schedule after token settlement = %#v, %v", settled, err)
	}
	if _, err := store.ClaimDueSchedules(ctx, profile.ID, now.Add(ScheduleClaimLease()+time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The settled schedule cannot be re-delivered, even after its old lease
	// would have expired.
	if due, err := store.ClaimDueSchedules(ctx, profile.ID, now.Add(10*time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("settled schedule was re-delivered = %#v, %v", due, err)
	}
}

func TestUnknownTokenAttemptDoesNotReclaimScheduleLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profile := testProfile()
	profile.ID = "schedule-unknown-boundary"
	profile.Status = ProfileStatusActive
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 16, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateSchedule(ctx, profile.ID, ScheduleInput{Kind: "reminder", Prompt: "Do not duplicate.", RunAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, now, 5)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := store.ClaimDueScheduledTasks(ctx, profile.ID, reservation.ID, now.Add(time.Minute), 8); err != nil || len(due) != 1 {
		t.Fatalf("claim = %#v, %v", due, err)
	}
	if err := store.MarkTokenAttemptUnknown(ctx, reservation, "provider outcome is unknown"); err != nil {
		t.Fatal(err)
	}
	if due, err := store.ClaimDueSchedules(ctx, profile.ID, now.Add(time.Minute+ScheduleClaimLease()+time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("unknown schedule was re-claimed = %#v, %v", due, err)
	}
}
