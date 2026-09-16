package aiservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	_ "modernc.org/sqlite"
)

// ReceiptStore is a write-ahead ledger, not a replay queue. On restart a
// prepared action remains unknown until its game-specific result is proven.
type ReceiptStore struct{ db *sql.DB }

func OpenReceiptStore(path string) (*ReceiptStore, error) {
	if path == "" {
		return nil, errors.New("receipt database path required")
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
			return nil, errors.New("receipt database must be a regular file")
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
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000", `CREATE TABLE IF NOT EXISTS ai_game_receipts (handle TEXT PRIMARY KEY,account TEXT NOT NULL,character_id TEXT NOT NULL,generation INTEGER NOT NULL,profile_id TEXT NOT NULL DEFAULT '',action BLOB NOT NULL,receipt BLOB NOT NULL)`, `CREATE TABLE IF NOT EXISTS ai_game_action_keys (operation_key TEXT PRIMARY KEY,handle TEXT NOT NULL)`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Databases created before profile scoping do not have this column. The
	// empty default preserves the old binding for existing rows while new
	// operations carry their profile in both the row and operation key.
	var hasProfile bool
	rows, err := db.Query("PRAGMA table_info(ai_game_receipts)")
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if name == "profile_id" {
			hasProfile = true
		}
	}
	if err := rows.Close(); err != nil {
		db.Close()
		return nil, err
	}
	if !hasProfile {
		if _, err := db.Exec(`ALTER TABLE ai_game_receipts ADD COLUMN profile_id TEXT NOT NULL DEFAULT ''`); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &ReceiptStore{db: db}, nil
}
func (s *ReceiptStore) Close() error { return s.db.Close() }
func (s *ReceiptStore) Prepare(ctx context.Context, b aimcp.Binding, a aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	r, _, err := s.PrepareOnce(ctx, b, a)
	return r, err
}

// PrepareOnce deduplicates the same action chosen from the same observation.
// A tool-call retry can recover its original receipt without another write.
func (s *ReceiptStore) PrepareOnce(ctx context.Context, b aimcp.Binding, a aimcp.TypedAction) (aimcp.ActionReceipt, bool, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	r := aimcp.ActionReceipt{Handle: "action-" + hex.EncodeToString(nonce[:]), Status: aimcp.ReceiptUnknown, Reason: "submission prepared; delivery and game outcome are not confirmed"}
	r.Evidence, _ = json.Marshal(map[string]any{"requested_action": a, "game_outcome_confirmed": false})
	action, err := json.Marshal(a)
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	keyBytes, _ := json.Marshal(struct {
		Profile, Account, Character string
		Generation                  uint64
		Action                      aimcp.TypedAction
	}{b.ProfileID, b.AccountID, b.CharacterID, b.Generation, a})
	digest := sha256.Sum256(keyBytes)
	key := hex.EncodeToString(digest[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	defer tx.Rollback()
	insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ai_game_action_keys(operation_key,handle) VALUES(?,?)`, key, r.Handle)
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	n, err := insert.RowsAffected()
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	if n == 0 {
		var existing []byte
		err = tx.QueryRowContext(ctx, `SELECT r.receipt FROM ai_game_receipts r JOIN ai_game_action_keys k ON k.handle=r.handle WHERE k.operation_key=?`, key).Scan(&existing)
		if err != nil {
			return aimcp.ActionReceipt{}, false, err
		}
		if err = json.Unmarshal(existing, &r); err != nil {
			return aimcp.ActionReceipt{}, false, err
		}
		return r, false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_game_receipts(handle,account,character_id,generation,profile_id,action,receipt) VALUES(?,?,?,?,?,?,?)`, r.Handle, b.AccountID, b.CharacterID, b.Generation, b.ProfileID, action, raw)
	if err != nil {
		return aimcp.ActionReceipt{}, false, err
	}
	return r, true, tx.Commit()
}
func (s *ReceiptStore) Load(ctx context.Context, b aimcp.Binding, id string) (aimcp.ActionReceipt, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT receipt FROM ai_game_receipts WHERE handle=? AND account=? AND character_id=? AND profile_id=?`, id, b.AccountID, b.CharacterID, b.ProfileID).Scan(&raw)
	if err != nil {
		return aimcp.ActionReceipt{}, err
	}
	var r aimcp.ActionReceipt
	err = json.Unmarshal(raw, &r)
	return r, err
}

// LoadAction loads the original typed action together with its durable
// receipt. The generation is deliberately not part of the lookup: a fresh
// control lease may inspect an older uncertain operation, then reconciliation
// compares the generation carried by its evidence before confirming anything.
func (s *ReceiptStore) LoadAction(ctx context.Context, b aimcp.Binding, id string) (aimcp.TypedAction, aimcp.ActionReceipt, error) {
	var actionRaw, receiptRaw []byte
	err := s.db.QueryRowContext(ctx, `SELECT action, receipt FROM ai_game_receipts
		WHERE handle=? AND account=? AND character_id=? AND profile_id=?`, id, b.AccountID, b.CharacterID, b.ProfileID).Scan(&actionRaw, &receiptRaw)
	if err != nil {
		return aimcp.TypedAction{}, aimcp.ActionReceipt{}, err
	}
	var action aimcp.TypedAction
	if err := json.Unmarshal(actionRaw, &action); err != nil {
		return aimcp.TypedAction{}, aimcp.ActionReceipt{}, err
	}
	var receipt aimcp.ActionReceipt
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		return aimcp.TypedAction{}, aimcp.ActionReceipt{}, err
	}
	return action, receipt, nil
}

func (s *ReceiptStore) Unknown(ctx context.Context, b aimcp.Binding) ([]aimcp.ActionReceipt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT receipt FROM ai_game_receipts WHERE account=? AND character_id=? AND profile_id=? AND json_extract(receipt,'$.status')='unknown' ORDER BY rowid DESC LIMIT 128`, b.AccountID, b.CharacterID, b.ProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []aimcp.ActionReceipt{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r aimcp.ActionReceipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// HasUnknown is a decision query, separate from the bounded display list.
// Excluding a newly prepared receipt permits checking for any earlier unknown
// action without loading or truncating the character's action history.
func (s *ReceiptStore) HasUnknown(ctx context.Context, b aimcp.Binding, exceptHandle string) (bool, error) {
	var present bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_game_receipts
		WHERE account=? AND character_id=? AND profile_id=? AND handle<>? AND json_extract(receipt,'$.status')='unknown')`,
		b.AccountID, b.CharacterID, b.ProfileID, exceptHandle).Scan(&present)
	return present, err
}
func (s *ReceiptStore) Save(ctx context.Context, b aimcp.Binding, r aimcp.ActionReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE ai_game_receipts SET receipt=? WHERE handle=? AND account=? AND character_id=? AND profile_id=? AND generation=?`, raw, r.Handle, b.AccountID, b.CharacterID, b.ProfileID, b.Generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("receipt binding changed")
	}
	return nil
}
