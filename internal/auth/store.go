package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

const (
	AccountActive        = "active"
	AccountDisabled      = "disabled"
	AdminActive          = "active"
	AdminDisabled        = "disabled"
	adminSessionLifetime = 12 * time.Hour
	maxLoginFailures     = 5
	loginLockDuration    = 15 * time.Minute
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
	mu   sync.Mutex
}

type Account struct {
	ID                 int64
	Username           string
	Status             string
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastLoginAt        *time.Time
	FailedAttempts     int
	LockedUntil        *time.Time
}

type AdminUser struct {
	ID             int64
	Username       string
	Role           string
	Status         string
	CreatedAt      time.Time
	FailedAttempts int
	LockedUntil    *time.Time
}

type Session struct {
	ID          int64
	AdminUserID int64
	Username    string
	Role        string
	ExpiresAt   time.Time
}

type AuditEvent struct {
	ID        int64
	ActorID   *int64
	Event     string
	Username  string
	SourceIP  string
	Detail    string
	CreatedAt time.Time
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("auth database path is empty")
	}
	if path != ":memory:" && path != "file::memory:?cache=shared" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create auth database directory: %w", err)
		}
		// The database contains password-derived secrets and live admin sessions.
		// Refuse links/special files and tighten an existing database before SQLite
		// opens it. The parent directory is private so WAL/SHM sidecars inherit a
		// protected boundary as well.
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("auth database must not be a symbolic link")
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("auth database is not a regular file")
			}
			if err := os.Chmod(path, 0o600); err != nil {
				return nil, fmt.Errorf("protect auth database: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect auth database: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open auth database: %w", err)
	}
	// One connection per process avoids per-connection PRAGMA differences;
	// SQLite WAL still permits the gateway and the admin process to coexist.
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
			db.Close()
			return nil, fmt.Errorf("configure auth database (%s): %w", pragma, err)
		}
	}
	if path != ":memory:" && path != "file::memory:?cache=shared" {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("protect auth database: %w", err)
		}
		if err := protectSQLiteSidecars(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL COLLATE BINARY UNIQUE,
  password_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_login_at TEXT,
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  locked_until TEXT
);
CREATE TABLE IF NOT EXISTS admin_users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL COLLATE BINARY UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin','operator')),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
	created_at TEXT NOT NULL,
	failed_attempts INTEGER NOT NULL DEFAULT 0,
	locked_until TEXT
);
CREATE TABLE IF NOT EXISTS sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
  event TEXT NOT NULL,
  username TEXT,
  source_ip TEXT,
  detail TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_created_idx ON audit_events(created_at DESC);
