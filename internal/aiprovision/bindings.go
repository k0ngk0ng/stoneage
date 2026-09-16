package aiprovision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

const bindingSchema = `
CREATE TABLE IF NOT EXISTS ai_initial_states (profile_id TEXT PRIMARY KEY, account_id INTEGER REFERENCES accounts(id), record_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ai_game_bindings (
  profile_id TEXT PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id),
  account_username TEXT NOT NULL,
  character_slot INTEGER NOT NULL CHECK (character_slot IN (0,1)),
  character_id TEXT NOT NULL,
  character_name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(account_id, character_slot),
  UNIQUE(account_id),
  UNIQUE(character_id)
);
CREATE INDEX IF NOT EXISTS ai_game_bindings_account_idx ON ai_game_bindings(account_id);
CREATE TRIGGER IF NOT EXISTS ai_game_bindings_immutable_update
BEFORE UPDATE ON ai_game_bindings
BEGIN
  SELECT RAISE(ABORT, 'AI game bindings are immutable');
END;
CREATE TRIGGER IF NOT EXISTS ai_game_bindings_immutable_delete
BEFORE DELETE ON ai_game_bindings
BEGIN
  SELECT RAISE(ABORT, 'AI game bindings are immutable');
END;
`

func ensureBindingSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: auth database is unavailable", ErrInvalidConfig)
	}
	if _, err := db.ExecContext(ctx, bindingSchema); err != nil {
		return fmt.Errorf("%w: create game binding schema: %v", ErrInvalidConfig, err)
	}
	return nil
}

func insertBinding(ctx context.Context, db *sql.DB, binding Binding) error {
	if err := validateBinding(binding); err != nil {
		return err
	}
	created := binding.CreatedAt.UTC().Format(time.RFC3339Nano)
	if binding.CreatedAt.IsZero() {
		created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO ai_game_bindings
        (profile_id,account_id,account_username,character_slot,character_id,character_name,created_at)
        VALUES(?,?,?,?,?,?,?)`, binding.ProfileID, binding.AccountID, binding.AccountUsername,
		binding.CharacterSlot, binding.CharacterID, binding.CharacterName, created)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrBindingConflict
	}
	return fmt.Errorf("%w: persist game binding: %v", ErrBindingConflict, err)
}

func getBinding(ctx context.Context, db *sql.DB, profileID string) (Binding, error) {
	if strings.TrimSpace(profileID) == "" {
		return Binding{}, ErrBindingNotFound
	}
	var binding Binding
	var created string
	err := db.QueryRowContext(ctx, `SELECT profile_id,account_id,account_username,character_slot,
        character_id,character_name,created_at FROM ai_game_bindings WHERE profile_id=?`, profileID).Scan(
		&binding.ProfileID, &binding.AccountID, &binding.AccountUsername, &binding.CharacterSlot,
		&binding.CharacterID, &binding.CharacterName, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, ErrBindingNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("%w: load game binding: %v", ErrBindingNotFound, err)
	}
	binding.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: invalid game binding timestamp", ErrBindingConflict)
	}
	if err := validateBinding(binding); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func validateBinding(binding Binding) error {
	if !profileIDPattern.MatchString(binding.ProfileID) || binding.AccountID <= 0 ||
		strings.TrimSpace(binding.AccountUsername) == "" || binding.CharacterSlot < 0 || binding.CharacterSlot > 1 ||
		strings.TrimSpace(binding.CharacterID) == "" || len([]byte(binding.CharacterID)) > 128 || strings.TrimSpace(binding.CharacterName) == "" {
		return fmt.Errorf("%w: invalid identity binding", ErrBindingConflict)
	}
	if err := auth.ValidateGameUsername([]byte(binding.AccountUsername)); err != nil {
		return fmt.Errorf("%w: invalid account username", ErrBindingConflict)
	}
	if err := validateCharacterName(binding.CharacterName); err != nil {
		return fmt.Errorf("%w: invalid character name", ErrBindingConflict)
	}
	if strings.ContainsAny(binding.AccountUsername, "\x00\r\n") || strings.ContainsAny(binding.CharacterID, "\x00\r\n") || strings.ContainsAny(binding.CharacterName, "\x00\r\n") {
		return fmt.Errorf("%w: identity binding contains control characters", ErrBindingConflict)
	}
	return nil
}
