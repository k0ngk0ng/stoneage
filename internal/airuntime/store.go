package airuntime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the durable state boundary for AI profiles.  The schema contains
// references to game accounts and characters only; there is deliberately no
// game-password column or password write method.
type Store struct {
	db          *sql.DB
	path        string
	secretStore *SecretStore
	mu          sync.Mutex
	now         func() time.Time
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is empty", ErrInvalidProvider)
	}
	if path != ":memory:" && path != "file::memory:?cache=shared" {
		directory := filepath.Dir(path)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create AI database directory: %w", err)
		}
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("airuntime: database must not be a symbolic link")
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("airuntime: database is not a regular file")
			}
			if err := os.Chmod(path, 0o600); err != nil {
				return nil, fmt.Errorf("protect AI database: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect AI database: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open AI database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: path, now: time.Now}
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure AI database (%s): %w", pragma, err)
		}
	}
	if path != ":memory:" && path != "file::memory:?cache=shared" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("protect AI database: %w", err)
		}
	}
	return store, nil
}

// OpenStore is the convenient fully initialized constructor.  Open remains
// available for callers that want to control migration timing.
func OpenStore(path string) (*Store, error) {
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// OpenWithSecrets is used by server-side management code when model keys are
// kept in a private directory.  The directory is never accepted from a
// browser request; callers should configure it at process startup.
func OpenWithSecrets(path, secretDir string) (*Store, error) {
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	secrets, err := NewSecretStore(secretDir)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	store.secretStore = secrets
	return store, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) DB() *sql.DB { return store.db }

func (store *Store) Path() string { return store.path }

// Migrate is idempotent and safe to call by every process using the store.
func (store *Store) Migrate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	const schema = `
CREATE TABLE IF NOT EXISTS ai_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_profiles (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  account_username TEXT NOT NULL DEFAULT '',
  character_id TEXT NOT NULL,
  character_name TEXT NOT NULL DEFAULT '',
  model_config_id TEXT NOT NULL DEFAULT '',
  personality_json TEXT NOT NULL,
  goal_json TEXT NOT NULL,
  skills_json TEXT NOT NULL,
  unlimited_funds INTEGER NOT NULL CHECK (unlimited_funds IN (0,1)),
  daily_token_budget INTEGER NOT NULL CHECK (daily_token_budget >= 0),
  external_spend_limit INTEGER NOT NULL CHECK (external_spend_limit >= 0),
  status TEXT NOT NULL CHECK (status IN ('active','paused','stopped','deleted')),
  version INTEGER NOT NULL CHECK (version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_profiles_status_idx ON ai_profiles(status);
CREATE TABLE IF NOT EXISTS ai_model_configs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  backend TEXT NOT NULL CHECK (backend = 'codex'),
  provider TEXT NOT NULL,
  base_url TEXT NOT NULL,
  model TEXT NOT NULL,
  wire_api TEXT NOT NULL DEFAULT 'responses' CHECK (wire_api = 'responses'),
  reasoning_effort TEXT NOT NULL CHECK (reasoning_effort IN ('','none','minimal','low','medium','high','xhigh','max')),
  timeout_ns INTEGER NOT NULL CHECK (timeout_ns > 0),
  max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens > 0),
  daily_token_budget INTEGER NOT NULL CHECK (daily_token_budget >= 0),
  has_key INTEGER NOT NULL DEFAULT 0 CHECK (has_key IN (0,1)),
  version INTEGER NOT NULL CHECK (version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_model_configs_name_idx ON ai_model_configs(name);
CREATE TABLE IF NOT EXISTS ai_runtime_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  profile_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  actor TEXT NOT NULL,
  detail_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_events_profile_created_idx ON ai_events(profile_id, created_at DESC);
CREATE TABLE IF NOT EXISTS ai_memories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  content_json TEXT NOT NULL,
  source_event_id INTEGER NOT NULL,
  confirmed INTEGER NOT NULL CHECK (confirmed = 1),
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_memories_profile_created_idx ON ai_memories(profile_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ai_memories_subject_idx ON ai_memories(profile_id, subject, confirmed, id DESC);
CREATE TABLE IF NOT EXISTS ai_agent_notes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  note_key TEXT NOT NULL,
  note_text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(profile_id, note_key)
);
CREATE INDEX IF NOT EXISTS ai_agent_notes_profile_updated_idx
  ON ai_agent_notes(profile_id, updated_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS ai_observation_dedupe (
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  event_key TEXT NOT NULL,
  event_id INTEGER,
  memory_id INTEGER,
  created_at TEXT NOT NULL,
  PRIMARY KEY(profile_id, event_key)
);
CREATE TABLE IF NOT EXISTS ai_checkpoints (
  profile_id TEXT PRIMARY KEY REFERENCES ai_profiles(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version >= 1),
  state_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_token_usage (
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  usage_date TEXT NOT NULL,
  charged_tokens INTEGER NOT NULL DEFAULT 0 CHECK (charged_tokens >= 0),
  reserved_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reserved_tokens >= 0),
  input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
  over_budget_attempts INTEGER NOT NULL DEFAULT 0 CHECK (over_budget_attempts >= 0),
  PRIMARY KEY(profile_id, usage_date)
);
CREATE TABLE IF NOT EXISTS ai_token_attempts (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  usage_date TEXT NOT NULL,
  charge_tokens INTEGER NOT NULL CHECK (charge_tokens > 0),
  state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','prepared','dispatched','recorded','unknown','settled')),
  prompt TEXT NOT NULL DEFAULT '',
  resume INTEGER NOT NULL DEFAULT 0 CHECK (resume IN (0,1)),
  thread_id TEXT NOT NULL DEFAULT '',
  outcome_json TEXT,
  error_text TEXT NOT NULL DEFAULT '',
  settled INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0,1)),
  failed INTEGER NOT NULL DEFAULT 0 CHECK (failed IN (0,1)),
  input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  settled_at TEXT
);
CREATE TABLE IF NOT EXISTS ai_unknown_attempt_reviews (
  attempt_id TEXT PRIMARY KEY REFERENCES ai_token_attempts(id) ON DELETE CASCADE,
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  actor TEXT NOT NULL,
  reason TEXT NOT NULL,
  reviewed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_token_attempts_profile_date_idx ON ai_token_attempts(profile_id, usage_date);
CREATE INDEX IF NOT EXISTS ai_token_attempts_pending_idx ON ai_token_attempts(profile_id, settled, created_at, id);
CREATE TABLE IF NOT EXISTS ai_schedules (
  id TEXT PRIMARY KEY,
  profile_id TEXT NOT NULL REFERENCES ai_profiles(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  prompt TEXT NOT NULL,
  run_at TEXT NOT NULL,
  repeat_interval_ns INTEGER NOT NULL DEFAULT 0 CHECK (repeat_interval_ns >= 0),
  status TEXT NOT NULL CHECK (status IN ('pending','delivering','delivered','cancelled')),
  claim_token TEXT NOT NULL DEFAULT '',
  claim_until TEXT NOT NULL DEFAULT '',
  delivery_attempt_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT '',
  occurrences INTEGER NOT NULL DEFAULT 0 CHECK (occurrences >= 0),
  last_claim_token TEXT NOT NULL DEFAULT '',
  last_outcome_json TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT NOT NULL DEFAULT '',
  cancelled_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ai_schedules_profile_run_idx ON ai_schedules(profile_id, status, run_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS ai_schedules_profile_idempotency_idx
  ON ai_schedules(profile_id, idempotency_key) WHERE idempotency_key <> '';
`
	if _, err := store.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create AI schema: %w", err)
	}
	// Profiles created by early development snapshots did not have a model
	// binding. Keep that upgrade additive so existing profiles remain usable
	// until an administrator assigns a ModelConfigID.
	if err := store.ensureColumn(ctx, "ai_profiles", "model_config_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("upgrade AI profile schema: %w", err)
	}
	if err := store.ensureColumn(ctx, "ai_model_configs", "backend", "TEXT NOT NULL DEFAULT 'codex'"); err != nil {
		return fmt.Errorf("upgrade AI model config schema: %w", err)
	}
	if err := store.ensureColumn(ctx, "ai_model_configs", "reasoning_effort", "TEXT NOT NULL DEFAULT 'high'"); err != nil {
		return fmt.Errorf("upgrade AI model config schema: %w", err)
	}
	if err := store.ensureColumn(ctx, "ai_model_configs", "wire_api", "TEXT NOT NULL DEFAULT 'responses'"); err != nil {
		return fmt.Errorf("upgrade AI model config schema: %w", err)
	}
	// Token attempts originally only contained accounting columns. Keep the
	// upgrade additive so a restart can reconcile rows created by an older
	// process instead of opening a second model turn.
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "state", definition: "TEXT NOT NULL DEFAULT 'reserved'"},
		{name: "prompt", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "resume", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "thread_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "outcome_json", definition: "TEXT"},
		{name: "error_text", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "updated_at", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := store.ensureColumn(ctx, "ai_token_attempts", column.name, column.definition); err != nil {
			return fmt.Errorf("upgrade AI token attempt schema (%s): %w", column.name, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE ai_token_attempts
        SET state=CASE WHEN settled=1 THEN 'settled' ELSE 'reserved' END,
            updated_at=CASE WHEN updated_at='' THEN created_at ELSE updated_at END
        WHERE state='' OR state IS NULL OR updated_at=''`); err != nil {
		return fmt.Errorf("normalize AI token attempt schema: %w", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"CREATE INDEX IF NOT EXISTS ai_token_attempts_pending_idx ON ai_token_attempts(profile_id, settled, created_at, id)"); err != nil {
		return fmt.Errorf("index AI token attempts: %w", err)
	}
	if err := store.migrateModelConfigReasoning(ctx); err != nil {
		return fmt.Errorf("upgrade AI model reasoning schema: %w", err)
	}
	if err := store.ensureColumn(ctx, "ai_schedules", "delivery_attempt_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("upgrade AI schedule schema: %w", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO ai_schema_migrations(version, applied_at) VALUES(?, ?)",
		1, store.timestamp(store.now())); err != nil {
		return fmt.Errorf("record AI schema migration: %w", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO ai_schema_migrations(version, applied_at) VALUES(?, ?)",
		3, store.timestamp(store.now())); err != nil {
		return fmt.Errorf("record AI schedule schema migration: %w", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO ai_schema_migrations(version, applied_at) VALUES(?, ?)",
		4, store.timestamp(store.now())); err != nil {
		return fmt.Errorf("record AI agent note schema migration: %w", err)
	}
	return nil
}

// migrateModelConfigReasoning rebuilds the model table when it was created by
// an older snapshot whose CHECK constraint only allowed DeepSeek's three
// reasoning levels. SQLite cannot alter a CHECK constraint in place, so the
// table is copied transactionally and all model rows, versions and key flags
// are preserved. Values outside the new generic set are normalized to empty,
// which means "provider default" and keeps a malformed legacy row readable.
func (store *Store) migrateModelConfigReasoning(ctx context.Context) error {
	var definition string
	err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='ai_model_configs'`).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	lower := strings.ToLower(definition)
	if strings.Contains(lower, "reasoning_effort in ('','none','minimal','low','medium','high','xhigh','max')") {
		_, err := store.db.ExecContext(ctx,
			"INSERT OR IGNORE INTO ai_schema_migrations(version, applied_at) VALUES(?, ?)",
			2, store.timestamp(store.now()))
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS ai_model_configs_name_idx"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE ai_model_configs RENAME TO ai_model_configs_legacy"); err != nil {
		return err
	}
	const table = `
CREATE TABLE ai_model_configs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  backend TEXT NOT NULL CHECK (backend = 'codex'),
  provider TEXT NOT NULL,
  base_url TEXT NOT NULL,
  model TEXT NOT NULL,
  wire_api TEXT NOT NULL DEFAULT 'responses' CHECK (wire_api = 'responses'),
  reasoning_effort TEXT NOT NULL CHECK (reasoning_effort IN ('','none','minimal','low','medium','high','xhigh','max')),
  timeout_ns INTEGER NOT NULL CHECK (timeout_ns > 0),
  max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens > 0),
  daily_token_budget INTEGER NOT NULL CHECK (daily_token_budget >= 0),
  has_key INTEGER NOT NULL DEFAULT 0 CHECK (has_key IN (0,1)),
  version INTEGER NOT NULL CHECK (version >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`
	if _, err := tx.ExecContext(ctx, table); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_model_configs
  (id, name, backend, provider, base_url, model, wire_api, reasoning_effort, timeout_ns,
   max_output_tokens, daily_token_budget, has_key, version, created_at, updated_at)
  SELECT id, name, backend, provider, base_url, model,
    COALESCE(NULLIF(wire_api, ''), 'responses'),
    CASE WHEN reasoning_effort IN ('','none','minimal','low','medium','high','xhigh','max')
      THEN reasoning_effort ELSE '' END,
    timeout_ns, max_output_tokens, daily_token_budget, has_key, version, created_at, updated_at
  FROM ai_model_configs_legacy`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE ai_model_configs_legacy"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CREATE INDEX ai_model_configs_name_idx ON ai_model_configs(name)"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO ai_schema_migrations(version, applied_at) VALUES(?, ?)",
		2, store.timestamp(store.now()))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := store.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// table, column, and definition are package constants supplied by Migrate;
	// avoid interpolating values from requests here.
	_, err = store.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

func (store *Store) CreateProfile(ctx context.Context, profile Profile) (Profile, error) {
	return store.CreateProfileAs(ctx, profile, "system")
}

func (store *Store) CreateProfileAs(ctx context.Context, profile Profile, actor string) (Profile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile.ID == "" {
		profile.ID = newID("profile")
	}
	if profile.Status == "" {
		profile.Status = ProfileStatusStopped
	}
	if profile.Version == 0 {
		profile.Version = 1
	}
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = store.now().UTC()
	}
	if profile.UpdatedAt.IsZero() {
		profile.UpdatedAt = profile.CreatedAt
	}
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	profile.Skills = cloneSkills(profile.Skills)
	personality, err := json.Marshal(profile.Personality)
	if err != nil {
		return Profile{}, fmt.Errorf("marshal personality: %w", err)
	}
	goal, err := json.Marshal(profile.Goal)
	if err != nil {
		return Profile{}, fmt.Errorf("marshal goal: %w", err)
	}
	skills, err := json.Marshal(cloneSkills(profile.Skills))
	if err != nil {
		return Profile{}, fmt.Errorf("marshal skills: %w", err)
	}
	if actor = cleanActor(actor); actor == "" {
		actor = "system"
	}
	detail, _ := json.Marshal(map[string]any{"version": profile.Version})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Profile{}, fmt.Errorf("begin profile create: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_profiles
		(id, account_id, account_username, character_id, character_name, model_config_id,
		 personality_json, goal_json, skills_json, unlimited_funds,
		 daily_token_budget, external_spend_limit, status, version, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.ID, profile.Account.ID, profile.Account.Username,
		profile.Character.ID, profile.Character.Name, profile.ModelConfigID, personality, goal, skills,
		boolInt(profile.UnlimitedFunds), profile.DailyTokenBudget,
		profile.ExternalSpendLimit, profile.Status, profile.Version,
		store.timestamp(profile.CreatedAt), store.timestamp(profile.UpdatedAt))
	if err != nil {
		return Profile{}, fmt.Errorf("create AI profile: %w", err)
	}
	if _, err := store.recordEventTx(ctx, tx, profile.ID, EventProfileCreated, actor, detail); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return Profile{}, fmt.Errorf("commit AI profile create: %w", err)
	}
	return cloneProfile(profile), nil
}

func (store *Store) GetProfile(ctx context.Context, id string) (Profile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	row := store.db.QueryRowContext(ctx, `SELECT id, account_id, account_username,
		character_id, character_name, model_config_id, personality_json, goal_json, skills_json,
        unlimited_funds, daily_token_budget, external_spend_limit, status,
        version, created_at, updated_at FROM ai_profiles WHERE id = ?`, id)
	return scanProfile(row)
}

func (store *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, account_id, account_username,
		character_id, character_name, model_config_id, personality_json, goal_json, skills_json,
        unlimited_funds, daily_token_budget, external_spend_limit, status,
        version, created_at, updated_at FROM ai_profiles ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list AI profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		profile, err := scanProfileRow(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AI profiles: %w", err)
	}
	return profiles, nil
}

func (store *Store) UpdateProfileCAS(ctx context.Context, id string, expectedVersion int64, patch ProfilePatch) (Profile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expectedVersion < 1 {
		return Profile{}, fmt.Errorf("%w: version must be positive", ErrConflict)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Profile{}, fmt.Errorf("begin profile update: %w", err)
	}
	defer tx.Rollback()
	profile, err := scanProfileTx(ctx, tx, id)
	if err != nil {
		return Profile{}, err
	}
	if profile.Version != expectedVersion {
		return Profile{}, ErrConflict
	}
	changed := make([]string, 0, 8)
	if patch.Account != nil {
		if strings.TrimSpace(patch.Account.ID) == "" {
			return Profile{}, fmt.Errorf("%w: account id is required", ErrInvalidProfile)
		}
		profile.Account = *patch.Account
		changed = append(changed, "account")
	}
	if patch.Character != nil {
		if strings.TrimSpace(patch.Character.ID) == "" {
			return Profile{}, fmt.Errorf("%w: character id is required", ErrInvalidProfile)
		}
		profile.Character = *patch.Character
		changed = append(changed, "character")
	}
	if patch.ModelConfigID != nil {
		profile.ModelConfigID = strings.TrimSpace(*patch.ModelConfigID)
		changed = append(changed, "model_config_id")
	}
	if patch.Personality != nil {
		profile.Personality = *patch.Personality
		changed = append(changed, "personality")
	}
	if patch.Goal != nil {
		profile.Goal = *patch.Goal
		changed = append(changed, "goal")
	}
	if patch.Skills != nil {
		profile.Skills = cloneSkills(*patch.Skills)
		changed = append(changed, "skills")
	}
	if patch.UnlimitedFunds != nil {
		profile.UnlimitedFunds = *patch.UnlimitedFunds
		changed = append(changed, "unlimited_funds")
	}
	if patch.DailyTokenBudget != nil {
		profile.DailyTokenBudget = *patch.DailyTokenBudget
		changed = append(changed, "daily_token_budget")
	}
	if patch.ExternalSpendLimit != nil {
		profile.ExternalSpendLimit = *patch.ExternalSpendLimit
		changed = append(changed, "external_spend_limit")
	}
	if patch.Status != nil {
		profile.Status = *patch.Status
		changed = append(changed, "status")
	}
	if len(changed) == 0 {
		return profile, nil
	}
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	personality, _ := json.Marshal(profile.Personality)
	goal, _ := json.Marshal(profile.Goal)
	skills, _ := json.Marshal(profile.Skills)
	profile.Version++
	profile.UpdatedAt = store.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE ai_profiles SET account_id=?, account_username=?,
		character_id=?, character_name=?, model_config_id=?, personality_json=?, goal_json=?, skills_json=?,
		unlimited_funds=?, daily_token_budget=?, external_spend_limit=?, status=?,
		version=?, updated_at=? WHERE id=? AND version=?`,
		profile.Account.ID, profile.Account.Username, profile.Character.ID, profile.Character.Name,
		profile.ModelConfigID, personality, goal, skills, boolInt(profile.UnlimitedFunds), profile.DailyTokenBudget,
		profile.ExternalSpendLimit, profile.Status, profile.Version, store.timestamp(profile.UpdatedAt),
		id, expectedVersion)
	if err != nil {
		return Profile{}, fmt.Errorf("update AI profile: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Profile{}, ErrConflict
	}
	detail, _ := json.Marshal(map[string]any{"version": profile.Version, "changed": changed})
	actor := cleanActor(patch.Actor)
	if actor == "" {
		actor = "system"
	}
	if _, err := store.recordEventTx(ctx, tx, id, EventProfileUpdated, actor, detail); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(); err != nil {
		return Profile{}, fmt.Errorf("commit AI profile update: %w", err)
	}
	return cloneProfile(profile), nil
}

// DeleteProfileCAS removes profile state while retaining the deletion audit
// event.  The event table intentionally does not foreign-key profile_id.
func (store *Store) DeleteProfileCAS(ctx context.Context, id string, expectedVersion int64, actor string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin profile delete: %w", err)
	}
	defer tx.Rollback()
	profile, err := scanProfileTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if profile.Version != expectedVersion {
		return ErrConflict
	}
	detail, _ := json.Marshal(map[string]any{"version": expectedVersion})
	if _, err := store.recordEventTx(ctx, tx, id, EventProfileDeleted, cleanActor(actor), detail); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM ai_profiles WHERE id=? AND version=?", id, expectedVersion)
	if err != nil {
		return fmt.Errorf("delete AI profile: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit AI profile delete: %w", err)
	}
	return nil
}

func (store *Store) AppendEvent(ctx context.Context, profileID, kind, actor string, detail json.RawMessage) (AuditEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return AuditEvent{}, err
	}
	defer tx.Rollback()
	event, err := store.recordEventTx(ctx, tx, profileID, kind, actor, detail)
	if err != nil {
		return AuditEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuditEvent{}, fmt.Errorf("commit AI event: %w", err)
	}
	return event, nil
}