CREATE TABLE IF NOT EXISTS login_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT,
  source_ip TEXT,
  successful INTEGER NOT NULL CHECK (successful IN (0,1)),
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS login_attempts_created_idx ON login_attempts(created_at DESC);
`
	if _, err := store.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create auth schema: %w", err)
	}
	// Databases created by the first version of the console do not have the
	// admin lockout columns. Keep upgrades additive and idempotent.
	for _, column := range []struct {
		name string
		def  string
	}{
		{"failed_attempts", "INTEGER NOT NULL DEFAULT 0"},
		{"locked_until", "TEXT"},
	} {
		if err := store.ensureColumn(ctx, "admin_users", column.name, column.def); err != nil {
			return fmt.Errorf("upgrade auth schema: %w", err)
		}
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)",
		1, store.timestamp(store.now())); err != nil {
		return fmt.Errorf("record auth schema migration: %w", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)",
		2, store.timestamp(store.now())); err != nil {
		return fmt.Errorf("record auth schema migration: %w", err)
	}
	if err := protectSQLiteSidecars(store.path); err != nil {
		return err
	}
	return nil
}

func protectSQLiteSidecars(path string) error {
	if path == ":memory:" || path == "file::memory:?cache=shared" {
		return nil
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect auth database sidecar: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("auth database sidecar is not a regular file")
		}
		if err := os.Chmod(sidecar, 0o600); err != nil {
			return fmt.Errorf("protect auth database sidecar: %w", err)
		}
	}
	return nil
}

func (store *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := store.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primary int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primary); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

func (store *Store) HasAdmins(ctx context.Context) (bool, error) {
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (store *Store) CreateAccount(ctx context.Context, username string, password []byte) (Account, error) {
	return store.CreateAccountAs(ctx, nil, username, password)
}

// CreateAccountAs creates a game account and attributes the administrative
// action to actorID when it was initiated from the web console. A nil actor is
// used by command-line imports and the game service itself.
func (store *Store) CreateAccountAs(ctx context.Context, actorID *int64, username string, password []byte) (Account, error) {
	username = CanonicalGameUsername(username)
	if err := ValidateGameUsername([]byte(username)); err != nil {
		return Account{}, err
	}
	hash, err := HashGamePassword(password)
	if err != nil {
		return Account{}, err
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `
INSERT INTO accounts(username,password_hash,status,must_change_password,created_at,updated_at)
VALUES(?,?,?,0,?,?)`, username, hash, AccountActive, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Account{}, fmt.Errorf("account already exists")
		}
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Account{}, err
	}
	if err := store.recordAudit(ctx, actorID, "account_created", username, "", ""); err != nil {
		return Account{}, err
	}
	return store.GetAccount(ctx, id)
}

// ImportLegacyAccount creates an account entry for a pre-existing SAAC
// character owner. Without a supplied password it is disabled and receives an
// unusable random hash, so migration can never silently grant access.
func (store *Store) ImportLegacyAccount(ctx context.Context, username string, password []byte) (bool, error) {
	username = CanonicalGameUsername(username)
	if err := ValidateGameUsername([]byte(username)); err != nil {
		return false, err
	}
	hash, err := randomUnusableGamePasswordHash()
	status := AccountDisabled
	mustChange := 1
	if len(password) > 0 {
		hash, err = HashGamePassword(password)
		status = AccountActive
		mustChange = 0
	}
	if err != nil {
		return false, err
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `
INSERT OR IGNORE INTO accounts(username,password_hash,status,must_change_password,created_at,updated_at)
VALUES(?,?,?,?,?,?)`, username, hash, status, mustChange, now, now)
	if err != nil {
		return false, fmt.Errorf("import account %q: %w", username, err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (store *Store) GetAccount(ctx context.Context, id int64) (Account, error) {
	row := store.db.QueryRowContext(ctx, `SELECT id,username,status,must_change_password,created_at,updated_at,last_login_at,failed_attempts,locked_until FROM accounts WHERE id=?`, id)
	return scanAccount(row)
}

func (store *Store) GetAccountByUsername(ctx context.Context, username string) (Account, error) {
	username = CanonicalGameUsername(username)
	// New rows are stored canonically.  The lower() fallback keeps accounts
	// imported by older gateway builds reachable without requiring a destructive
	// database rewrite; ASCII is guaranteed by ValidateGameUsername.
	row := store.db.QueryRowContext(ctx, `SELECT id,username,status,must_change_password,created_at,updated_at,last_login_at,failed_attempts,locked_until FROM accounts WHERE lower(username)=? ORDER BY id LIMIT 1`, username)
	return scanAccount(row)
}

func (store *Store) ListAccounts(ctx context.Context, search string) ([]Account, error) {
	query := `SELECT id,username,status,must_change_password,created_at,updated_at,last_login_at,failed_attempts,locked_until FROM accounts`
	args := []any{}
	if search != "" {
		query += " WHERE username LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(search)+"%")
	}
	query += " ORDER BY username COLLATE BINARY LIMIT 500"
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (store *Store) SetAccountPassword(ctx context.Context, id int64, password []byte, mustChange bool) error {
	return store.SetAccountPasswordAs(ctx, nil, id, password, mustChange)
}

func (store *Store) SetAccountPasswordAs(ctx context.Context, actorID *int64, id int64, password []byte, mustChange bool) error {
	hash, err := HashGamePassword(password)
	if err != nil {
		return err
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `UPDATE accounts SET password_hash=?,must_change_password=?,failed_attempts=0,locked_until=NULL,updated_at=? WHERE id=?`, hash, boolInt(mustChange), now, id)
	if err != nil {
		return fmt.Errorf("set account password: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	account, err := store.GetAccount(ctx, id)
	if err == nil {
		err = store.recordAudit(ctx, actorID, "account_password_changed", account.Username, "", "")
	}
	return err
}

func (store *Store) SetAccountStatus(ctx context.Context, id int64, status string) error {
	return store.SetAccountStatusAs(ctx, nil, id, status)
}

func (store *Store) SetAccountStatusAs(ctx context.Context, actorID *int64, id int64, status string) error {
	if status != AccountActive && status != AccountDisabled {
		return fmt.Errorf("invalid account status")
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `UPDATE accounts SET status=?,updated_at=? WHERE id=?`, status, now, id)
	if err != nil {
		return fmt.Errorf("set account status: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	account, err := store.GetAccount(ctx, id)
	if err == nil {
		err = store.recordAudit(ctx, actorID, "account_status_changed", account.Username, "", status)
	}
	return err
}

func (store *Store) Authenticate(ctx context.Context, username string, password []byte, sourceIP string) (Account, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	username = CanonicalGameUsername(username)
	if ValidateGameUsername([]byte(username)) != nil || ValidateGamePassword(password) != nil {
		_ = store.recordLoginAttempt(ctx, username, sourceIP, false)
		return Account{}, ErrInvalidCredentials
	}
	account, err := store.GetAccountByUsername(ctx, username)
	if err != nil {
		_ = store.recordLoginAttempt(ctx, username, sourceIP, false)
		return Account{}, ErrInvalidCredentials
	}
	now := store.now()
	if account.Status != AccountActive || (account.LockedUntil != nil && account.LockedUntil.After(now)) || !VerifyPassword(accountPasswordHash(ctx, store, account.ID), password) {
		store.recordFailedLogin(ctx, account, sourceIP, now)
		return Account{}, ErrInvalidCredentials
	}
	updated := store.timestamp(now)
	if _, err := store.db.ExecContext(ctx, `UPDATE accounts SET last_login_at=?,failed_attempts=0,locked_until=NULL,updated_at=? WHERE id=?`, updated, updated, account.ID); err != nil {
		return Account{}, fmt.Errorf("record account login: %w", err)
	}
	account.LastLoginAt = &now
	account.FailedAttempts = 0
	account.LockedUntil = nil
	_ = store.recordLoginAttempt(ctx, username, sourceIP, true)
	_ = store.recordAudit(ctx, nil, "game_login_success", username, sourceIP, "")
	return account, nil
}

// accountPasswordHash fetches the hash separately so Account remains safe to
// expose to handlers without ever carrying a password hash.
func accountPasswordHash(ctx context.Context, store *Store, id int64) string {
	var hash string
	_ = store.db.QueryRowContext(ctx, "SELECT password_hash FROM accounts WHERE id=?", id).Scan(&hash)
	return hash
}

func (store *Store) CreateAdmin(ctx context.Context, username string, password []byte) (AdminUser, error) {
	if err := ValidateAdminUsername(username); err != nil {
		return AdminUser{}, err
	}
	hash, err := HashAdminPassword(password)
	if err != nil {
		return AdminUser{}, err
	}
	now := store.timestamp(store.now())
	result, err := store.db.ExecContext(ctx, `INSERT INTO admin_users(username,password_hash,role,status,created_at) VALUES(?,?, 'admin', ?, ?)`, username, hash, AdminActive, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return AdminUser{}, fmt.Errorf("administrator already exists")
		}
		return AdminUser{}, fmt.Errorf("create administrator: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AdminUser{}, err
	}
	admin, err := store.GetAdmin(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}
	if err := store.recordAudit(ctx, &admin.ID, "admin_created", admin.Username, "", ""); err != nil {
		return AdminUser{}, err
	}
	return admin, nil
}

func (store *Store) GetAdmin(ctx context.Context, id int64) (AdminUser, error) {
	row := store.db.QueryRowContext(ctx, "SELECT id,username,role,status,created_at,failed_attempts,locked_until FROM admin_users WHERE id=?", id)
	return scanAdmin(row)
}

func (store *Store) AuthenticateAdmin(ctx context.Context, username string, password []byte, sourceIP string) (AdminUser, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var id int64
	var hash, role, status, created string
	var failed int
	var locked sql.NullString
	err := store.db.QueryRowContext(ctx, "SELECT id,password_hash,role,status,created_at,failed_attempts,locked_until FROM admin_users WHERE username=?", username).Scan(&id, &hash, &role, &status, &created, &failed, &locked)
	if err != nil {
		_ = store.recordLoginAttempt(ctx, "admin:"+username, sourceIP, false)
		return AdminUser{}, ErrInvalidCredentials
	}
	now := store.now()
	lockedUntil, parseErr := nullableTimestamp(locked)
	if parseErr != nil || status != AdminActive || (lockedUntil != nil && lockedUntil.After(now)) || !VerifyPassword(hash, password) {
		if status == AdminActive {
			store.recordFailedAdminLogin(ctx, id, username, sourceIP, failed, lockedUntil, now)
		} else {
			_ = store.recordLoginAttempt(ctx, "admin:"+username, sourceIP, false)
		}
		return AdminUser{}, ErrInvalidCredentials
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE admin_users SET failed_attempts=0,locked_until=NULL WHERE id=?", id); err != nil {
		return AdminUser{}, fmt.Errorf("record administrator login: %w", err)
	}
	admin, err := store.GetAdmin(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}
	_ = store.recordLoginAttempt(ctx, "admin:"+username, sourceIP, true)
	_ = store.recordAudit(ctx, &admin.ID, "admin_login_success", "", sourceIP, "")
	return admin, nil
}

func (store *Store) CreateSession(ctx context.Context, adminID int64, lifetime time.Duration) (string, time.Time, error) {
	if lifetime <= 0 {
		lifetime = adminSessionLifetime
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	now := store.now().UTC()
	expires := now.Add(lifetime)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,admin_user_id,created_at,expires_at) VALUES(?,?,?,?)`, hex.EncodeToString(hash[:]), adminID, store.timestamp(now), store.timestamp(expires)); err != nil {
		return "", time.Time{}, err
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", store.timestamp(now))
	return token, expires, nil
}

