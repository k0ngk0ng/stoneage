package aibroker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
	_ "modernc.org/sqlite"
)

const maxJournalBytes = 64 << 20

const maxReviewActorBytes = 128

// SQLiteJournal is the durable request claim/transition store. SQLite's
// UNIQUE constraint and transaction locking make request claims atomic across
// two Broker values and across separate broker processes sharing the file.
// Only a payload hash and a redacted response are persisted.
type SQLiteJournal struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

// OpenSQLiteJournal opens (and migrates) a private broker journal. The path
// is checked against the operator Codex/config roots before any directory or
// database file is created. Existing symlink components are rejected so a
// deployment cannot redirect the journal to an unexpected tree.
func OpenSQLiteJournal(path string) (*SQLiteJournal, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: journal path is empty", ErrInvalidConfig)
	}
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return openSQLiteJournal(path, path)
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil || abs == "" || abs == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: journal path is invalid", ErrInvalidConfig)
	}
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, fmt.Errorf("%w: initialize journal path guard", ErrInvalidConfig)
	}
	if err := guard.Check(abs); err != nil {
		return nil, fmt.Errorf("%w: journal path is not isolated", ErrInvalidConfig)
	}
	if err := rejectSymlinkAncestors(abs); err != nil {
		return nil, fmt.Errorf("%w: journal path is not isolated", ErrInvalidConfig)
	}
	directory := filepath.Dir(abs)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create journal directory", ErrInvalidConfig)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("%w: protect journal directory", ErrInvalidConfig)
	}
	if info, statErr := os.Lstat(abs); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: journal must be a regular file", ErrInvalidConfig)
		}
		if info.Size() > maxJournalBytes {
			return nil, fmt.Errorf("%w: journal is too large", ErrInvalidConfig)
		}
		if err := os.Chmod(abs, 0o600); err != nil {
			return nil, fmt.Errorf("%w: protect journal", ErrInvalidConfig)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect journal", ErrInvalidConfig)
	}
	return openSQLiteJournal(abs, abs)
}

func openSQLiteJournal(dsn, path string) (*SQLiteJournal, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open journal", ErrInvalidConfig)
	}
	// One connection per Journal keeps in-memory tests coherent and still
	// lets separate SQLiteJournal instances/processes serialize claims through
	// SQLite's file lock.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%w: configure journal", ErrInvalidConfig)
		}
	}
	const schema = `
CREATE TABLE IF NOT EXISTS ai_broker_runs (
  profile_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('running','completed','unknown')),
  container_name TEXT NOT NULL,
  volume_name TEXT NOT NULL,
  response BLOB,
  error_code TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  review_actor TEXT NOT NULL DEFAULT '',
  review_reason TEXT NOT NULL DEFAULT '',
  reviewed_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (profile_id, request_id)

);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: migrate journal", ErrInvalidConfig)
	}
	if err := migrateSQLiteJournal(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: migrate journal", ErrInvalidConfig)
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file::memory:") {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%w: protect journal", ErrInvalidConfig)
		}
	}
	return &SQLiteJournal{db: db, path: path}, nil
}

// migrateSQLiteJournal upgrades journals created before operator review was
// introduced. The index rebuild happens in the same SQLite transaction as any
// column additions, so a reviewed unknown row releases its profile claim
// atomically with the metadata write.
func migrateSQLiteJournal(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	rows, err := tx.Query(`PRAGMA table_info(ai_broker_runs)`)
	if err != nil {
		return err
	}
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"review_actor", "review_reason", "reviewed_at"} {
		if _, ok := columns[column]; ok {
			continue
		}
		if _, err := tx.Exec(`ALTER TABLE ai_broker_runs ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DROP INDEX IF EXISTS ai_broker_active_profile_idx`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX ai_broker_active_profile_idx
ON ai_broker_runs(profile_id)
WHERE state='running' OR (state='unknown' AND reviewed_at='')`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// Path returns the configured SQLite path.
func (journal *SQLiteJournal) Path() string {
	if journal == nil {
		return ""
	}
	return journal.path
}

