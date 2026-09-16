package airuntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// AgentNote is a private, profile-scoped note that an agent may use to keep
// track of its own plans and observations. It is deliberately separate from
// Memory: notes are model-authored working state and never become confirmed
// game facts.
type AgentNote struct {
	ID        int64     `json:"id"`
	ProfileID string    `json:"profile_id"`
	Key       string    `json:"key"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	// Agent note limits bound both durable state and the amount of self-authored
	// text a caller can later include in a model prompt.
	MaxAgentNoteKeyBytes  = 128
	MaxAgentNoteTextBytes = 4096

	DefaultAgentNoteLimit = 50
	MaxAgentNoteLimit     = 100
)

var errAgentNotesUnavailable = errors.New("airuntime: agent note store is unavailable")

// UpsertAgentNote creates or replaces one note key within profileID. The
// profile is checked in the same transaction as the write, so a deleted
// profile cannot be recreated indirectly and notes cannot cross profile
// boundaries. Updating a key preserves its creation time and row ID.
func (store *Store) UpsertAgentNote(ctx context.Context, profileID, key, text string) (AgentNote, error) {
	if store == nil || store.db == nil {
		return AgentNote{}, errAgentNotesUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	profileID, key, text, err := normalizeAgentNoteInput(profileID, key, text)
	if err != nil {
		return AgentNote{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentNote{}, fmt.Errorf("begin AI agent note upsert: %w", err)
	}
	defer tx.Rollback()
	if _, err := scanProfileTx(ctx, tx, profileID); err != nil {
		return AgentNote{}, err
	}
	now := store.timestamp(store.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_agent_notes
		(profile_id, note_key, note_text, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, note_key) DO UPDATE SET
		  note_text=excluded.note_text, updated_at=excluded.updated_at`,
		profileID, key, text, now, now); err != nil {
		return AgentNote{}, fmt.Errorf("upsert AI agent note: %w", err)
	}
	note, err := scanAgentNoteTx(ctx, tx, profileID, key)
	if err != nil {
		return AgentNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentNote{}, fmt.Errorf("commit AI agent note upsert: %w", err)
	}
	return note, nil
}

// ListAgentNotes returns the newest notes for one profile. A non-positive
// limit uses DefaultAgentNoteLimit; larger requests are capped to keep note
// recall bounded.
func (store *Store) ListAgentNotes(ctx context.Context, profileID string, limit int) ([]AgentNote, error) {
	if store == nil || store.db == nil {
		return nil, errAgentNotesUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, errors.New("airuntime: agent note profile id is required")
	}
	if !utf8.ValidString(profileID) {
		return nil, errors.New("airuntime: agent note profile id is invalid")
	}
	if limit <= 0 {
		limit = DefaultAgentNoteLimit
	}
	if limit > MaxAgentNoteLimit {
		limit = MaxAgentNoteLimit
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, profile_id, note_key,
		note_text, created_at, updated_at FROM ai_agent_notes
		WHERE profile_id=? ORDER BY updated_at DESC, id DESC LIMIT ?`, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list AI agent notes: %w", err)
	}
	return scanAgentNotes(rows)
}

// DeleteAgentNote removes one key from one profile. Missing profiles and
// missing keys both return ErrNotFound, and a key belonging to another
// profile is therefore never observable or deletable through this method.
func (store *Store) DeleteAgentNote(ctx context.Context, profileID, key string) error {
	if store == nil || store.db == nil {
		return errAgentNotesUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Deletion does not use note text, so only validate the two fields it
	// accepts. Keeping the same key rules as upsert avoids ambiguous keys.
	profileID, key, err := normalizeAgentNoteKey(profileID, key)
	if err != nil {
		return err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin AI agent note delete: %w", err)
	}
	defer tx.Rollback()
	if _, err := scanProfileTx(ctx, tx, profileID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM ai_agent_notes
		WHERE profile_id=? AND note_key=?`, profileID, key)
	if err != nil {
		return fmt.Errorf("delete AI agent note: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check AI agent note delete: %w", err)
	}
	if count != 1 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit AI agent note delete: %w", err)
	}
	return nil
}

func normalizeAgentNoteInput(profileID, key, text string) (string, string, string, error) {
	profileID, key, err := normalizeAgentNoteKey(profileID, key)
	if err != nil {
		return "", "", "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", "", errors.New("airuntime: agent note text is required")
	}
	if !utf8.ValidString(text) || len([]byte(text)) > MaxAgentNoteTextBytes || !safeAgentNoteText(text) {
		return "", "", "", errors.New("airuntime: agent note text is invalid or too large")
	}
	return profileID, key, text, nil
}

func normalizeAgentNoteKey(profileID, key string) (string, string, error) {
	profileID = strings.TrimSpace(profileID)
	key = strings.TrimSpace(key)
	if profileID == "" {
		return "", "", errors.New("airuntime: agent note profile id is required")
	}
	if !utf8.ValidString(profileID) {
		return "", "", errors.New("airuntime: agent note profile id is invalid")
	}
	if key == "" {
		return "", "", errors.New("airuntime: agent note key is required")
	}
	if !utf8.ValidString(key) || len([]byte(key)) > MaxAgentNoteKeyBytes || !safeAgentNoteText(key) || strings.ContainsAny(key, "\r\n\t") {
		return "", "", errors.New("airuntime: agent note key is invalid or too large")
	}
	return profileID, key, nil
}

func safeAgentNoteText(value string) bool {
	for _, character := range value {
		if character == 0 || (character < 0x20 && character != '\t' && character != '\n' && character != '\r') {
			return false
		}
	}
	return true
}

func scanAgentNoteTx(ctx context.Context, tx *sql.Tx, profileID, key string) (AgentNote, error) {
	var note AgentNote
	var created, updated string
	err := tx.QueryRowContext(ctx, `SELECT id, profile_id, note_key,
		note_text, created_at, updated_at FROM ai_agent_notes
		WHERE profile_id=? AND note_key=?`, profileID, key).Scan(
		&note.ID, &note.ProfileID, &note.Key, &note.Text, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentNote{}, ErrNotFound
	}
	if err != nil {
		return AgentNote{}, fmt.Errorf("read AI agent note: %w", err)
	}
	note.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return AgentNote{}, err
	}
	note.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return AgentNote{}, err
	}
	return note, nil
}

func scanAgentNotes(rows *sql.Rows) ([]AgentNote, error) {
	defer rows.Close()
	notes := make([]AgentNote, 0)
	for rows.Next() {
		var note AgentNote
		var created, updated string
		if err := rows.Scan(&note.ID, &note.ProfileID, &note.Key, &note.Text, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan AI agent note: %w", err)
		}
		var err error
		note.CreatedAt, err = parseTimestamp(created)
		if err != nil {
			return nil, err
		}
		note.UpdatedAt, err = parseTimestamp(updated)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AI agent notes: %w", err)
	}
	return notes, nil
}
