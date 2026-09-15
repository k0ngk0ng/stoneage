package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Gift target scopes are deliberately small. A single run may either target
// one explicitly selected character or contain the complete target list for a
// broadcast. The list is materialized when the run is created so that later
// account changes cannot change the meaning of a run.
const (
	GiftTargetSingle    = "single"
	GiftTargetAll       = "all"
	GiftTargetCharacter = GiftTargetSingle

	GiftRunPending   = "pending"
	GiftRunRunning   = "running"
	GiftRunCompleted = "completed"
	GiftRunFailed    = "failed"
	GiftRunUncertain = "uncertain"

	GiftDeliveryPending   = "pending"
	GiftDeliverySending   = "sending"
	GiftDeliveryApplied   = "applied"
	GiftDeliverySkipped   = "skip_capacity"
	GiftDeliveryFailed    = "failed"
	GiftDeliveryUncertain = "uncertain"
)

var (
	// ErrGiftDeliveryNotPending means another worker has already claimed the
	// target, or that it was finalized. Callers must inspect the delivery and
	// never turn a sending delivery back into pending automatically.
	ErrGiftDeliveryNotPending = errors.New("gift delivery is not pending")
	// ErrGiftDeliveryFinalized means a terminal result already exists. Repeated
	// completion with the same result is idempotent and does not return it.
	ErrGiftDeliveryFinalized = errors.New("gift delivery is already finalized")
)

type GiftPackage struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// GiftTarget is a concrete account/character target captured in a run. The
// character name is a binding guard for the game bridge and is intentionally
// kept with the target even when it is empty for a legacy record.
type GiftTarget struct {
	AccountID     int64  `json:"account_id"`
	CharacterSlot int    `json:"character_slot"`
	CharacterName string `json:"character_name"`
	SkipReason    string `json:"skip_reason,omitempty"`
}