func (journal *SQLiteJournal) Close() error {
	if journal == nil || journal.db == nil {
		return nil
	}
	return journal.db.Close()
}

func (journal *SQLiteJournal) Get(ctx context.Context, profileID, requestID string) (JournalEntry, error) {
	if journal == nil || journal.db == nil {
		return JournalEntry{}, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return JournalEntry{}, err
	}
	if _, err := entryKey(profileID, requestID); err != nil {
		return JournalEntry{}, err
	}
	var entry JournalEntry
	var state, updated, reviewActor, reviewReason, reviewedAt string
	var response []byte
	err := journal.db.QueryRowContext(ctx, `SELECT payload_hash,state,container_name,volume_name,response,error_code,updated_at,review_actor,review_reason,reviewed_at FROM ai_broker_runs WHERE profile_id=? AND request_id=?`, profileID, requestID).
		Scan(&entry.PayloadHash, &state, &entry.ContainerName, &entry.VolumeName, &response, &entry.ErrorCode, &updated, &reviewActor, &reviewReason, &reviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return JournalEntry{}, ErrJournalNotFound
	}
	if err != nil {
		return JournalEntry{}, fmt.Errorf("%w: read journal", ErrDocker)
	}
	entry.ProfileID, entry.RequestID, entry.State = profileID, requestID, RunState(state)
	entry.Response = append([]byte(nil), response...)
	if updated != "" {
		entry.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return JournalEntry{}, fmt.Errorf("%w: journal timestamp is corrupt", ErrDocker)
		}
	}
	entry.Review, err = decodeReview(reviewActor, reviewReason, reviewedAt)
	if err != nil {
		return JournalEntry{}, fmt.Errorf("%w: journal review is corrupt", ErrDocker)
	}
	if _, err := validateEntry(entry); err != nil {
		return JournalEntry{}, fmt.Errorf("%w: journal entry is corrupt", ErrDocker)
	}
	return entry, nil
}

