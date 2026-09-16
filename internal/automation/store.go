package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct{ db *sql.DB }

func OpenStore(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("automation database path required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			_ = f.Close()
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("automation database must be a regular file")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000", `CREATE TABLE IF NOT EXISTS automation_plans (id TEXT PRIMARY KEY, character_id TEXT NOT NULL, revision INTEGER NOT NULL, status TEXT NOT NULL CHECK(status IN ('running','paused','completed')), body BLOB NOT NULL)`, `CREATE UNIQUE INDEX IF NOT EXISTS automation_one_active_character ON automation_plans(character_id) WHERE status='running'`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }
func (s *SQLiteStore) Create(ctx context.Context, c Checkpoint) error {
	if err := c.Plan.Validate(); err != nil {
		return err
	}
	if c.Revision != 1 {
		return errors.New("new checkpoint revision must be one")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_plans(id,character_id,revision,status,body) VALUES(?,?,?,?,?)`, c.Plan.ID, c.Plan.CharacterID, c.Revision, c.Status, data)
	if err != nil {
		return fmt.Errorf("create automation checkpoint: %w", err)
	}
	return nil
}
func (s *SQLiteStore) Load(ctx context.Context, id string) (Checkpoint, error) {
	var data []byte
	var c Checkpoint
	if err := s.db.QueryRowContext(ctx, `SELECT body FROM automation_plans WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}
func (s *SQLiteStore) Save(ctx context.Context, c Checkpoint, expected uint64) error {
	if expected == 0 || c.Revision != expected+1 {
		return ErrConflict
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, `UPDATE automation_plans SET revision=?,status=?,body=? WHERE id=? AND character_id=? AND revision=?`, c.Revision, c.Status, data, c.Plan.ID, c.Plan.CharacterID, expected)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *SQLiteStore) List(ctx context.Context, characterID string) ([]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM automation_plans WHERE character_id=? ORDER BY rowid DESC`, characterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Checkpoint{}
	for rows.Next() {
		var raw []byte
		var c Checkpoint
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ListRecoveryHistory includes terminal rows so they can supersede older
// unfinished runs during startup reconciliation. Callers must
// establish exclusive ownership of their runner before changing any row.
func (s *SQLiteStore) ListRecoveryHistory(ctx context.Context) ([]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM automation_plans ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Checkpoint
	for rows.Next() {
		var raw []byte
		var checkpoint Checkpoint
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &checkpoint); err != nil {
			return nil, err
		}
		result = append(result, checkpoint)
	}
	return result, rows.Err()
}