func (store *Store) ListEvents(ctx context.Context, profileID string, limit int) ([]AuditEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, profile_id, kind, actor, detail_json, created_at
        FROM ai_events`
	args := []any{}
	if profileID != "" {
		query += " WHERE profile_id=?"
		args = append(args, profileID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list AI events: %w", err)
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var detail, created string
		if err := rows.Scan(&event.ID, &event.ProfileID, &event.Kind, &event.Actor, &detail, &created); err != nil {
			return nil, fmt.Errorf("scan AI event: %w", err)
		}
		event.Detail = json.RawMessage(detail)
		event.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// RecordConfirmedMemory is the only memory write operation.  Requiring an
// explicit confirmation and source event keeps model suggestions from
// silently becoming durable relationship facts.
func (store *Store) RecordConfirmedMemory(ctx context.Context, profileID string, input MemoryInput) (Memory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !input.Confirmed {
		return Memory{}, errors.New("airuntime: memory requires confirmed event")
	}
	if strings.TrimSpace(input.Kind) == "" {
		return Memory{}, errors.New("airuntime: memory kind is required")
	}
	if len(bytesTrim(input.Content)) == 0 {
		input.Content = json.RawMessage("null")
	}
	if !json.Valid(input.Content) {
		return Memory{}, errors.New("airuntime: memory content is not JSON")
	}
	actor := cleanActor(input.Actor)
	if actor == "" {
		actor = "game"
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, err
	}
	defer tx.Rollback()
	if _, err := scanProfileTx(ctx, tx, profileID); err != nil {
		return Memory{}, err
	}
	if input.SourceEventID != 0 {
		var sourceProfile string
		if err := tx.QueryRowContext(ctx, "SELECT profile_id FROM ai_events WHERE id=?", input.SourceEventID).Scan(&sourceProfile); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Memory{}, ErrNotFound
			}
			return Memory{}, fmt.Errorf("check memory source event: %w", err)
		}
		if sourceProfile != profileID {
			return Memory{}, errors.New("airuntime: memory source event belongs to another profile")
		}
	} else {
		return Memory{}, errors.New("airuntime: confirmed memory requires source event")
	}
	detail, _ := json.Marshal(map[string]any{
		"kind": input.Kind, "subject": input.Subject, "source_event_id": input.SourceEventID,
	})
	event, err := store.recordEventTx(ctx, tx, profileID, EventMemoryConfirmed, actor, detail)
	if err != nil {
		return Memory{}, err
	}
	now := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `INSERT INTO ai_memories
        (profile_id, kind, subject, content_json, source_event_id, confirmed, created_at)
        VALUES(?, ?, ?, ?, ?, 1, ?)`, profileID, input.Kind, input.Subject,
		string(input.Content), input.SourceEventID, now)
	if err != nil {
		return Memory{}, fmt.Errorf("record AI memory: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Memory{}, fmt.Errorf("read AI memory id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("commit AI memory: %w", err)
	}
	created, _ := parseTimestamp(now)
	_ = event // event is intentionally retained in the audit log.
	return Memory{ID: id, ProfileID: profileID, Kind: input.Kind, Subject: input.Subject,
		Content: cloneJSON(input.Content), SourceEventID: input.SourceEventID,
		Confirmed: true, CreatedAt: created}, nil
}

func (store *Store) AddMemory(ctx context.Context, profileID string, memory Memory) (Memory, error) {
	return store.RecordConfirmedMemory(ctx, profileID, MemoryInput{
		Kind: memory.Kind, Subject: memory.Subject, Content: memory.Content,
		SourceEventID: memory.SourceEventID, Confirmed: memory.Confirmed,
	})
}

func (store *Store) ListMemories(ctx context.Context, profileID string, limit int) ([]Memory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, profile_id, kind, subject,
        content_json, source_event_id, confirmed, created_at FROM ai_memories
        WHERE profile_id=? ORDER BY id DESC LIMIT ?`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list AI memories: %w", err)
	}
	return scanMemories(rows)
}