func (store *Store) ValidateSession(ctx context.Context, token string) (Session, error) {
	if len(token) < 32 {
		return Session{}, ErrInvalidCredentials
	}
	hash := sha256.Sum256([]byte(token))
	var session Session
	var expires string
	err := store.db.QueryRowContext(ctx, `
SELECT s.id,s.admin_user_id,a.username,a.role,s.expires_at
FROM sessions s JOIN admin_users a ON a.id=s.admin_user_id
WHERE s.token_hash=? AND a.status=?`, hex.EncodeToString(hash[:]), AdminActive).Scan(&session.ID, &session.AdminUserID, &session.Username, &session.Role, &expires)
	if err != nil {
		return Session{}, ErrInvalidCredentials
	}
	parsed, err := parseTimestamp(expires)
	if err != nil || parsed.Before(store.now()) {
		_, _ = store.db.ExecContext(ctx, "DELETE FROM sessions WHERE id=?", session.ID)
		return Session{}, ErrInvalidCredentials
	}
	session.ExpiresAt = parsed
	return session, nil
}

func (store *Store) DeleteSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := store.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", hex.EncodeToString(hash[:]))
	return err
}

func (store *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id,actor_admin_id,event,COALESCE(username,''),COALESCE(source_ip,''),COALESCE(detail,''),created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var actor sql.NullInt64
		var created string
		if err := rows.Scan(&event.ID, &actor, &event.Event, &event.Username, &event.SourceIP, &event.Detail, &created); err != nil {
			return nil, err
		}
		if actor.Valid {
			id := actor.Int64
			event.ActorID = &id
		}
		event.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (store *Store) recordFailedLogin(ctx context.Context, account Account, sourceIP string, now time.Time) {
	if account.LockedUntil != nil && account.LockedUntil.After(now) {
		_ = store.recordLoginAttempt(ctx, account.Username, sourceIP, false)
		_ = store.recordAudit(ctx, nil, "game_login_failed", account.Username, sourceIP, "locked")
		return
	}
	failed := account.FailedAttempts + 1
	var locked any
	if failed >= maxLoginFailures {
		locked = store.timestamp(now.Add(loginLockDuration))
		failed = 0
	}
	_, _ = store.db.ExecContext(ctx, "UPDATE accounts SET failed_attempts=?,locked_until=?,updated_at=? WHERE id=?", failed, locked, store.timestamp(now), account.ID)
	_ = store.recordLoginAttempt(ctx, account.Username, sourceIP, false)
	_ = store.recordAudit(ctx, nil, "game_login_failed", account.Username, sourceIP, "")
}

func (store *Store) recordFailedAdminLogin(ctx context.Context, id int64, username, sourceIP string, previous int, existingLock *time.Time, now time.Time) {
	if existingLock != nil && existingLock.After(now) {
		_ = store.recordLoginAttempt(ctx, "admin:"+username, sourceIP, false)
		_ = store.recordAudit(ctx, &id, "admin_login_failed", "", sourceIP, "locked")
		return
	}
	failed := previous + 1
	var locked any
	if failed >= maxLoginFailures {
		locked = store.timestamp(now.Add(loginLockDuration))
		failed = 0
	}
	_, _ = store.db.ExecContext(ctx, "UPDATE admin_users SET failed_attempts=?,locked_until=? WHERE id=?", failed, locked, id)
	_ = store.recordLoginAttempt(ctx, "admin:"+username, sourceIP, false)
	_ = store.recordAudit(ctx, &id, "admin_login_failed", "", sourceIP, "")
}

func (store *Store) recordLoginAttempt(ctx context.Context, username, sourceIP string, successful bool) error {
	_, err := store.db.ExecContext(ctx, "INSERT INTO login_attempts(username,source_ip,successful,created_at) VALUES(?,?,?,?)", username, sourceIP, boolInt(successful), store.timestamp(store.now()))
	return err
}

func (store *Store) recordAudit(ctx context.Context, actorID *int64, event, username, sourceIP, detail string) error {
	var actor any
	if actorID != nil {
		actor = *actorID
	}
	_, err := store.db.ExecContext(ctx, "INSERT INTO audit_events(actor_admin_id,event,username,source_ip,detail,created_at) VALUES(?,?,?,?,?,?)", actor, event, username, sourceIP, detail, store.timestamp(store.now()))
	return err
}

// RecordAudit records a non-account administrative event, such as a config
// change or a controlled service restart.
func (store *Store) RecordAudit(ctx context.Context, actorID *int64, event, username, sourceIP, detail string) error {
	return store.recordAudit(ctx, actorID, event, username, sourceIP, detail)
}

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var account Account
	var mustChange, failed int
	var created, updated string
	var lastLogin, locked sql.NullString
	if err := row.Scan(&account.ID, &account.Username, &account.Status, &mustChange, &created, &updated, &lastLogin, &failed, &locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, ErrNotFound
		}
		return Account{}, err
	}
	var err error
	account.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return Account{}, err
	}
	account.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return Account{}, err
	}
	account.MustChangePassword = mustChange != 0
	account.FailedAttempts = failed
	if lastLogin.Valid && lastLogin.String != "" {
		value, parseErr := parseTimestamp(lastLogin.String)
		if parseErr != nil {
			return Account{}, parseErr
		}
		account.LastLoginAt = &value
	}
	if locked.Valid && locked.String != "" {
		value, parseErr := parseTimestamp(locked.String)
		if parseErr != nil {
			return Account{}, parseErr
		}
		account.LockedUntil = &value
	}
	return account, nil
}

func scanAdmin(row interface{ Scan(...any) error }) (AdminUser, error) {
	var admin AdminUser
	var created string
	var locked sql.NullString
	if err := row.Scan(&admin.ID, &admin.Username, &admin.Role, &admin.Status, &created, &admin.FailedAttempts, &locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminUser{}, ErrNotFound
		}
		return AdminUser{}, err
	}
	var err error
	admin.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return admin, err
	}
	admin.LockedUntil, err = nullableTimestamp(locked)
	return admin, err
}

func nullableTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (store *Store) timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTimestamp(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

// MarshalJSON keeps accidental future API responses from exposing internal
// database details while still allowing the web layer to serialize accounts.
func (account Account) MarshalJSON() ([]byte, error) {
	type publicAccount struct {
		ID                 int64      `json:"id"`
		Username           string     `json:"username"`
		Status             string     `json:"status"`
		MustChangePassword bool       `json:"must_change_password"`
		CreatedAt          time.Time  `json:"created_at"`
		UpdatedAt          time.Time  `json:"updated_at"`
		LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
	}
	return json.Marshal(publicAccount{account.ID, account.Username, account.Status, account.MustChangePassword, account.CreatedAt, account.UpdatedAt, account.LastLoginAt})
}
