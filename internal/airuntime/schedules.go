package airuntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ScheduleStatus is the durable lifecycle of an AI player's self-created
// wake-up. A schedule is never an instruction packet: it is a bounded piece
// of context which the supervisor may use to start a later model decision.
type ScheduleStatus string

const (
	SchedulePending    ScheduleStatus = "pending"
	ScheduleDelivering ScheduleStatus = "delivering"
	ScheduleDelivered  ScheduleStatus = "delivered"
	ScheduleCancelled  ScheduleStatus = "cancelled"
	// These aliases retain the intuitive names used by early adapters while
	// making the delivery semantics explicit: completion acknowledges that the
	// due prompt was handed to Codex, not that a game task has finished.
	ScheduleClaimed   = ScheduleDelivering
	ScheduleCompleted = ScheduleDelivered
)

const (
	maxScheduleIDBytes             = 128
	maxScheduleKindBytes           = 64
	maxScheduleTitleBytes          = 160
	maxSchedulePromptBytes         = 8 * 1024
	maxScheduleIdempotencyKeyBytes = 128
	maxScheduleLimit               = 100
	maxScheduleClaimLimit          = 32
	maxScheduleHorizon             = 365 * 24 * time.Hour
	minScheduleRepeat              = time.Minute
	maxScheduleRepeat              = 365 * 24 * time.Hour
	// A claim is a lease rather than a permanent lock. If the process exits
	// after claiming and before completion, the next supervisor can reclaim it
	// after this interval.
	scheduleClaimLease = 2 * time.Minute
)

// ScheduleInput is the model-facing subset needed to create a persistent
// wake-up. RunAt is preferred. DelaySeconds is accepted for clients that do
// not have a trusted wall clock and is resolved by Store at commit time.
// DueAt, RepeatSeconds and Label are compatibility aliases for callers that
// use the older naming in their own adapters; the normalized values are
// persisted in the same columns.
type ScheduleInput struct {
	ID             string        `json:"id,omitempty"`
	Kind           string        `json:"kind"`
	Title          string        `json:"title,omitempty"`
	Label          string        `json:"label,omitempty"`
	Prompt         string        `json:"prompt"`
	RunAt          time.Time     `json:"run_at,omitempty"`
	DueAt          time.Time     `json:"due_at,omitempty"`
	DelaySeconds   int64         `json:"delay_seconds,omitempty"`
	RepeatInterval time.Duration `json:"repeat_interval,omitempty"`
	RepeatSeconds  int64         `json:"repeat_seconds,omitempty"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	Actor          string        `json:"-"`
}

// Schedule is a durable timer scoped to exactly one AI profile. ClaimToken
// is returned to the trusted runtime for completion but is intentionally
// omitted from JSON responses so an MCP model cannot forge a completion.
type Schedule struct {
	ID                string          `json:"id"`
	ProfileID         string          `json:"profile_id"`
	Kind              string          `json:"kind"`
	Title             string          `json:"title,omitempty"`
	Prompt            string          `json:"prompt"`
	RunAt             time.Time       `json:"run_at"`
	DueAt             time.Time       `json:"due_at"`
	RepeatInterval    time.Duration   `json:"repeat_interval,omitempty"`
	Status            ScheduleStatus  `json:"status"`
	ClaimToken        string          `json:"-"`
	ClaimUntil        time.Time       `json:"-"`
	DeliveryAttemptID string          `json:"-"`
	IdempotencyKey    string          `json:"idempotency_key,omitempty"`
	Occurrences       int             `json:"occurrences"`
	LastOutcome       json.RawMessage `json:"last_outcome,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CompletedAt       time.Time       `json:"completed_at,omitempty"`
	CancelledAt       time.Time       `json:"cancelled_at,omitempty"`
}

// ScheduleCompletion is useful to callers that want a named result type;
// CompleteSchedule also accepts any JSON-marshalable value for convenience.
type ScheduleCompletion struct {
	Outcome json.RawMessage `json:"outcome,omitempty"`
}