func scanMemories(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	memories := make([]Memory, 0)
	for rows.Next() {
		var memory Memory
		var err error
		var content, created string
		var confirmed int
		if err := rows.Scan(&memory.ID, &memory.ProfileID, &memory.Kind, &memory.Subject,
			&content, &memory.SourceEventID, &confirmed, &created); err != nil {
			return nil, err
		}
		memory.Content = json.RawMessage(content)
		memory.Confirmed = confirmed == 1
		memory.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func (store *Store) GetCheckpoint(ctx context.Context, profileID string) (Checkpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var checkpoint Checkpoint
	var state, updated string
	err := store.db.QueryRowContext(ctx, `SELECT profile_id, version, state_json, updated_at
        FROM ai_checkpoints WHERE profile_id=?`, profileID).Scan(&checkpoint.ProfileID,
		&checkpoint.Version, &state, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, ErrNotFound
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("get AI checkpoint: %w", err)
	}
	checkpoint.State = json.RawMessage(state)
	checkpoint.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func (store *Store) SaveCheckpointCAS(ctx context.Context, profileID string, expectedVersion int64, state json.RawMessage, actor string) (Checkpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(bytesTrim(state)) == 0 || !json.Valid(state) {
		return Checkpoint{}, errors.New("airuntime: checkpoint state must be valid JSON")
	}
	if expectedVersion < 0 {
		return Checkpoint{}, ErrConflict
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Checkpoint{}, err
	}
	defer tx.Rollback()
	if _, err := scanProfileTx(ctx, tx, profileID); err != nil {
		return Checkpoint{}, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM ai_checkpoints WHERE profile_id=?", profileID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedVersion != 0 {
			return Checkpoint{}, ErrConflict
		}
		current = 0
	} else if err != nil {
		return Checkpoint{}, fmt.Errorf("read AI checkpoint version: %w", err)
	} else if current != expectedVersion {
		return Checkpoint{}, ErrConflict
	}
	next := current + 1
	updated := store.timestamp(store.now())
	if current == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO ai_checkpoints(profile_id, version, state_json, updated_at)
            VALUES(?, ?, ?, ?)`, profileID, next, string(state), updated)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE ai_checkpoints SET version=?, state_json=?, updated_at=?
            WHERE profile_id=? AND version=?`, next, string(state), updated, profileID, expectedVersion)
		if err == nil {
			var count int64
			count, _ = result.RowsAffected()
			if count != 1 {
				err = ErrConflict
			}
		}
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("save AI checkpoint: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{"version": next})
	if _, err := store.recordEventTx(ctx, tx, profileID, EventCheckpointSaved, cleanActor(actor), detail); err != nil {
		return Checkpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, fmt.Errorf("commit AI checkpoint: %w", err)
	}
	parsed, _ := parseTimestamp(updated)
	return Checkpoint{ProfileID: profileID, Version: next, State: cloneJSON(state), UpdatedAt: parsed}, nil
}

// BeginTokenAttempt reserves the configured maximum response charge before a
// request starts.  This makes concurrent decisions race-safe.  A failed or
// cancelled request still consumes the reservation when it is settled.
func (store *Store) BeginTokenAttempt(ctx context.Context, profileID string, day time.Time, charge int64) (TokenReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if charge <= 0 {
		return TokenReservation{}, errors.New("airuntime: token charge must be positive")
	}
	date := day.UTC().Format("2006-01-02")
	id := newID("attempt")
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenReservation{}, err
	}
	defer tx.Rollback()
	var budget int64
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT daily_token_budget, status FROM ai_profiles WHERE id=?", profileID).Scan(&budget, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenReservation{}, ErrNotFound
		}
		return TokenReservation{}, fmt.Errorf("read AI token budget: %w", err)
	}
	if status != ProfileStatusActive {
		return TokenReservation{}, fmt.Errorf("airuntime: profile status %q does not allow decisions", status)
	}
	// An unresolved request remains the player's active turn even across a
	// UTC budget boundary. Never reserve a replacement before reconciling it.
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_token_attempts WHERE profile_id=? AND settled=0)`, profileID).Scan(&pending); err != nil {
		return TokenReservation{}, err
	}
	if pending != 0 {
		return TokenReservation{}, ErrAttemptPending
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ai_token_usage(profile_id, usage_date)
        VALUES(?, ?)`, profileID, date); err != nil {
		return TokenReservation{}, err
	}
	var charged, reserved, actual int64
	if err := tx.QueryRowContext(ctx, `SELECT charged_tokens, reserved_tokens, total_tokens FROM ai_token_usage
        WHERE profile_id=? AND usage_date=?`, profileID, date).Scan(&charged, &reserved, &actual); err != nil {
		return TokenReservation{}, err
	}
	accounted := charged
	if actual > accounted {
		accounted = actual
	}
	if budget == 0 || accounted >= budget || reserved >= budget-accounted || charge > budget-accounted-reserved {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_token_usage SET attempts=attempts+1,
            over_budget_attempts=over_budget_attempts+1 WHERE profile_id=? AND usage_date=?`, profileID, date); err != nil {
			return TokenReservation{}, err
		}
		detail, _ := json.Marshal(map[string]any{"date": date, "requested_tokens": charge, "budget": budget})
		if _, err := store.recordEventTx(ctx, tx, profileID, EventBudgetExceeded, "runtime", detail); err != nil {
			return TokenReservation{}, err
		}
		if err := tx.Commit(); err != nil {
			return TokenReservation{}, err
		}
		return TokenReservation{}, ErrTokenBudgetExceeded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_usage SET attempts=attempts+1,
        reserved_tokens=reserved_tokens+? WHERE profile_id=? AND usage_date=?`, charge, profileID, date); err != nil {
		return TokenReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_token_attempts
		(id, profile_id, usage_date, charge_tokens, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, profileID, date, charge, string(TokenAttemptReserved), store.timestamp(store.now()), store.timestamp(store.now())); err != nil {
		return TokenReservation{}, err
	}
	detail, _ := json.Marshal(map[string]any{"date": date, "reserved_tokens": charge})
	if _, err := store.recordEventTx(ctx, tx, profileID, EventDecisionStarted, "runtime", detail); err != nil {
		return TokenReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return TokenReservation{}, err
	}
	return TokenReservation{ID: id, ProfileID: profileID, Date: date, Charge: charge}, nil
}