// ListRunning returns durable claims that may have been left by a broker
// process which exited before publishing an outcome. The caller owns the
// returned slice and may reconcile it while holding the broker process lock.
func (journal *SQLiteJournal) ListRunning(ctx context.Context) ([]JournalEntry, error) {
	if journal == nil || journal.db == nil {
		return nil, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	rows, err := journal.db.QueryContext(ctx, `SELECT profile_id,request_id,payload_hash,state,container_name,volume_name,response,error_code,updated_at FROM ai_broker_runs WHERE state=? ORDER BY updated_at,profile_id,request_id`, string(RunRunning))
	if err != nil {
		return nil, fmt.Errorf("%w: list running journal entries", ErrDocker)
	}
	defer rows.Close()
	entries := make([]JournalEntry, 0)
	for rows.Next() {
		var entry JournalEntry
		var state, updated string
		var response []byte
		if err := rows.Scan(&entry.ProfileID, &entry.RequestID, &entry.PayloadHash, &state, &entry.ContainerName, &entry.VolumeName, &response, &entry.ErrorCode, &updated); err != nil {
			return nil, fmt.Errorf("%w: scan running journal entry", ErrDocker)
		}
		entry.State = RunState(state)
		entry.Response = append([]byte(nil), response...)
		if updated != "" {
			entry.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
			if err != nil {
				return nil, fmt.Errorf("%w: journal timestamp is corrupt", ErrDocker)
			}
		}
		if _, err := validateEntry(entry); err != nil {
			return nil, fmt.Errorf("%w: journal entry is corrupt", ErrDocker)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list running journal entries", ErrDocker)
	}
	return entries, nil
}

func (journal *SQLiteJournal) Create(ctx context.Context, entry JournalEntry) (bool, error) {
	if journal == nil || journal.db == nil {
		return false, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	if _, err := validateEntry(entry); err != nil {
		return false, err
	}
	if entry.Review != nil {
		return false, ErrJournalState
	}
	// INSERT OR IGNORE is atomic under SQLite's primary-key constraint. A
	// loser of a concurrent claim does not overwrite the first request's hash.
	result, err := journal.db.ExecContext(ctx, `INSERT OR IGNORE INTO ai_broker_runs(profile_id,request_id,payload_hash,state,container_name,volume_name,response,error_code,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		entry.ProfileID, entry.RequestID, entry.PayloadHash, string(entry.State), entry.ContainerName, entry.VolumeName, nullableBytes(entry.Response), entry.ErrorCode, formatJournalTime(entry.UpdatedAt))
	if err != nil {
		return false, fmt.Errorf("%w: claim journal entry", ErrDocker)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: inspect journal claim", ErrDocker)
	}
	if count == 1 {
		return true, nil
	}
	// A different request for the same profile cannot share the persistent
	// state volume while an earlier run is unresolved. The partial unique
	// index makes this check atomic across journal/database instances.
	var activeRequest string
	err = journal.db.QueryRowContext(ctx, `SELECT request_id FROM ai_broker_runs WHERE profile_id=? AND (state='running' OR (state='unknown' AND reviewed_at='')) LIMIT 1`, entry.ProfileID).Scan(&activeRequest)
	if err == nil && activeRequest != entry.RequestID {
		return false, ErrProfileBusy
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDocker
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect active profile", ErrDocker)
	}
	return false, nil
}

func (journal *SQLiteJournal) Update(ctx context.Context, entry JournalEntry) error {
	if journal == nil || journal.db == nil {
		return fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if _, err := validateEntry(entry); err != nil {
		return err
	}
	if entry.Review != nil {
		return ErrJournalState
	}
	// A completed result is terminal. Keep this legacy update method
	// compare-and-publish safe for callers that do not use UpdateIfState.
	result, err := journal.db.ExecContext(ctx, `UPDATE ai_broker_runs SET state=?,container_name=?,volume_name=?,response=?,error_code=?,updated_at=? WHERE profile_id=? AND request_id=? AND payload_hash=? AND state<>? AND reviewed_at=''`,
		string(entry.State), entry.ContainerName, entry.VolumeName, nullableBytes(entry.Response), entry.ErrorCode, formatJournalTime(entry.UpdatedAt), entry.ProfileID, entry.RequestID, entry.PayloadHash, string(RunCompleted))
	if err != nil {
		return fmt.Errorf("%w: update journal entry", ErrDocker)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: inspect journal update", ErrDocker)
	}
	if count == 1 {
		return nil
	}
	var existingHash string
	err = journal.db.QueryRowContext(ctx, `SELECT payload_hash FROM ai_broker_runs WHERE profile_id=? AND request_id=?`, entry.ProfileID, entry.RequestID).Scan(&existingHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrJournalNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: inspect journal entry", ErrDocker)
	}
	if existingHash != entry.PayloadHash {
		return ErrJournalConflict
	}
	var existingState, reviewedAt string
	if err := journal.db.QueryRowContext(ctx, `SELECT state,reviewed_at FROM ai_broker_runs WHERE profile_id=? AND request_id=?`, entry.ProfileID, entry.RequestID).Scan(&existingState, &reviewedAt); err == nil {
		if existingState == string(RunCompleted) || reviewedAt != "" {
			return ErrJournalState
		}
	}
	return ErrDocker
}

// UpdateIfState publishes entry only when the durable row is still in the
// expected state. This is used for startup reconciliation and late runtime
// results so a newer terminal outcome cannot be overwritten.
func (journal *SQLiteJournal) UpdateIfState(ctx context.Context, expected RunState, entry JournalEntry) error {
	if journal == nil || journal.db == nil {
		return fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if expected != RunRunning && expected != RunCompleted && expected != RunUnknown {
		return fmt.Errorf("%w: expected journal state is invalid", ErrInvalidRequest)
	}
	// Completed outcomes are immutable through every publication path.
	// Matching the terminal state is not permission to replace its result.
	if expected == RunCompleted {
		return ErrJournalState
	}
	if _, err := validateEntry(entry); err != nil {
		return err
	}
	if entry.Review != nil {
		return ErrJournalState
	}
	result, err := journal.db.ExecContext(ctx, `UPDATE ai_broker_runs SET state=?,container_name=?,volume_name=?,response=?,error_code=?,updated_at=? WHERE profile_id=? AND request_id=? AND payload_hash=? AND state=? AND reviewed_at=''`,
		string(entry.State), entry.ContainerName, entry.VolumeName, nullableBytes(entry.Response), entry.ErrorCode, formatJournalTime(entry.UpdatedAt), entry.ProfileID, entry.RequestID, entry.PayloadHash, string(expected))
	if err != nil {
		return fmt.Errorf("%w: compare-and-update journal entry", ErrDocker)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: inspect journal update", ErrDocker)
	}
	if count == 1 {
		return nil
	}
	var existingHash, existingState, reviewedAt string
	err = journal.db.QueryRowContext(ctx, `SELECT payload_hash,state,reviewed_at FROM ai_broker_runs WHERE profile_id=? AND request_id=?`, entry.ProfileID, entry.RequestID).Scan(&existingHash, &existingState, &reviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrJournalNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: inspect journal entry", ErrDocker)
	}
	if existingHash != entry.PayloadHash {
		return ErrJournalConflict
	}
	if RunState(existingState) != expected || reviewedAt != "" {
		return ErrJournalState
	}
	return ErrDocker
}

// ReviewUnknown records a fixed operator disposition while retaining the
// unknown state and response. The reviewed_at column is intentionally part of
// the active-profile partial index, so this one UPDATE atomically releases the
// profile claim with the review metadata.
func (journal *SQLiteJournal) ReviewUnknown(ctx context.Context, profileID, requestID string, expectedUpdatedAt time.Time, actor, reason string) (JournalEntry, error) {
	if journal == nil || journal.db == nil {
		return JournalEntry{}, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	if err := checkContext(ctx); err != nil {
		return JournalEntry{}, err
	}
	if _, err := entryKey(profileID, requestID); err != nil {
		return JournalEntry{}, err
	}
	if err := validateReviewInput(expectedUpdatedAt, actor, reason); err != nil {
		return JournalEntry{}, err
	}
	reviewedAt := formatJournalTime(time.Now().UTC())
	result, err := journal.db.ExecContext(ctx, `UPDATE ai_broker_runs SET review_actor=?,review_reason=?,reviewed_at=? WHERE profile_id=? AND request_id=? AND state=? AND updated_at=? AND reviewed_at=''`,
		actor, reason, reviewedAt, profileID, requestID, string(RunUnknown), formatJournalTime(expectedUpdatedAt))
	if err != nil {
		return JournalEntry{}, fmt.Errorf("%w: review unknown journal entry", ErrDocker)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return JournalEntry{}, fmt.Errorf("%w: inspect unknown review", ErrDocker)
	}
	if count == 1 {
		return journal.Get(ctx, profileID, requestID)
	}

	// A retry of the exact same disposition is idempotent even though the
	// active-profile index was released by the first UPDATE. A different
	// disposition, stale version, or non-unknown row is rejected.
	current, getErr := journal.Get(ctx, profileID, requestID)
	if getErr != nil {
		return JournalEntry{}, getErr
	}
	if current.Review != nil {
		if current.Review.Actor == actor && current.Review.Reason == reason {
			return current, nil
		}
		return JournalEntry{}, ErrJournalState
	}
	return JournalEntry{}, ErrJournalState
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func formatJournalTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func validateReviewInput(expectedUpdatedAt time.Time, actor, reason string) error {
	if expectedUpdatedAt.IsZero() {
		return fmt.Errorf("%w: expected journal timestamp is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(actor) == "" || len(actor) > maxReviewActorBytes || strings.ContainsAny(actor, "\x00\r\n") {
		return fmt.Errorf("%w: review actor is invalid", ErrInvalidRequest)
	}
	if reason != ReviewReasonAcceptUncertainOutcome {
		return fmt.Errorf("%w: review reason is invalid", ErrInvalidRequest)
	}
	return nil
}

func validateStoredReview(review Review) error {
	if strings.TrimSpace(review.Actor) == "" || len(review.Actor) > maxReviewActorBytes || strings.ContainsAny(review.Actor, "\x00\r\n") {
		return fmt.Errorf("%w: review actor is invalid", ErrInvalidRequest)
	}
	if review.Reason != ReviewReasonAcceptUncertainOutcome || review.ReviewedAt.IsZero() {
		return fmt.Errorf("%w: review metadata is invalid", ErrInvalidRequest)
	}
	return nil
}

func decodeReview(actor, reason, reviewedAt string) (*Review, error) {
	if actor == "" && reason == "" && reviewedAt == "" {
		return nil, nil
	}
	if actor == "" || reason == "" || reviewedAt == "" {
		return nil, fmt.Errorf("%w: review metadata is incomplete", ErrInvalidRequest)
	}
	at, err := time.Parse(time.RFC3339Nano, reviewedAt)
	if err != nil {
		return nil, err
	}
	review := &Review{Actor: actor, Reason: reason, ReviewedAt: at}
	if err := validateStoredReview(*review); err != nil {
		return nil, err
	}
	return review, nil
}

func cloneReview(review *Review) *Review {
	if review == nil {
		return nil
	}
	copy := *review
	return &copy
}

// MemoryJournal is an in-process seam for unit tests. Production callers must
// use SQLiteJournal so concurrent broker processes share claims.
type MemoryJournal struct {
	mu      sync.Mutex
	entries map[string]JournalEntry
}

func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{entries: make(map[string]JournalEntry)} }

func (journal *MemoryJournal) Get(ctx context.Context, profileID, requestID string) (JournalEntry, error) {
	if err := checkContext(ctx); err != nil {
		return JournalEntry{}, err
	}
	key, err := entryKey(profileID, requestID)
	if err != nil {
		return JournalEntry{}, err
	}
	if journal == nil {
		return JournalEntry{}, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, ok := journal.entries[key]
	if !ok {
		return JournalEntry{}, ErrJournalNotFound
	}
	entry.Response = append([]byte(nil), entry.Response...)
	entry.Review = cloneReview(entry.Review)
	return entry, nil
}

func (journal *MemoryJournal) ListRunning(ctx context.Context) ([]JournalEntry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if journal == nil {
		return nil, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	keys := make([]string, 0, len(journal.entries))
	for key, entry := range journal.entries {
		if entry.State == RunRunning {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	entries := make([]JournalEntry, 0, len(keys))
	for _, key := range keys {
		entry := journal.entries[key]
		entry.Response = append([]byte(nil), entry.Response...)
		entries = append(entries, entry)
	}
	return entries, nil
}

func (journal *MemoryJournal) Create(ctx context.Context, entry JournalEntry) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	key, err := validateEntry(entry)
	if err != nil {
		return false, err
	}
	if entry.Review != nil {
		return false, ErrJournalState
	}
	if journal == nil {
		return false, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if _, ok := journal.entries[key]; ok {
		return false, nil
	}
	for activeKey, existing := range journal.entries {
		if existing.ProfileID == entry.ProfileID && (existing.State == RunRunning || (existing.State == RunUnknown && existing.Review == nil)) && activeKey != key {
			return false, ErrProfileBusy
		}
	}
	entry.Response = append([]byte(nil), entry.Response...)
	journal.entries[key] = entry
	return true, nil
}

func (journal *MemoryJournal) Update(ctx context.Context, entry JournalEntry) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	key, err := validateEntry(entry)
	if err != nil {
		return err
	}
	if entry.Review != nil {
		return ErrJournalState
	}
	if journal == nil {
		return fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	old, ok := journal.entries[key]
	if !ok {
		return ErrJournalNotFound
	}
	if old.PayloadHash != entry.PayloadHash {
		return ErrJournalConflict
	}
	if old.State == RunCompleted {
		return ErrJournalState
	}
	if old.Review != nil {
		return ErrJournalState
	}
	entry.Response = append([]byte(nil), entry.Response...)
	journal.entries[key] = entry
	return nil
}

func (journal *MemoryJournal) UpdateIfState(ctx context.Context, expected RunState, entry JournalEntry) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if expected != RunRunning && expected != RunCompleted && expected != RunUnknown {
		return fmt.Errorf("%w: expected journal state is invalid", ErrInvalidRequest)
	}
	// Completed outcomes are immutable through every publication path.
	// Matching the terminal state is not permission to replace its result.
	if expected == RunCompleted {
		return ErrJournalState
	}
	key, err := validateEntry(entry)
	if err != nil {
		return err
	}
	if entry.Review != nil {
		return ErrJournalState
	}
	if journal == nil {
		return fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	old, ok := journal.entries[key]
	if !ok {
		return ErrJournalNotFound
	}
	if old.PayloadHash != entry.PayloadHash {
		return ErrJournalConflict
	}
	if old.State != expected {
		return ErrJournalState
	}
	if old.Review != nil {
		return ErrJournalState
	}
	entry.Response = append([]byte(nil), entry.Response...)
	journal.entries[key] = entry
	return nil
}

func (journal *MemoryJournal) ReviewUnknown(ctx context.Context, profileID, requestID string, expectedUpdatedAt time.Time, actor, reason string) (JournalEntry, error) {
	if err := checkContext(ctx); err != nil {
		return JournalEntry{}, err
	}
	key, err := entryKey(profileID, requestID)
	if err != nil {
		return JournalEntry{}, err
	}
	if err := validateReviewInput(expectedUpdatedAt, actor, reason); err != nil {
		return JournalEntry{}, err
	}
	if journal == nil {
		return JournalEntry{}, fmt.Errorf("%w: journal is nil", ErrInvalidConfig)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, ok := journal.entries[key]
	if !ok {
		return JournalEntry{}, ErrJournalNotFound
	}
	if entry.Review != nil {
		if entry.Review.Actor == actor && entry.Review.Reason == reason {
			entry.Response = append([]byte(nil), entry.Response...)
			entry.Review = cloneReview(entry.Review)
			return entry, nil
		}
		return JournalEntry{}, ErrJournalState
	}
	if entry.State != RunUnknown || !entry.UpdatedAt.Equal(expectedUpdatedAt) {
		return JournalEntry{}, ErrJournalState
	}
	entry.Review = &Review{Actor: actor, Reason: reason, ReviewedAt: time.Now().UTC()}
	entry.Response = append([]byte(nil), entry.Response...)
	entry.Review = cloneReview(entry.Review)
	journal.entries[key] = entry
	return entry, nil
}

func (journal *MemoryJournal) Close() error { return nil }

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func entryKey(profileID, requestID string) (string, error) {
	if profileID == "" || requestID == "" {
		return "", fmt.Errorf("%w: profile and request IDs are required", ErrInvalidRequest)
	}
	return profileID + "\x00" + requestID, nil
}

func validateEntry(entry JournalEntry) (string, error) {
	key, err := entryKey(entry.ProfileID, entry.RequestID)
	if err != nil {
		return "", err
	}
	if len(entry.PayloadHash) != 64 || !isLowerHex(entry.PayloadHash) {
		return "", fmt.Errorf("%w: payload hash is invalid", ErrInvalidRequest)
	}
	switch entry.State {
	case RunRunning, RunCompleted, RunUnknown:
	default:
		return "", fmt.Errorf("%w: journal state is invalid", ErrInvalidRequest)
	}
	if !containerNamePattern.MatchString(entry.ContainerName) || !volumeNamePattern.MatchString(entry.VolumeName) {
		return "", fmt.Errorf("%w: Docker names are invalid", ErrInvalidRequest)
	}
	if entry.Review != nil {
		if entry.State != RunUnknown {
			return "", fmt.Errorf("%w: review is only valid for unknown runs", ErrInvalidRequest)
		}
		if err := validateStoredReview(*entry.Review); err != nil {
			return "", err
		}
	}
	return key, nil
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func rejectSymlinkAncestors(path string) error {
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("symbolic-link parent")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

var _ Journal = (*SQLiteJournal)(nil)
var _ Journal = (*MemoryJournal)(nil)
