package airuntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const UnknownReviewReason = "accept_uncertain_outcome"

// UnknownAttemptReview records an operator acknowledgement, never a model result.
type UnknownAttemptReview struct {
	AttemptID  string    `json:"attempt_id"`
	ProfileID  string    `json:"profile_id"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

type ReviewUnknownAttemptRequest struct {
	AttemptID                 string
	ProfileID                 string
	ExpectedUpdatedAt         time.Time
	ExpectedProfileVersion    int64
	ExpectedCheckpointVersion int64
	CheckpointState           json.RawMessage
	Actor                     string
	Reason                    string
}

func (store *Store) GetUnknownAttemptReview(ctx context.Context, profileID, attemptID string) (UnknownAttemptReview, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var review UnknownAttemptReview
	var at string
	err := store.db.QueryRowContext(ctx, `SELECT attempt_id,profile_id,actor,reason,reviewed_at FROM ai_unknown_attempt_reviews WHERE attempt_id=? AND profile_id=?`, attemptID, profileID).Scan(&review.AttemptID, &review.ProfileID, &review.Actor, &review.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return review, ErrNotFound
	}
	if err != nil {
		return review, err
	}
	review.ReviewedAt, err = parseTimestamp(at)
	return review, err
}

// ReviewUnknownAttempt consumes the reserved budget conservatively and clears
// the supervisor recovery checkpoint in the same transaction. The old attempt
// remains unknown, with no fabricated provider outcome or measured usage.
func (store *Store) ReviewUnknownAttempt(ctx context.Context, request ReviewUnknownAttemptRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Reason != UnknownReviewReason || strings.TrimSpace(request.Actor) == "" || len(request.Actor) > 128 || strings.ContainsAny(request.Actor, "\x00\r\n") || request.ExpectedUpdatedAt.IsZero() || request.ExpectedCheckpointVersion < 1 || !json.Valid(request.CheckpointState) {
		return ErrAttemptConflict
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reviewActor, reviewReason string
	err = tx.QueryRowContext(ctx, `SELECT actor,reason FROM ai_unknown_attempt_reviews WHERE attempt_id=? AND profile_id=?`, request.AttemptID, request.ProfileID).Scan(&reviewActor, &reviewReason)
	if err == nil {
		if reviewActor == request.Actor && reviewReason == request.Reason {
			return nil
		}
		return ErrAttemptConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	profile, err := scanProfileTx(ctx, tx, request.ProfileID)
	if err != nil {
		return err
	}
	if profile.Version != request.ExpectedProfileVersion || (profile.Status != ProfileStatusPaused && profile.Status != ProfileStatusStopped) {
		return ErrAttemptConflict
	}
	var state, updated, date string
	var charge int64
	var settled int
	err = tx.QueryRowContext(ctx, `SELECT state,updated_at,usage_date,charge_tokens,settled FROM ai_token_attempts WHERE id=? AND profile_id=?`, request.AttemptID, request.ProfileID).Scan(&state, &updated, &date, &charge, &settled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	expected, err := parseTimestamp(updated)
	if err != nil {
		return err
	}
	if state != string(TokenAttemptUnknown) || settled != 0 || !expected.Equal(request.ExpectedUpdatedAt) {
		return ErrAttemptConflict
	}
	var checkpointVersion int64
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT version,state_json FROM ai_checkpoints WHERE profile_id=?`, request.ProfileID).Scan(&checkpointVersion, &previous); err != nil {
		return err
	}
	if checkpointVersion != request.ExpectedCheckpointVersion {
		return ErrConflict
	}
	var before, after struct {
		PendingAttemptID string `json:"pending_attempt_id"`
		ThreadID         string `json:"thread_id"`
		ModelTurnDone    bool   `json:"model_turn_done"`
	}
	if json.Unmarshal([]byte(previous), &before) != nil || json.Unmarshal(request.CheckpointState, &after) != nil || before.PendingAttemptID != request.AttemptID || after.PendingAttemptID != "" || after.ThreadID != "" || after.ModelTurnDone {
		return ErrAttemptConflict
	}
	now := store.timestamp(store.now())
	if _, err = tx.ExecContext(ctx, `UPDATE ai_token_usage SET reserved_tokens=reserved_tokens-?,charged_tokens=charged_tokens+?,failed_attempts=failed_attempts+1 WHERE profile_id=? AND usage_date=?`, charge, charge, request.ProfileID, date); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_token_attempts SET settled=1,failed=1,updated_at=?,settled_at=? WHERE id=?`, now, now, request.AttemptID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_checkpoints SET version=version+1,state_json=?,updated_at=? WHERE profile_id=?`, string(request.CheckpointState), now, request.ProfileID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_unknown_attempt_reviews(attempt_id,profile_id,actor,reason,reviewed_at) VALUES(?,?,?,?,?)`, request.AttemptID, request.ProfileID, request.Actor, request.Reason, now); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"attempt_id": request.AttemptID, "reason": request.Reason, "charged_tokens": charge, "outcome": "unknown", "checkpoint_version": checkpointVersion + 1})
	if _, err = store.recordEventTx(ctx, tx, request.ProfileID, "ai_unknown_attempt_reviewed", request.Actor, detail); err != nil {
		return err
	}
	return tx.Commit()
}