const maxTokenAttemptPromptBytes = 1 << 20

// PrepareTokenAttempt durably records the exact model input before a runner
// is called. Repeating the same write is idempotent; changing any request
// field after preparation is a conflict and must not be treated as a retry.
func (store *Store) PrepareTokenAttempt(ctx context.Context, reservation TokenReservation, metadata TokenAttemptMetadata) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateTokenReservation(reservation); err != nil {
		return err
	}
	if len([]byte(metadata.Prompt)) > maxTokenAttemptPromptBytes || strings.ContainsRune(metadata.Prompt, '\x00') {
		return errors.New("airuntime: token attempt prompt is invalid")
	}
	if len([]byte(metadata.ThreadID)) > 256 || strings.ContainsAny(metadata.ThreadID, "\x00\r\n") {
		return errors.New("airuntime: token attempt thread ID is invalid")
	}
	if !metadata.Resume && metadata.ThreadID != "" {
		return errors.New("airuntime: token attempt thread requires resume")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID, date, state, prompt, threadID string
	var charge int64
	var resume int
	var settled int
	if err := tx.QueryRowContext(ctx, `SELECT profile_id, usage_date, charge_tokens, state,
        prompt, resume, thread_id, settled FROM ai_token_attempts WHERE id=?`, reservation.ID).
		Scan(&profileID, &date, &charge, &state, &prompt, &resume, &threadID, &settled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if profileID != reservation.ProfileID || date != reservation.Date || charge != reservation.Charge {
		return errors.New("airuntime: token reservation mismatch")
	}
	if settled != 0 || TokenAttemptState(state) == TokenAttemptSettled {
		return ErrAttemptSettled
	}
	if prompt != "" || threadID != "" || resume != 0 || state == string(TokenAttemptPrepared) || state == string(TokenAttemptDispatched) || state == string(TokenAttemptRecorded) || state == string(TokenAttemptUnknown) {
		if prompt != metadata.Prompt || threadID != metadata.ThreadID || (resume != 0) != metadata.Resume {
			return ErrAttemptConflict
		}
		if state != string(TokenAttemptReserved) {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_attempts SET state=?, prompt=?, resume=?, thread_id=?, updated_at=? WHERE id=? AND settled=0`,
		string(TokenAttemptPrepared), metadata.Prompt, boolInt(metadata.Resume), metadata.ThreadID, store.timestamp(store.now()), reservation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// MarkTokenAttemptDispatched records the point immediately before calling a
// model runner. A dispatched attempt without a recorded outcome is ambiguous
// after a process crash and is never automatically submitted again.
func (store *Store) MarkTokenAttemptDispatched(ctx context.Context, reservation TokenReservation) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateTokenReservation(reservation); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID, date, state string
	var charge int64
	var settled int
	if err := tx.QueryRowContext(ctx, `SELECT profile_id, usage_date, charge_tokens, state, settled
        FROM ai_token_attempts WHERE id=?`, reservation.ID).Scan(&profileID, &date, &charge, &state, &settled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if profileID != reservation.ProfileID || date != reservation.Date || charge != reservation.Charge {
		return errors.New("airuntime: token reservation mismatch")
	}
	if settled != 0 || TokenAttemptState(state) == TokenAttemptSettled {
		return ErrAttemptSettled
	}
	if state == string(TokenAttemptRecorded) || state == string(TokenAttemptUnknown) || state == string(TokenAttemptDispatched) {
		return nil
	}
	if state != string(TokenAttemptPrepared) {
		return ErrAttemptConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_attempts SET state=?, updated_at=? WHERE id=? AND settled=0`,
		string(TokenAttemptDispatched), store.timestamp(store.now()), reservation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// RecordTokenAttemptResult commits a bounded runner outcome before token
// settlement. The write is idempotent for the same outcome and rejects a
// conflicting second result for the same reservation.
func (store *Store) RecordTokenAttemptResult(ctx context.Context, reservation TokenReservation, outcome TokenAttemptOutcome, failed bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateTokenReservation(reservation); err != nil {
		return err
	}
	if err := validateTokenAttemptOutcome(&outcome); err != nil {
		return err
	}
	data, err := json.Marshal(outcome)
	if err != nil || len(data) > maxTokenAttemptPromptBytes {
		return errors.New("airuntime: token attempt outcome is too large")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID, date, state, existing string
	var charge int64
	var settled, existingFailed int
	if err := tx.QueryRowContext(ctx, `SELECT profile_id, usage_date, charge_tokens, state, COALESCE(outcome_json, ''), settled, failed
        FROM ai_token_attempts WHERE id=?`, reservation.ID).Scan(&profileID, &date, &charge, &state, &existing, &settled, &existingFailed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if profileID != reservation.ProfileID || date != reservation.Date || charge != reservation.Charge {
		return errors.New("airuntime: token reservation mismatch")
	}
	if settled != 0 || TokenAttemptState(state) == TokenAttemptSettled {
		return ErrAttemptSettled
	}
	if existing != "" {
		if existing != string(data) || existingFailed != boolInt(failed) {
			return ErrAttemptConflict
		}
		return nil
	}
	if state != string(TokenAttemptReserved) && state != string(TokenAttemptPrepared) && state != string(TokenAttemptDispatched) && state != string(TokenAttemptUnknown) {
		return ErrAttemptConflict
	}
	usage := outcome.Usage
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_attempts SET state=?, outcome_json=?, failed=?,
        input_tokens=?, output_tokens=?, total_tokens=?, error_text=?, updated_at=? WHERE id=? AND settled=0`,
		string(TokenAttemptRecorded), string(data), boolInt(failed), usage.InputTokens, usage.OutputTokens,
		usage.TotalTokens, outcome.Error, store.timestamp(store.now()), reservation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// MarkTokenAttemptUnknown closes the dispatch phase when its result cannot be
// proven. It is safe to call repeatedly and preserves the reservation until
// FinishTokenAttempt accounts for it.
func (store *Store) MarkTokenAttemptUnknown(ctx context.Context, reservation TokenReservation, reason string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateTokenReservation(reservation); err != nil {
		return err
	}
	if len(reason) > 512 || strings.ContainsAny(reason, "\x00\r\n") {
		return errors.New("airuntime: token attempt reason is invalid")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID, date, state string
	var charge int64
	var settled int
	if err := tx.QueryRowContext(ctx, `SELECT profile_id, usage_date, charge_tokens, state, settled
        FROM ai_token_attempts WHERE id=?`, reservation.ID).Scan(&profileID, &date, &charge, &state, &settled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if profileID != reservation.ProfileID || date != reservation.Date || charge != reservation.Charge {
		return errors.New("airuntime: token reservation mismatch")
	}
	if settled != 0 || TokenAttemptState(state) == TokenAttemptSettled {
		return ErrAttemptSettled
	}
	if state == string(TokenAttemptRecorded) {
		return ErrAttemptConflict
	}
	if state == string(TokenAttemptUnknown) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_attempts SET state=?, error_text=?, updated_at=? WHERE id=? AND settled=0`,
		string(TokenAttemptUnknown), reason, store.timestamp(store.now()), reservation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// GetTokenAttempt returns a durable attempt, including its recorded request
// metadata and outcome. It is intended for supervisor recovery, not for model
// prompts or client-facing status.
func (store *Store) GetTokenAttempt(ctx context.Context, attemptID string) (TokenAttempt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(attemptID) == "" {
		return TokenAttempt{}, ErrNotFound
	}
	return store.scanTokenAttempt(ctx, `SELECT id, profile_id, usage_date, charge_tokens, state,
        prompt, resume, thread_id, outcome_json, error_text, failed, created_at, updated_at, settled_at
        FROM ai_token_attempts WHERE id=?`, attemptID)
}

// PendingTokenAttempt returns the oldest unsettled attempt for a profile. A
// supervisor must resolve it before creating a new model turn.
func (store *Store) PendingTokenAttempt(ctx context.Context, profileID string) (TokenAttempt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(profileID) == "" {
		return TokenAttempt{}, ErrNotFound
	}
	return store.scanTokenAttempt(ctx, `SELECT id, profile_id, usage_date, charge_tokens, state,
        prompt, resume, thread_id, outcome_json, error_text, failed, created_at, updated_at, settled_at
        FROM ai_token_attempts WHERE profile_id=? AND settled=0 ORDER BY created_at ASC, id ASC LIMIT 1`, profileID)
}

func (store *Store) scanTokenAttempt(ctx context.Context, query string, args ...any) (TokenAttempt, error) {
	var attempt TokenAttempt
	var state, created, updated string
	var outcomeValue, settledValue sql.NullString
	var resume, failed int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&attempt.ID, &attempt.ProfileID, &attempt.Date,
		&attempt.Charge, &state, &attempt.Prompt, &resume, &attempt.ThreadID, &outcomeValue, &attempt.Error, &failed,
		&created, &updated, &settledValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenAttempt{}, ErrNotFound
		}
		return TokenAttempt{}, err
	}
	attempt.State = TokenAttemptState(state)
	attempt.Resume = resume != 0
	attempt.Failed = failed != 0
	var err error
	attempt.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return TokenAttempt{}, err
	}
	attempt.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return TokenAttempt{}, err
	}
	if settledValue.Valid && settledValue.String != "" {
		attempt.SettledAt, err = parseTimestamp(settledValue.String)
		if err != nil {
			return TokenAttempt{}, err
		}
	}
	if outcomeValue.Valid && outcomeValue.String != "" {
		var decoded TokenAttemptOutcome
		if err := json.Unmarshal([]byte(outcomeValue.String), &decoded); err != nil {
			return TokenAttempt{}, fmt.Errorf("airuntime: decode token attempt outcome: %w", err)
		}
		attempt.Outcome = &decoded
	}
	return attempt, nil
}

func validateTokenReservation(reservation TokenReservation) error {
	if strings.TrimSpace(reservation.ID) == "" || strings.TrimSpace(reservation.ProfileID) == "" ||
		strings.TrimSpace(reservation.Date) == "" || reservation.Charge <= 0 {
		return errors.New("airuntime: invalid token reservation")
	}
	return nil
}

func validateTokenAttemptOutcome(outcome *TokenAttemptOutcome) error {
	if outcome == nil {
		return errors.New("airuntime: token attempt outcome is required")
	}
	if len(outcome.ThreadID) > 256 || len(outcome.LastMessage) > maxTokenAttemptPromptBytes ||
		len(outcome.TurnID) > 256 || len(outcome.TurnStatus) > 64 || len(outcome.TurnError) > 512 ||
		len(outcome.ProcessStatus) > 64 || len(outcome.ProcessSignal) > 128 || len(outcome.CheckpointState) > 64 ||
		len(outcome.Error) > 512 {
		return errors.New("airuntime: token attempt outcome is invalid")
	}
	for _, value := range []string{outcome.ThreadID, outcome.LastMessage, outcome.TurnID, outcome.TurnStatus,
		outcome.TurnError, outcome.ProcessStatus, outcome.ProcessSignal, outcome.CheckpointState, outcome.Error} {
		if strings.ContainsRune(value, '\x00') {
			return errors.New("airuntime: token attempt outcome contains NUL")
		}
	}
	if outcome.Usage.InputTokens < 0 || outcome.Usage.OutputTokens < 0 || outcome.Usage.TotalTokens < 0 {
		return errors.New("airuntime: negative token usage")
	}
	if outcome.Usage.TotalTokens == 0 {
		outcome.Usage.TotalTokens = outcome.Usage.InputTokens + outcome.Usage.OutputTokens
	}
	return nil
}

func (store *Store) FinishTokenAttempt(ctx context.Context, reservation TokenReservation, usage TokenUsage, failed bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID, date, attemptState string
	var outcomeValue sql.NullString
	var charge int64
	var settled, recordedFailed int
	if err := tx.QueryRowContext(ctx, `SELECT profile_id, usage_date, charge_tokens, state, outcome_json, failed, settled
		FROM ai_token_attempts WHERE id=?`, reservation.ID).Scan(&profileID, &date, &charge, &attemptState, &outcomeValue, &recordedFailed, &settled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if profileID != reservation.ProfileID || date != reservation.Date || charge != reservation.Charge {
		return errors.New("airuntime: token reservation mismatch")
	}
	if settled != 0 {
		return ErrAttemptSettled
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return errors.New("airuntime: negative token usage")
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if outcomeValue.Valid && outcomeValue.String != "" {
		var outcome TokenAttemptOutcome
		if err := json.Unmarshal([]byte(outcomeValue.String), &outcome); err != nil {
			return fmt.Errorf("airuntime: decode token attempt outcome: %w", err)
		}
		if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
			usage = outcome.Usage
		} else if usage.InputTokens != outcome.Usage.InputTokens || usage.OutputTokens != outcome.Usage.OutputTokens || usage.TotalTokens != outcome.Usage.TotalTokens {
			return ErrAttemptConflict
		}
		if recordedFailed != boolInt(failed) {
			return ErrAttemptConflict
		}
		failed = recordedFailed != 0
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_usage SET reserved_tokens=reserved_tokens-?,
        charged_tokens=charged_tokens+?, input_tokens=input_tokens+?, output_tokens=output_tokens+?,
        total_tokens=total_tokens+?, failed_attempts=failed_attempts+? WHERE profile_id=? AND usage_date=?`,
		charge, charge, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, boolInt(failed), profileID, date); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_token_attempts SET state=?, settled=1, failed=?,
		input_tokens=?, output_tokens=?, total_tokens=?, updated_at=?, settled_at=? WHERE id=? AND settled=0`,
		string(TokenAttemptSettled), boolInt(failed), usage.InputTokens, usage.OutputTokens, usage.TotalTokens,
		store.timestamp(store.now()), store.timestamp(store.now()), reservation.ID); err != nil {
		return err
	}
	// Schedule delivery is committed in the same transaction as token
	// settlement. A recorded (therefore known) runner outcome acknowledges
	// every reminder included in this exact prompt. If the attempt failed
	// before dispatch, release its claims so the next turn can retry. An
	// unsettled dispatched/unknown attempt never reaches this method and its
	// delivery lease is intentionally retained for exact recovery.
	if err := store.finishScheduleDeliveryTx(ctx, tx, profileID, reservation.ID, attemptState, failed); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"date": date, "charged_tokens": charge,
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"total_tokens": usage.TotalTokens})
	kind := EventDecisionFinished
	if failed {
		kind = EventDecisionFailed
	}
	if _, err := store.recordEventTx(ctx, tx, profileID, kind, "runtime", detail); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// finishScheduleDeliveryTx closes the schedule boundary under the same
// SQLite transaction as FinishTokenAttempt. This removes the crash window in
// which Codex has a durably recorded outcome but a reminder remains pending,
// or a reminder is acknowledged while its token reservation is unsettled.
func (store *Store) finishScheduleDeliveryTx(ctx context.Context, tx *sql.Tx, profileID, attemptID, attemptState string, failed bool) error {
	if attemptState != string(TokenAttemptRecorded) && attemptState != string(TokenAttemptReserved) && attemptState != string(TokenAttemptPrepared) {
		return nil
	}
	if attemptState == string(TokenAttemptReserved) || attemptState == string(TokenAttemptPrepared) {
		if !failed {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE ai_schedules SET status=?, claim_token='', claim_until='', delivery_attempt_id='', updated_at=?
			WHERE profile_id=? AND delivery_attempt_id=? AND status=?`, string(SchedulePending), store.timestamp(store.now()), profileID, attemptID, string(ScheduleDelivering))
		return err
	}
	if failed {
		// A dispatched process that returned a failed/incomplete result does
		// not prove whether Codex accepted the prompt. Keep the delivery claim
		// fenced and require exact recovery or operator resolution; allowing
		// lease expiry here could submit the same reminder twice.
		_, err := tx.ExecContext(ctx, `UPDATE ai_schedules SET claim_until='', updated_at=?
			WHERE profile_id=? AND delivery_attempt_id=? AND status=?`, store.timestamp(store.now()), profileID, attemptID, string(ScheduleDelivering))
		return err
	}
	now := store.now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT id, run_at, repeat_interval_ns, claim_token
		FROM ai_schedules WHERE profile_id=? AND delivery_attempt_id=? AND status=?`, profileID, attemptID, string(ScheduleDelivering))
	if err != nil {
		return fmt.Errorf("find delivered AI schedules: %w", err)
	}
	defer rows.Close()
	// Keep the persisted schedule outcome intentionally small. The complete
	// token outcome remains in ai_token_attempts and may contain model text;
	// MCP schedule listings only need to know that this delivery was settled.
	deliveryOutcome := json.RawMessage(`{"delivered":true}`)
	for rows.Next() {
		var id, runAt, claimToken string
		var repeatNanos int64
		if err := rows.Scan(&id, &runAt, &repeatNanos, &claimToken); err != nil {
			return fmt.Errorf("scan delivered AI schedule: %w", err)
		}
		due, err := parseTimestamp(runAt)
		if err != nil {
			return err
		}
		nextStatus := ScheduleDelivered
		nextRun := due
		completedAt := store.timestamp(now)
		if repeatNanos > 0 {
			interval := time.Duration(repeatNanos)
			if interval < minScheduleRepeat || interval > maxScheduleRepeat {
				return fmt.Errorf("%w: persisted repeat interval is invalid", ErrInvalidSchedule)
			}
			nextStatus = SchedulePending
			nextRun = due.Add(interval)
			for !nextRun.After(now) {
				nextRun = nextRun.Add(interval)
			}
			completedAt = ""
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_schedules SET status=?, run_at=?, claim_token='', claim_until='',
			delivery_attempt_id='', last_claim_token=?, last_outcome_json=?, occurrences=occurrences+1,
			updated_at=?, completed_at=? WHERE profile_id=? AND id=? AND status=? AND claim_token=?`,
			string(nextStatus), store.timestamp(nextRun), claimToken, string(deliveryOutcome), store.timestamp(now), completedAt,
			profileID, id, string(ScheduleDelivering), claimToken); err != nil {
			return fmt.Errorf("complete AI schedule delivery: %w", err)
		}
		detail, _ := json.Marshal(map[string]any{"schedule_id": id, "status": string(nextStatus), "next_run_at": store.timestamp(nextRun), "attempt_id": attemptID})
		if _, err := store.recordEventTx(ctx, tx, profileID, EventScheduleCompleted, "runtime", detail); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (store *Store) TokenUsage(ctx context.Context, profileID string, day time.Time) (TokenUsage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	date := day.UTC().Format("2006-01-02")
	var usage TokenUsage
	usage.Date = date
	err := store.db.QueryRowContext(ctx, `SELECT charged_tokens, reserved_tokens, input_tokens,
        output_tokens, total_tokens, attempts, failed_attempts, over_budget_attempts
        FROM ai_token_usage WHERE profile_id=? AND usage_date=?`, profileID, date).Scan(
		&usage.ChargedTokens, &usage.ReservedTokens, &usage.InputTokens, &usage.OutputTokens,
		&usage.TotalTokens, &usage.Attempts, &usage.FailedAttempts, &usage.OverBudgetAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return usage, nil
	}
	if err != nil {
		return TokenUsage{}, err
	}
	return usage, nil
}

func (store *Store) recordEventTx(ctx context.Context, tx *sql.Tx, profileID, kind, actor string, detail json.RawMessage) (AuditEvent, error) {
	if strings.TrimSpace(profileID) == "" {
		return AuditEvent{}, errors.New("airuntime: event profile id is required")
	}
	if strings.TrimSpace(kind) == "" {
		return AuditEvent{}, errors.New("airuntime: event kind is required")
	}
	if actor = cleanActor(actor); actor == "" {
		actor = "system"
	}
	if len(bytesTrim(detail)) == 0 {
		detail = json.RawMessage("{}")
	}
	if !json.Valid(detail) {
		return AuditEvent{}, errors.New("airuntime: event detail is not JSON")
	}
	created := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `INSERT INTO ai_events(profile_id, kind, actor, detail_json, created_at)
        VALUES(?, ?, ?, ?, ?)`, profileID, kind, actor, string(detail), created)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("record AI event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AuditEvent{}, err
	}
	parsed, _ := parseTimestamp(created)
	return AuditEvent{ID: id, ProfileID: profileID, Kind: kind, Actor: actor,
		Detail: cloneJSON(detail), CreatedAt: parsed}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(row scanner) (Profile, error) {
	profile, err := scanProfileRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return profile, err
}

func scanProfileRow(row scanner) (Profile, error) {
	var profile Profile
	var accountID, accountUsername, characterID, characterName, modelConfigID string
	var personality, goal, skills, created, updated string
	var unlimited int
	if err := row.Scan(&profile.ID, &accountID, &accountUsername, &characterID, &characterName,
		&modelConfigID, &personality, &goal, &skills, &unlimited, &profile.DailyTokenBudget,
		&profile.ExternalSpendLimit, &profile.Status, &profile.Version, &created, &updated); err != nil {
		return Profile{}, err
	}
	if err := json.Unmarshal([]byte(personality), &profile.Personality); err != nil {
		return Profile{}, fmt.Errorf("decode AI personality: %w", err)
	}
	if err := json.Unmarshal([]byte(goal), &profile.Goal); err != nil {
		return Profile{}, fmt.Errorf("decode AI goal: %w", err)
	}
	if err := json.Unmarshal([]byte(skills), &profile.Skills); err != nil {
		return Profile{}, fmt.Errorf("decode AI skills: %w", err)
	}
	profile.Skills = cloneSkills(profile.Skills)
	profile.Account = AccountIdentity{ID: accountID, Username: accountUsername}
	profile.Character = CharacterIdentity{ID: characterID, Name: characterName}
	profile.ModelConfigID = modelConfigID
	profile.UnlimitedFunds = unlimited == 1
	var err error
	profile.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return Profile{}, err
	}
	profile.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func scanProfileTx(ctx context.Context, tx *sql.Tx, id string) (Profile, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, account_id, account_username,
		character_id, character_name, model_config_id, personality_json, goal_json, skills_json,
        unlimited_funds, daily_token_budget, external_spend_limit, status,
        version, created_at, updated_at FROM ai_profiles WHERE id=?`, id)
	return scanProfile(row)
}

func cloneProfile(profile Profile) Profile {
	profile.Skills = cloneSkills(profile.Skills)
	profile.Personality.Traits = append([]string(nil), profile.Personality.Traits...)
	if profile.Personality.Values != nil {
		profile.Personality.Values = copyStringMap(profile.Personality.Values)
	}
	profile.Goal.Metadata = copyStringMap(profile.Goal.Metadata)
	profile.Goal.CharacterBuild = profile.Goal.CharacterBuild.Clone()
	if profile.Goal.Life != nil {
		life := *profile.Goal.Life
		life.Activities = append([]string(nil), life.Activities...)
		profile.Goal.Life = &life
	}
	return profile
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func cleanActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if len(actor) > 128 {
		actor = actor[:128]
	}
	return actor
}

func bytesTrim(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }

func (store *Store) timestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("airuntime: invalid timestamp: %w", err)
	}
	return parsed, nil
}

func newID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