// ScheduleClaimLease is exported for supervisor tests and adapters which
// need to display the bounded retry window without duplicating the value.
func ScheduleClaimLease() time.Duration { return scheduleClaimLease }

func (input ScheduleInput) normalized(now time.Time) (ScheduleInput, error) {
	now = now.UTC()
	input.ID = strings.TrimSpace(input.ID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		input.Title = strings.TrimSpace(input.Label)
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Kind == "" {
		input.Kind = "reminder"
	}
	if input.Title == "" {
		input.Title = input.Kind
	}
	if input.RunAt.IsZero() {
		input.RunAt = input.DueAt
	}
	if input.RunAt.IsZero() {
		if input.DelaySeconds <= 0 {
			return ScheduleInput{}, fmt.Errorf("%w: run_at or positive delay_seconds is required", ErrInvalidSchedule)
		}
		if input.DelaySeconds > int64(maxScheduleHorizon/time.Second) {
			return ScheduleInput{}, fmt.Errorf("%w: delay is too far in the future", ErrInvalidSchedule)
		}
		input.RunAt = now.Add(time.Duration(input.DelaySeconds) * time.Second)
	}
	input.RunAt = input.RunAt.UTC()
	if input.RepeatInterval == 0 && input.RepeatSeconds != 0 {
		if input.RepeatSeconds < 0 || input.RepeatSeconds > int64(maxScheduleRepeat/time.Second) {
			return ScheduleInput{}, fmt.Errorf("%w: repeat_seconds must be non-negative", ErrInvalidSchedule)
		}
		input.RepeatInterval = time.Duration(input.RepeatSeconds) * time.Second
	}
	if input.ID != "" && !validScheduleToken(input.ID, maxScheduleIDBytes) {
		return ScheduleInput{}, fmt.Errorf("%w: invalid id", ErrInvalidSchedule)
	}
	if !validScheduleToken(input.Kind, maxScheduleKindBytes) {
		return ScheduleInput{}, fmt.Errorf("%w: invalid kind", ErrInvalidSchedule)
	}
	if len([]byte(input.Title)) > maxScheduleTitleBytes || strings.ContainsAny(input.Title, "\x00\r\n") {
		return ScheduleInput{}, fmt.Errorf("%w: title is invalid", ErrInvalidSchedule)
	}
	if len([]byte(input.Prompt)) == 0 || len([]byte(input.Prompt)) > maxSchedulePromptBytes || strings.ContainsRune(input.Prompt, '\x00') {
		return ScheduleInput{}, fmt.Errorf("%w: prompt is invalid", ErrInvalidSchedule)
	}
	if len([]byte(input.IdempotencyKey)) > maxScheduleIdempotencyKeyBytes || strings.ContainsAny(input.IdempotencyKey, "\x00\r\n") {
		return ScheduleInput{}, fmt.Errorf("%w: idempotency key is invalid", ErrInvalidSchedule)
	}
	if !input.RunAt.After(now) {
		return ScheduleInput{}, fmt.Errorf("%w: run_at must be in the future", ErrInvalidSchedule)
	}
	if input.RunAt.Sub(now) > maxScheduleHorizon {
		return ScheduleInput{}, fmt.Errorf("%w: run_at is too far in the future", ErrInvalidSchedule)
	}
	if input.RepeatInterval < 0 || (input.RepeatInterval > 0 && input.RepeatInterval < minScheduleRepeat) {
		return ScheduleInput{}, fmt.Errorf("%w: repeat interval is too short", ErrInvalidSchedule)
	}
	if input.RepeatInterval > maxScheduleRepeat {
		return ScheduleInput{}, fmt.Errorf("%w: repeat interval is too long", ErrInvalidSchedule)
	}
	return input, nil
}

func validScheduleToken(value string, maximum int) bool {
	if value == "" || len([]byte(value)) > maximum {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validateScheduleProfileID(profileID string) error {
	if strings.TrimSpace(profileID) == "" || len([]byte(profileID)) > maxScheduleIDBytes || strings.ContainsAny(profileID, "\x00\r\n") {
		return fmt.Errorf("%w: profile id is required", ErrInvalidSchedule)
	}
	return nil
}

func validateScheduleID(id string) error {
	if !validScheduleToken(strings.TrimSpace(id), maxScheduleIDBytes) {
		return fmt.Errorf("%w: schedule id is invalid", ErrInvalidSchedule)
	}
	return nil
}

// CreateSchedule writes one schedule and its audit event atomically. A
// profile-scoped idempotency key returns the existing schedule if all request
// fields match; a different request with the same key is a conflict.
func (store *Store) CreateSchedule(ctx context.Context, profileID string, input ScheduleInput) (Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return Schedule{}, err
	}
	now := store.now().UTC()
	normalized, err := input.normalized(now)
	if err != nil {
		return Schedule{}, err
	}
	idProvided := normalized.ID != ""
	if normalized.ID == "" {
		normalized.ID = newID("schedule")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, fmt.Errorf("begin schedule create: %w", err)
	}
	defer tx.Rollback()
	var profileStatus string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM ai_profiles WHERE id=?", profileID).Scan(&profileStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, fmt.Errorf("read schedule profile: %w", err)
	}
	if profileStatus == ProfileStatusDeleted {
		return Schedule{}, ErrNotFound
	}
	if normalized.IdempotencyKey != "" {
		existing, err := scanScheduleQuery(ctx, tx.QueryRowContext(ctx, `SELECT id, profile_id, kind, title,
			prompt, run_at, repeat_interval_ns, status, claim_token, claim_until, delivery_attempt_id, idempotency_key,
			occurrences, last_outcome_json, created_at, updated_at, completed_at, cancelled_at
			FROM ai_schedules WHERE profile_id=? AND idempotency_key=?`, profileID, normalized.IdempotencyKey))
		if err == nil {
			if scheduleInputMatches(existing, normalized, idProvided) {
				return existing, nil
			}
			return Schedule{}, ErrScheduleConflict
		}
		if !errors.Is(err, ErrNotFound) {
			return Schedule{}, err
		}
	}
	var existingID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM ai_schedules WHERE id=?", normalized.ID).Scan(&existingID); err == nil {
		return Schedule{}, ErrScheduleConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, err
	}
	created := store.timestamp(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_schedules
		(id, profile_id, kind, title, prompt, run_at, repeat_interval_ns, status,
		 claim_token, claim_until, idempotency_key, occurrences, last_outcome_json,
		 created_at, updated_at, completed_at, cancelled_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, 0, '', ?, ?, '', '')`,
		normalized.ID, profileID, normalized.Kind, normalized.Title, normalized.Prompt,
		store.timestamp(normalized.RunAt), normalized.RepeatInterval.Nanoseconds(), string(SchedulePending),
		normalized.IdempotencyKey, created, created)
	if err != nil {
		return Schedule{}, fmt.Errorf("create AI schedule: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{
		"schedule_id": normalized.ID, "kind": normalized.Kind, "run_at": store.timestamp(normalized.RunAt),
		"repeat_interval_ns": normalized.RepeatInterval.Nanoseconds(),
	})
	if _, err := store.recordEventTx(ctx, tx, profileID, EventScheduleCreated, "runtime", detail); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, fmt.Errorf("commit AI schedule create: %w", err)
	}
	return Schedule{
		ID: normalized.ID, ProfileID: profileID, Kind: normalized.Kind, Title: normalized.Title,
		Prompt: normalized.Prompt, RunAt: normalized.RunAt, DueAt: normalized.RunAt,
		RepeatInterval: normalized.RepeatInterval, Status: SchedulePending,
		IdempotencyKey: normalized.IdempotencyKey, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// GetSchedule reads one schedule only within the supplied profile. A caller
// cannot use this method to probe another profile's timers.
func (store *Store) GetSchedule(ctx context.Context, profileID, scheduleID string) (Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return Schedule{}, err
	}
	if err := validateScheduleID(scheduleID); err != nil {
		return Schedule{}, err
	}
	return scanScheduleQuery(ctx, store.db.QueryRowContext(ctx, scheduleSelect+" WHERE profile_id=? AND id=?", profileID, scheduleID))
}

// ListSchedules returns the most imminent schedules first. A zero or omitted
// limit uses the safe default; the result never crosses profile boundaries.
func (store *Store) ListSchedules(ctx context.Context, profileID string, limits ...int) ([]Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return nil, err
	}
	limit := maxScheduleLimit
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := store.db.QueryContext(ctx, scheduleSelect+" WHERE profile_id=? ORDER BY CASE status WHEN 'delivering' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, run_at, id LIMIT ?", profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list AI schedules: %w", err)
	}
	defer rows.Close()
	result := make([]Schedule, 0)
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AI schedules: %w", err)
	}
	return result, nil
}

// ClaimDueSchedules atomically leases due pending schedules and expired
// claims. The returned ClaimToken is required by CompleteSchedule.
func (store *Store) ClaimDueSchedules(ctx context.Context, profileID string, at time.Time, limits ...int) ([]Schedule, error) {
	return store.claimDueSchedules(ctx, profileID, "", at, limits...)
}

// ClaimDueScheduledTasks associates each delivery lease with the durable
// token-attempt ID which will carry its prompt to Codex. This lets a
// supervisor reconcile a crash by looking at both stores and prevents an
// unknown model turn from being silently treated as a fresh delivery.
func (store *Store) ClaimDueScheduledTasks(ctx context.Context, profileID, attemptID string, at time.Time, limit int) ([]Schedule, error) {
	attemptID = strings.TrimSpace(attemptID)
	if !validScheduleToken(attemptID, maxScheduleIDBytes) {
		return nil, fmt.Errorf("%w: delivery attempt id is required", ErrInvalidSchedule)
	}
	return store.claimDueSchedules(ctx, profileID, attemptID, at, limit)
}

func (store *Store) claimDueSchedules(ctx context.Context, profileID, attemptID string, at time.Time, limits ...int) ([]Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = store.now()
	}
	at = at.UTC()
	limit := maxScheduleClaimLimit
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	if limit > maxScheduleClaimLimit {
		limit = maxScheduleClaimLimit
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin schedule claim: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, scheduleSelect+` WHERE profile_id=? AND run_at<=? AND (
		status=? OR (
			status=? AND claim_until<>'' AND claim_until<=?
			AND NOT EXISTS (
				SELECT 1 FROM ai_token_attempts a
				WHERE a.id=ai_schedules.delivery_attempt_id
					AND (a.settled=0 OR a.failed=1)
			)
		)
	)
	ORDER BY run_at, id LIMIT ?`, profileID, store.timestamp(at), string(SchedulePending), string(ScheduleDelivering), store.timestamp(at), limit)
	if err != nil {
		return nil, fmt.Errorf("find due AI schedules: %w", err)
	}
	schedules := make([]Schedule, 0)
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		claim := newID("claim")
		until := at.Add(scheduleClaimLease)
		result, err := tx.ExecContext(ctx, `UPDATE ai_schedules SET status=?, claim_token=?, claim_until=?, delivery_attempt_id=?, updated_at=?
			WHERE profile_id=? AND id=? AND (
				status=? OR (
					status=? AND claim_until<>'' AND claim_until<=?
					AND NOT EXISTS (
						SELECT 1 FROM ai_token_attempts a
						WHERE a.id=ai_schedules.delivery_attempt_id
							AND (a.settled=0 OR a.failed=1)
					)
				)
			)`,
			string(ScheduleClaimed), claim, store.timestamp(until), attemptID, store.timestamp(at), profileID, schedule.ID,
			string(SchedulePending), string(ScheduleClaimed), store.timestamp(at))
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim AI schedule: %w", err)
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			continue
		}
		schedule.Status = ScheduleClaimed
		schedule.ClaimToken = claim
		schedule.ClaimUntil = until
		schedule.DeliveryAttemptID = attemptID
		schedule.UpdatedAt = at
		detail, _ := json.Marshal(map[string]any{"schedule_id": schedule.ID, "run_at": store.timestamp(schedule.RunAt)})
		if _, err := store.recordEventTx(ctx, tx, profileID, EventScheduleClaimed, "runtime", detail); err != nil {
			rows.Close()
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate due AI schedules: %w", err)
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit AI schedule claim: %w", err)
	}
	return schedules, nil
}

// NextScheduleAt returns the next pending wake-up for a profile. It is a
// read-only helper for a supervisor's existing heartbeat loop.
func (store *Store) NextScheduleAt(ctx context.Context, profileID string) (time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return time.Time{}, err
	}
	var value string
	err := store.db.QueryRowContext(ctx, `SELECT run_at FROM ai_schedules
		WHERE profile_id=? AND status=? ORDER BY run_at, id LIMIT 1`, profileID, string(SchedulePending)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read next AI schedule: %w", err)
	}
	return parseTimestamp(value)
}

// CompleteSchedule applies one claimed wake-up. Repeating schedules are
// returned to pending with their next occurrence. Repeating completion is
// anchored to the previous due time and skips missed intervals, so a long
// outage cannot create a burst of duplicate model turns.
func (store *Store) CompleteSchedule(ctx context.Context, profileID, scheduleID, claimToken string, outcome any) (Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return Schedule{}, err
	}
	if err := validateScheduleID(scheduleID); err != nil {
		return Schedule{}, err
	}
	claimToken = strings.TrimSpace(claimToken)
	if !validScheduleToken(claimToken, maxScheduleIDBytes) {
		return Schedule{}, fmt.Errorf("%w: claim token is required", ErrInvalidSchedule)
	}
	outcomeJSON, err := scheduleOutcomeJSON(outcome)
	if err != nil {
		return Schedule{}, err
	}
	now := store.now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, fmt.Errorf("begin schedule completion: %w", err)
	}
	defer tx.Rollback()
	schedule, err := scanScheduleQuery(ctx, tx.QueryRowContext(ctx, scheduleSelect+" WHERE profile_id=? AND id=?", profileID, scheduleID))
	if err != nil {
		return Schedule{}, err
	}
	if schedule.LastOutcome != nil && scheduleLastClaimToken(ctx, tx, profileID, scheduleID) == claimToken {
		return schedule, nil
	}
	if schedule.Status != ScheduleClaimed {
		return Schedule{}, ErrScheduleConflict
	}
	if schedule.ClaimToken != claimToken {
		return Schedule{}, ErrScheduleConflict
	}
	nextStatus := ScheduleCompleted
	nextRunAt := schedule.RunAt
	completedAt := store.timestamp(now)
	cancelledAt := ""
	if schedule.RepeatInterval > 0 {
		nextStatus = SchedulePending
		nextRunAt = schedule.RunAt.Add(schedule.RepeatInterval)
		for !nextRunAt.After(now) {
			nextRunAt = nextRunAt.Add(schedule.RepeatInterval)
		}
		completedAt = ""
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_schedules SET status=?, run_at=?, claim_token='', claim_until='',
		last_claim_token=?, last_outcome_json=?, occurrences=occurrences+1, delivery_attempt_id='', updated_at=?, completed_at=?, cancelled_at=?
		WHERE profile_id=? AND id=? AND status=? AND claim_token=?`,
		string(nextStatus), store.timestamp(nextRunAt), claimToken, string(outcomeJSON), store.timestamp(now),
		completedAt, cancelledAt, profileID, scheduleID, string(ScheduleClaimed), claimToken)
	if err != nil {
		return Schedule{}, fmt.Errorf("complete AI schedule: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{"schedule_id": scheduleID, "status": string(nextStatus), "next_run_at": store.timestamp(nextRunAt)})
	if _, err := store.recordEventTx(ctx, tx, profileID, EventScheduleCompleted, "runtime", detail); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, fmt.Errorf("commit AI schedule completion: %w", err)
	}
	schedule.Status = nextStatus
	schedule.RunAt = nextRunAt
	schedule.DueAt = nextRunAt
	schedule.ClaimToken = ""
	schedule.ClaimUntil = time.Time{}
	schedule.LastOutcome = cloneJSON(outcomeJSON)
	schedule.Occurrences++
	schedule.UpdatedAt = now
	if nextStatus == ScheduleCompleted {
		schedule.CompletedAt = now
	} else {
		schedule.CompletedAt = time.Time{}
	}
	return schedule, nil
}

// CancelSchedule is idempotent when a schedule is already cancelled. A
// completed schedule remains completed and reports a conflict so callers do
// not mistake a stale cancellation for a state change.
func (store *Store) CancelSchedule(ctx context.Context, profileID, scheduleID, reason string) (Schedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateScheduleProfileID(profileID); err != nil {
		return Schedule{}, err
	}
	if err := validateScheduleID(scheduleID); err != nil {
		return Schedule{}, err
	}
	reason = strings.TrimSpace(reason)
	if len([]byte(reason)) > maxSchedulePromptBytes || strings.ContainsRune(reason, '\x00') {
		return Schedule{}, fmt.Errorf("%w: cancellation reason is invalid", ErrInvalidSchedule)
	}
	now := store.now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, fmt.Errorf("begin schedule cancellation: %w", err)
	}
	defer tx.Rollback()
	schedule, err := scanScheduleQuery(ctx, tx.QueryRowContext(ctx, scheduleSelect+" WHERE profile_id=? AND id=?", profileID, scheduleID))
	if err != nil {
		return Schedule{}, err
	}
	if schedule.Status == ScheduleCancelled {
		return schedule, nil
	}
	if schedule.Status == ScheduleCompleted {
		return Schedule{}, ErrScheduleConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_schedules SET status=?, claim_token='', claim_until='', delivery_attempt_id='', cancelled_at=?, updated_at=?
		WHERE profile_id=? AND id=? AND status IN (?, ?)`, string(ScheduleCancelled), store.timestamp(now), store.timestamp(now),
		profileID, scheduleID, string(SchedulePending), string(ScheduleClaimed))
	if err != nil {
		return Schedule{}, fmt.Errorf("cancel AI schedule: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{"schedule_id": scheduleID, "reason": reason})
	if _, err := store.recordEventTx(ctx, tx, profileID, EventScheduleCancelled, "runtime", detail); err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, fmt.Errorf("commit AI schedule cancellation: %w", err)
	}
	schedule.Status = ScheduleCancelled
	schedule.ClaimToken = ""
	schedule.ClaimUntil = time.Time{}
	schedule.CancelledAt = now
	schedule.UpdatedAt = now
	return schedule, nil
}

// ReclaimDueSchedules is a descriptive alias for callers which want to make
// the restart behavior explicit; ClaimDueSchedules already reclaims expired
// leases atomically.
func (store *Store) ReclaimDueSchedules(ctx context.Context, profileID string, at time.Time, limit ...int) ([]Schedule, error) {
	return store.ClaimDueSchedules(ctx, profileID, at, limit...)
}

const scheduleSelect = `SELECT id, profile_id, kind, title, prompt, run_at, repeat_interval_ns,
	status, claim_token, claim_until, delivery_attempt_id, idempotency_key, occurrences, last_outcome_json,
	created_at, updated_at, completed_at, cancelled_at FROM ai_schedules`

func scanScheduleQuery(ctx context.Context, row *sql.Row) (Schedule, error) {
	var schedule Schedule
	var runAt, claimUntil, lastOutcome, created, updated, completed, cancelled string
	var repeatNanos int64
	var occurrences int
	err := row.Scan(&schedule.ID, &schedule.ProfileID, &schedule.Kind, &schedule.Title, &schedule.Prompt,
		&runAt, &repeatNanos, &schedule.Status, &schedule.ClaimToken, &claimUntil, &schedule.DeliveryAttemptID, &schedule.IdempotencyKey,
		&occurrences, &lastOutcome, &created, &updated, &completed, &cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("scan AI schedule: %w", err)
	}
	return finishScheduleScan(schedule, runAt, claimUntil, repeatNanos, occurrences, lastOutcome, created, updated, completed, cancelled)
}

func scanSchedule(row scanner) (Schedule, error) {
	var schedule Schedule
	var runAt, claimUntil, lastOutcome, created, updated, completed, cancelled string
	var repeatNanos int64
	var occurrences int
	if err := row.Scan(&schedule.ID, &schedule.ProfileID, &schedule.Kind, &schedule.Title, &schedule.Prompt,
		&runAt, &repeatNanos, &schedule.Status, &schedule.ClaimToken, &claimUntil, &schedule.DeliveryAttemptID, &schedule.IdempotencyKey,
		&occurrences, &lastOutcome, &created, &updated, &completed, &cancelled); err != nil {
		return Schedule{}, fmt.Errorf("scan AI schedule: %w", err)
	}
	return finishScheduleScan(schedule, runAt, claimUntil, repeatNanos, occurrences, lastOutcome, created, updated, completed, cancelled)
}

func finishScheduleScan(schedule Schedule, runAt, claimUntil string, repeatNanos int64, occurrences int, lastOutcome, created, updated, completed, cancelled string) (Schedule, error) {
	var err error
	schedule.RunAt, err = parseTimestamp(runAt)
	if err != nil {
		return Schedule{}, err
	}
	schedule.DueAt = schedule.RunAt
	schedule.RepeatInterval = time.Duration(repeatNanos)
	schedule.Occurrences = occurrences
	if lastOutcome != "" {
		schedule.LastOutcome = json.RawMessage(lastOutcome)
	}
	if claimUntil != "" {
		schedule.ClaimUntil, err = parseTimestamp(claimUntil)
		if err != nil {
			return Schedule{}, err
		}
	}
	schedule.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return Schedule{}, err
	}
	schedule.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return Schedule{}, err
	}
	if completed != "" {
		schedule.CompletedAt, err = parseTimestamp(completed)
		if err != nil {
			return Schedule{}, err
		}
	}
	if cancelled != "" {
		schedule.CancelledAt, err = parseTimestamp(cancelled)
		if err != nil {
			return Schedule{}, err
		}
	}
	return schedule, nil
}

func scheduleInputMatches(schedule Schedule, input ScheduleInput, idProvided bool) bool {
	return (!idProvided || schedule.ID == input.ID) && schedule.Kind == input.Kind && schedule.Title == input.Title &&
		schedule.Prompt == input.Prompt && schedule.RunAt.Equal(input.RunAt) &&
		schedule.RepeatInterval == input.RepeatInterval && schedule.IdempotencyKey == input.IdempotencyKey
}

func scheduleLastClaimToken(ctx context.Context, tx *sql.Tx, profileID, scheduleID string) string {
	var token string
	_ = tx.QueryRowContext(ctx, "SELECT last_claim_token FROM ai_schedules WHERE profile_id=? AND id=?", profileID, scheduleID).Scan(&token)
	return token
}

func scheduleOutcomeJSON(outcome any) (json.RawMessage, error) {
	if outcome == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := outcome.(json.RawMessage); ok {
		if len(bytesTrim(raw)) == 0 {
			return json.RawMessage(`{}`), nil
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: completion outcome is not JSON", ErrInvalidSchedule)
		}
		if len(raw) > maxSchedulePromptBytes {
			return nil, fmt.Errorf("%w: completion outcome is too large", ErrInvalidSchedule)
		}
		return cloneJSON(raw), nil
	}
	encoded, err := json.Marshal(outcome)
	if err != nil || len(encoded) > maxSchedulePromptBytes || !json.Valid(encoded) {
		return nil, fmt.Errorf("%w: completion outcome is invalid", ErrInvalidSchedule)
	}
	return encoded, nil
}