type GiftRun struct {
	ID              int64           `json:"id"`
	PackageID       int64           `json:"package_id"`
	PackageSnapshot json.RawMessage `json:"package_snapshot"`
	// PackageName and Definition are decoded convenience fields. The raw
	// PackageSnapshot remains authoritative and is preserved for future fields.
	PackageName string          `json:"package_name"`
	Definition  json.RawMessage `json:"definition"`
	TargetScope string          `json:"target_scope"`
	AdminID     int64           `json:"admin_id"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type GiftDelivery struct {
	ID            int64           `json:"id"`
	RunID         int64           `json:"run_id"`
	AccountID     int64           `json:"account_id"`
	CharacterSlot int             `json:"character_slot"`
	CharacterName string          `json:"character_name"`
	Status        string          `json:"status"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         string          `json:"error,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type GiftRunPage struct {
	Runs       []GiftRun `json:"runs"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type GiftDeliveryPage struct {
	Deliveries []GiftDelivery `json:"deliveries"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type GiftRunDetails struct {
	Run        GiftRun        `json:"run"`
	Deliveries []GiftDelivery `json:"deliveries"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// migrateGiftSchema is kept separate from the account schema so the store's
// migration hook remains small and old databases receive only additive tables.
func (store *Store) migrateGiftSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS gift_packages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  definition_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS gift_packages_updated_idx ON gift_packages(updated_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS gift_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  package_id INTEGER REFERENCES gift_packages(id) ON DELETE SET NULL,
  package_snapshot TEXT NOT NULL,
  target_scope TEXT NOT NULL CHECK (target_scope IN ('single','all')),
  admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
  status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','uncertain')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS gift_runs_created_idx ON gift_runs(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS gift_runs_status_idx ON gift_runs(status, id DESC);
CREATE TABLE IF NOT EXISTS gift_deliveries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL REFERENCES gift_runs(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
  character_slot INTEGER NOT NULL CHECK (character_slot >= 0 AND character_slot <= 1),
  character_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending','sending','applied','skip_capacity','failed','uncertain')),
  payload TEXT,
  result TEXT,
  error TEXT,
  updated_at TEXT NOT NULL,
  UNIQUE (run_id, account_id, character_slot)
);
CREATE INDEX IF NOT EXISTS gift_deliveries_run_idx ON gift_deliveries(run_id, id);
CREATE INDEX IF NOT EXISTS gift_deliveries_status_idx ON gift_deliveries(status, id);
`
	_, err := store.db.ExecContext(ctx, schema)
	return err
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func validateGiftName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("gift package name is required")
	}
	if len(name) > 200 {
		return "", errors.New("gift package name is too long")
	}
	return name, nil
}

func validateGiftDefinition(definition json.RawMessage) (json.RawMessage, error) {
	if len(definition) == 0 || !json.Valid(definition) {
		return nil, errors.New("gift package definition must be valid JSON")
	}
	return cloneJSON(definition), nil
}

func (store *Store) CreateGiftPackage(ctx context.Context, name string, definition json.RawMessage) (GiftPackage, error) {
	name, err := validateGiftName(name)
	if err != nil {
		return GiftPackage{}, err
	}
	definition, err = validateGiftDefinition(definition)
	if err != nil {
		return GiftPackage{}, err
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `
INSERT INTO gift_packages(name,definition_json,created_at,updated_at) VALUES(?,?,?,?)`,
		name, string(definition), now, now)
	if err != nil {
		return GiftPackage{}, fmt.Errorf("create gift package: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return GiftPackage{}, err
	}
	return store.GetGiftPackage(ctx, id)
}

func (store *Store) GetGiftPackage(ctx context.Context, id int64) (GiftPackage, error) {
	if id <= 0 {
		return GiftPackage{}, ErrNotFound
	}
	var packageValue GiftPackage
	var definition, created, updated string
	err := store.db.QueryRowContext(ctx, `
SELECT id,name,definition_json,created_at,updated_at FROM gift_packages WHERE id=?`, id).
		Scan(&packageValue.ID, &packageValue.Name, &definition, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return GiftPackage{}, ErrNotFound
	}
	if err != nil {
		return GiftPackage{}, err
	}
	packageValue.Definition = json.RawMessage(definition)
	packageValue.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return GiftPackage{}, err
	}
	packageValue.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return GiftPackage{}, err
	}
	return packageValue, nil
}

func (store *Store) UpdateGiftPackage(ctx context.Context, id int64, name string, definition json.RawMessage) (GiftPackage, error) {
	if id <= 0 {
		return GiftPackage{}, ErrNotFound
	}
	name, err := validateGiftName(name)
	if err != nil {
		return GiftPackage{}, err
	}
	definition, err = validateGiftDefinition(definition)
	if err != nil {
		return GiftPackage{}, err
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE gift_packages SET name=?,definition_json=?,updated_at=? WHERE id=?`,
		name, string(definition), store.timestamp(store.now()), id)
	if err != nil {
		return GiftPackage{}, fmt.Errorf("update gift package: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return GiftPackage{}, err
	}
	if affected == 0 {
		return GiftPackage{}, ErrNotFound
	}
	return store.GetGiftPackage(ctx, id)
}

func (store *Store) DeleteGiftPackage(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrNotFound
	}
	result, err := store.db.ExecContext(ctx, "DELETE FROM gift_packages WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete gift package: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGiftPackages returns all package definitions in newest-first order. It
// is intended for the small administrative catalog; runs and deliveries have
// cursor-based methods below because those tables can grow without a bound.
func (store *Store) ListGiftPackages(ctx context.Context) ([]GiftPackage, error) {
	rows, err := store.db.QueryContext(ctx, `
SELECT id,name,definition_json,created_at,updated_at
FROM gift_packages ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	packages := make([]GiftPackage, 0)
	for rows.Next() {
		var packageValue GiftPackage
		var definition, created, updated string
		if err := rows.Scan(&packageValue.ID, &packageValue.Name, &definition, &created, &updated); err != nil {
			return nil, err
		}
		packageValue.Definition = json.RawMessage(definition)
		packageValue.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, err
		}
		packageValue.UpdatedAt, err = parseTimestamp(updated)
		if err != nil {
			return nil, err
		}
		packages = append(packages, packageValue)
	}
	return packages, rows.Err()
}

type giftSnapshot struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
}

func makeGiftSnapshot(packageValue GiftPackage) (json.RawMessage, error) {
	return json.Marshal(giftSnapshot{Name: packageValue.Name, Definition: packageValue.Definition})
}

func decodeGiftSnapshot(raw string) (string, json.RawMessage) {
	var snapshot giftSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return "", json.RawMessage(raw)
	}
	return snapshot.Name, cloneJSON(snapshot.Definition)
}

func scanGiftRun(row interface{ Scan(...any) error }) (GiftRun, error) {
	var run GiftRun
	var packageID sql.NullInt64
	var adminID sql.NullInt64
	var snapshot, created, updated string
	if err := row.Scan(&run.ID, &packageID, &snapshot, &run.TargetScope, &adminID, &run.Status, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GiftRun{}, ErrNotFound
		}
		return GiftRun{}, err
	}
	if packageID.Valid {
		run.PackageID = packageID.Int64
	}
	if adminID.Valid {
		run.AdminID = adminID.Int64
	}
	run.PackageSnapshot = json.RawMessage(snapshot)
	run.PackageName, run.Definition = decodeGiftSnapshot(snapshot)
	var err error
	run.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return GiftRun{}, err
	}
	run.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return GiftRun{}, err
	}
	return run, nil
}

func (store *Store) GetGiftRun(ctx context.Context, id int64) (GiftRun, error) {
	if id <= 0 {
		return GiftRun{}, ErrNotFound
	}
	return scanGiftRun(store.db.QueryRowContext(ctx, `
SELECT id,package_id,package_snapshot,target_scope,admin_user_id,status,created_at,updated_at
FROM gift_runs WHERE id=?`, id))
}

func parseGiftCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid gift cursor")
	}
	return value, nil
}

func encodeGiftCursor(id int64) string { return strconv.FormatInt(id, 10) }

func normalizeGiftPage(limit int) (int, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return 0, errors.New("gift page size must be between 1 and 500")
	}
	return limit, nil
}

func (store *Store) ListGiftRuns(ctx context.Context, cursor string, limit int) (GiftRunPage, error) {
	after, err := parseGiftCursor(cursor)
	if err != nil {
		return GiftRunPage{}, err
	}
	limit, err = normalizeGiftPage(limit)
	if err != nil {
		return GiftRunPage{}, err
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT id,package_id,package_snapshot,target_scope,admin_user_id,status,created_at,updated_at
FROM gift_runs WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return GiftRunPage{}, err
	}
	defer rows.Close()
	result := GiftRunPage{Runs: make([]GiftRun, 0, limit)}
	for rows.Next() {
		run, scanErr := scanGiftRun(rows)
		if scanErr != nil {
			return GiftRunPage{}, scanErr
		}
		result.Runs = append(result.Runs, run)
	}
	if err := rows.Err(); err != nil {
		return GiftRunPage{}, err
	}
	if len(result.Runs) > limit {
		result.NextCursor = encodeGiftCursor(result.Runs[limit-1].ID)
		result.Runs = result.Runs[:limit]
	}
	return result, nil
}

// CreateGiftRun snapshots a package and materializes all concrete targets in
// one transaction. A single target scope must contain exactly one target;
// an all scope may contain zero targets when there are no characters yet.
func (store *Store) CreateGiftRun(ctx context.Context, packageID int64, targetScope string, adminID int64, targets []GiftTarget) (GiftRun, error) {
	if packageID <= 0 {
		return GiftRun{}, ErrNotFound
	}
	if targetScope != GiftTargetSingle && targetScope != GiftTargetAll {
		return GiftRun{}, errors.New("invalid gift target scope")
	}
	if targetScope == GiftTargetSingle && len(targets) != 1 {
		return GiftRun{}, errors.New("single gift run requires exactly one target")
	}
	for _, target := range targets {
		if target.AccountID <= 0 || target.CharacterSlot < 0 || target.CharacterSlot > 1 {
			return GiftRun{}, errors.New("invalid gift target")
		}
		if len(target.CharacterName) > 256 {
			return GiftRun{}, errors.New("gift character name is too long")
		}
		if len(target.SkipReason) > 4096 {
			return GiftRun{}, errors.New("gift target skip reason is too long")
		}
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GiftRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var packageValue GiftPackage
	var definition, created, updated string
	err = tx.QueryRowContext(ctx, `
SELECT id,name,definition_json,created_at,updated_at FROM gift_packages WHERE id=?`, packageID).
		Scan(&packageValue.ID, &packageValue.Name, &definition, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return GiftRun{}, ErrNotFound
	}
	if err != nil {
		return GiftRun{}, err
	}
	packageValue.Definition = json.RawMessage(definition)
	snapshot, err := makeGiftSnapshot(packageValue)
	if err != nil {
		return GiftRun{}, err
	}
	now := store.timestamp(store.now())
	var adminValue any
	if adminID > 0 {
		adminValue = adminID
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO gift_runs(package_id,package_snapshot,target_scope,admin_user_id,status,created_at,updated_at)
VALUES(?,?,?,?,?,?,?)`, packageID, string(snapshot), targetScope, adminValue, GiftRunPending, now, now)
	if err != nil {
		return GiftRun{}, fmt.Errorf("create gift run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return GiftRun{}, err
	}
	hasSkipped := false
	for _, target := range targets {
		status := GiftDeliveryPending
		var deliveryError any
		if strings.TrimSpace(target.SkipReason) != "" {
			status = GiftDeliverySkipped
			hasSkipped = true
			deliveryError = target.SkipReason
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO gift_deliveries(run_id,account_id,character_slot,character_name,status,error,updated_at)
VALUES(?,?,?,?,?,?,?)`, runID, target.AccountID, target.CharacterSlot, target.CharacterName, status, deliveryError, now); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return GiftRun{}, fmt.Errorf("duplicate gift target: %w", err)
			}
			return GiftRun{}, fmt.Errorf("create gift target: %w", err)
		}
	}
	if hasSkipped || len(targets) == 0 {
		if err := refreshGiftRunStatus(ctx, tx, runID, now); err != nil {
			return GiftRun{}, fmt.Errorf("set gift run status: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return GiftRun{}, err
	}
	return store.GetGiftRun(ctx, runID)
}

func scanGiftDelivery(row interface{ Scan(...any) error }) (GiftDelivery, error) {
	var delivery GiftDelivery
	var payload, result sql.NullString
	var deliveryError sql.NullString
	var updated string
	if err := row.Scan(&delivery.ID, &delivery.RunID, &delivery.AccountID, &delivery.CharacterSlot,
		&delivery.CharacterName, &delivery.Status, &payload, &result, &deliveryError, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GiftDelivery{}, ErrNotFound
		}
		return GiftDelivery{}, err
	}
	if payload.Valid && payload.String != "" {
		delivery.Payload = json.RawMessage(payload.String)
	}
	if result.Valid && result.String != "" {
		delivery.Result = json.RawMessage(result.String)
	}
	if deliveryError.Valid {
		delivery.Error = deliveryError.String
	}
	var err error
	delivery.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return GiftDelivery{}, err
	}
	return delivery, nil
}

const giftDeliverySelect = `
SELECT id,run_id,account_id,character_slot,character_name,status,payload,result,error,updated_at
FROM gift_deliveries`

func (store *Store) GetGiftDelivery(ctx context.Context, id int64) (GiftDelivery, error) {
	if id <= 0 {
		return GiftDelivery{}, ErrNotFound
	}
	return scanGiftDelivery(store.db.QueryRowContext(ctx, giftDeliverySelect+" WHERE id=?", id))
}

func (store *Store) ListGiftDeliveries(ctx context.Context, runID int64, cursor string, limit int) (GiftDeliveryPage, error) {
	if runID <= 0 {
		return GiftDeliveryPage{}, ErrNotFound
	}
	after, err := parseGiftCursor(cursor)
	if err != nil {
		return GiftDeliveryPage{}, err
	}
	limit, err = normalizeGiftPage(limit)
	if err != nil {
		return GiftDeliveryPage{}, err
	}
	rows, err := store.db.QueryContext(ctx, giftDeliverySelect+" WHERE run_id=? AND id>? ORDER BY id LIMIT ?", runID, after, limit+1)
	if err != nil {
		return GiftDeliveryPage{}, err
	}
	defer rows.Close()
	result := GiftDeliveryPage{Deliveries: make([]GiftDelivery, 0, limit)}
	for rows.Next() {
		delivery, scanErr := scanGiftDelivery(rows)
		if scanErr != nil {
			return GiftDeliveryPage{}, scanErr
		}
		result.Deliveries = append(result.Deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return GiftDeliveryPage{}, err
	}
	if len(result.Deliveries) > limit {
		result.NextCursor = encodeGiftCursor(result.Deliveries[limit-1].ID)
		result.Deliveries = result.Deliveries[:limit]
	}
	return result, nil
}

func (store *Store) GetGiftRunDetails(ctx context.Context, runID int64, cursor string, limit int) (GiftRunDetails, error) {
	run, err := store.GetGiftRun(ctx, runID)
	if err != nil {
		return GiftRunDetails{}, err
	}
	deliveries, err := store.ListGiftDeliveries(ctx, runID, cursor, limit)
	if err != nil {
		return GiftRunDetails{}, err
	}
	return GiftRunDetails{Run: run, Deliveries: deliveries.Deliveries, NextCursor: deliveries.NextCursor}, nil
}

func validateGiftTerminalStatus(status string) bool {
	switch status {
	case GiftDeliveryApplied, GiftDeliverySkipped, GiftDeliveryFailed, GiftDeliveryUncertain:
		return true
	default:
		return false
	}
}

func normalizeJSONField(value json.RawMessage) (any, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid(value) {
		return nil, errors.New("gift delivery field must be valid JSON")
	}
	return string(value), nil
}

func sameJSONValue(a sql.NullString, b json.RawMessage) bool {
	if len(b) == 0 {
		return !a.Valid || a.String == ""
	}
	return a.Valid && a.String == string(b)
}

func (store *Store) ClaimGiftDelivery(ctx context.Context, id int64) (GiftDelivery, error) {
	if id <= 0 {
		return GiftDelivery{}, ErrNotFound
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GiftDelivery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, "SELECT status FROM gift_deliveries WHERE id=?", id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return GiftDelivery{}, ErrNotFound
	}
	if err != nil {
		return GiftDelivery{}, err
	}
	if status != GiftDeliveryPending {
		// The transaction still owns the store's sole SQLite connection. Scan
		// through tx, then roll back before returning; calling a Store method
		// here would wait for this transaction and deadlock.
		delivery, scanErr := scanGiftDelivery(tx.QueryRowContext(ctx, giftDeliverySelect+" WHERE id=?", id))
		_ = tx.Rollback()
		if scanErr != nil {
			return GiftDelivery{}, scanErr
		}
		return delivery, ErrGiftDeliveryNotPending
	}
	now := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `UPDATE gift_deliveries SET status=?,updated_at=? WHERE id=? AND status=?`, GiftDeliverySending, now, id, GiftDeliveryPending)
	if err != nil {
		return GiftDelivery{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gift_runs SET status=?,updated_at=? WHERE id=(SELECT run_id FROM gift_deliveries WHERE id=?) AND status=?`, GiftRunRunning, now, id, GiftRunPending); err != nil {
		return GiftDelivery{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return GiftDelivery{}, err
	}
	if affected != 1 {
		return GiftDelivery{}, ErrGiftDeliveryNotPending
	}
	if err := tx.Commit(); err != nil {
		return GiftDelivery{}, err
	}
	return store.GetGiftDelivery(ctx, id)
}

// ClaimNextGiftDelivery atomically claims the oldest pending target across
// all runs. It returns (zero, false, nil) when the queue is empty. A pending
// target is never reclaimed by timeout: only RecoverInterruptedGiftDeliveries
// moves an in-flight target to uncertain for manual review.
func (store *Store) ClaimNextGiftDelivery(ctx context.Context) (GiftDelivery, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GiftDelivery{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM gift_deliveries WHERE status=? ORDER BY id LIMIT 1", GiftDeliveryPending).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return GiftDelivery{}, false, nil
	}
	if err != nil {
		return GiftDelivery{}, false, err
	}
	now := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `UPDATE gift_deliveries SET status=?,updated_at=? WHERE id=? AND status=?`, GiftDeliverySending, now, id, GiftDeliveryPending)
	if err != nil {
		return GiftDelivery{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return GiftDelivery{}, false, err
	}
	if affected != 1 {
		return GiftDelivery{}, false, ErrGiftDeliveryNotPending
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gift_runs SET status=?,updated_at=? WHERE id=(SELECT run_id FROM gift_deliveries WHERE id=?) AND status=?`, GiftRunRunning, now, id, GiftRunPending); err != nil {
		return GiftDelivery{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return GiftDelivery{}, false, err
	}
	delivery, err := store.GetGiftDelivery(ctx, id)
	return delivery, true, err
}

// RecoverInterruptedGiftDeliveries closes the crash window for the admin
// worker. A delivery left in sending is evidence that game state may already
// have changed, so it becomes uncertain and can only be investigated by an
// operator. Pending work remains pending and is safe to claim.
func (store *Store) RecoverInterruptedGiftDeliveries(ctx context.Context) (int64, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `
UPDATE gift_deliveries
SET status=?, error=COALESCE(NULLIF(error,''), ?), updated_at=?
WHERE status=?`, GiftDeliveryUncertain,
		"administrator restarted while delivery was sending; verify game state before any manual action",
		now, GiftDeliverySending)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE gift_runs SET status=?,updated_at=?
WHERE id IN (SELECT DISTINCT run_id FROM gift_deliveries WHERE status=?)`, GiftRunUncertain, now, GiftDeliveryUncertain); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// CompleteGiftDelivery records one authoritative result. A delivery can only
// move from sending to a terminal state. Repeating the exact terminal update
// returns the existing row; changing a terminal result is rejected.
func (store *Store) CompleteGiftDelivery(ctx context.Context, id int64, status string, payload, result json.RawMessage, deliveryError string) (GiftDelivery, error) {
	if id <= 0 {
		return GiftDelivery{}, ErrNotFound
	}
	if !validateGiftTerminalStatus(status) {
		return GiftDelivery{}, errors.New("invalid gift delivery terminal status")
	}
	payloadValue, err := normalizeJSONField(payload)
	if err != nil {
		return GiftDelivery{}, err
	}
	resultValue, err := normalizeJSONField(result)
	if err != nil {
		return GiftDelivery{}, err
	}
	if len(deliveryError) > 4096 {
		return GiftDelivery{}, errors.New("gift delivery error is too long")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return GiftDelivery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current GiftDelivery
	var currentPayload, currentResult sql.NullString
	var currentError sql.NullString
	var currentUpdated string
	err = tx.QueryRowContext(ctx, `
SELECT id,run_id,account_id,character_slot,character_name,status,payload,result,error,updated_at
FROM gift_deliveries WHERE id=?`, id).Scan(&current.ID, &current.RunID, &current.AccountID,
		&current.CharacterSlot, &current.CharacterName, &current.Status, &currentPayload,
		&currentResult, &currentError, &currentUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return GiftDelivery{}, ErrNotFound
	}
	if err != nil {
		return GiftDelivery{}, err
	}
	if currentError.Valid {
		current.Error = currentError.String
	}
	if current.Status != GiftDeliverySending {
		if current.Status == status && sameJSONValue(currentPayload, payload) && sameJSONValue(currentResult, result) && current.Error == deliveryError {
			current.Payload = json.RawMessage(currentPayload.String)
			current.Result = json.RawMessage(currentResult.String)
			current.UpdatedAt, _ = parseTimestamp(currentUpdated)
			_ = tx.Rollback()
			return current, nil
		}
		return current, ErrGiftDeliveryFinalized
	}
	now := store.timestamp(store.now())
	if _, err := tx.ExecContext(ctx, `UPDATE gift_deliveries SET status=?,payload=?,result=?,error=?,updated_at=? WHERE id=? AND status=?`,
		status, payloadValue, resultValue, nullIfEmpty(deliveryError), now, id, GiftDeliverySending); err != nil {
		return GiftDelivery{}, err
	}
	if err := refreshGiftRunStatus(ctx, tx, current.RunID, now); err != nil {
		return GiftDelivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return GiftDelivery{}, err
	}
	return store.GetGiftDelivery(ctx, id)
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func refreshGiftRunStatus(ctx context.Context, tx *sql.Tx, runID int64, now string) error {
	var total, pending, sending, uncertain, failed int
	if err := tx.QueryRowContext(ctx, `
	SELECT COUNT(*),
	       COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END), 0),
	       COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END), 0),
	       COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END), 0),
	       COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END), 0)
	FROM gift_deliveries WHERE run_id=?`, GiftDeliveryPending, GiftDeliverySending, GiftDeliveryUncertain, GiftDeliveryFailed, runID).
		Scan(&total, &pending, &sending, &uncertain, &failed); err != nil {
		return err
	}
	status := GiftRunRunning
	if total == 0 {
		status = GiftRunCompleted
	} else if pending > 0 || sending > 0 {
		status = GiftRunRunning
	} else if uncertain > 0 {
		status = GiftRunUncertain
	} else if failed > 0 {
		status = GiftRunFailed
	} else {
		status = GiftRunCompleted
	}
	_, err := tx.ExecContext(ctx, "UPDATE gift_runs SET status=?,updated_at=? WHERE id=?", status, now, runID)
	return err
}

// ListAccountsPage intentionally has no status predicate: legacy broadcast
// runs must include both active and disabled accounts, while the caller can
// decide whether a disabled account's character is currently deliverable.
type AccountPage struct {
	Accounts   []Account `json:"accounts"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

func (store *Store) ListAccountsPage(ctx context.Context, cursor string, limit int) (AccountPage, error) {
	after, err := parseGiftCursor(cursor)
	if err != nil {
		return AccountPage{}, err
	}
	limit, err = normalizeGiftPage(limit)
	if err != nil {
		return AccountPage{}, err
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT id,username,status,must_change_password,created_at,updated_at,last_login_at,failed_attempts,locked_until
FROM accounts WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return AccountPage{}, err
	}
	defer rows.Close()
	page := AccountPage{Accounts: make([]Account, 0, limit)}
	for rows.Next() {
		account, scanErr := scanAccount(rows)
		if scanErr != nil {
			return AccountPage{}, scanErr
		}
		page.Accounts = append(page.Accounts, account)
	}
	if err := rows.Err(); err != nil {
		return AccountPage{}, err
	}
	if len(page.Accounts) > limit {
		page.NextCursor = encodeGiftCursor(page.Accounts[limit-1].ID)
		page.Accounts = page.Accounts[:limit]
	}
	return page, nil
}
