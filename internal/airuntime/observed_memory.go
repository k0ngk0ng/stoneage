package airuntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// These limits apply to the server-observation path.  Observations are fed
// back into future model prompts, so a malformed game field must not be able
// to turn that path into an unbounded storage or prompt input.
const (
	MaxObservedEventKeyBytes = 512
	MaxObservedKindBytes     = 128
	MaxObservedSubjectBytes  = 256
	MaxObservedContentBytes  = 16 * 1024
)

var observedKindPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// RecordGameObservation records one fact returned by the trusted game
// observation boundary. eventKey is hashed before persistence and the hash
// is unique per profile. The dedupe claim, audit event, and confirmed memory
// are committed as one SQLite transaction, so a retry cannot leave a partial
// memory behind.
//
// content accepts JSON-compatible values as a convenience for callers. A
// string which is valid JSON is retained as structured JSON; other strings
// become JSON strings. The function deliberately rejects credential-shaped
// fields and raw protocol material because this path is for game facts only.
func (store *Store) RecordGameObservation(ctx context.Context, profileID, eventKey, kind, subject string, content any) (created bool, err error) {
	if store == nil || store.db == nil {
		return false, errors.New("airuntime: observation store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	profileID = strings.TrimSpace(profileID)
	eventKey = strings.TrimSpace(eventKey)
	kind = strings.TrimSpace(kind)
	subject = strings.TrimSpace(subject)
	if profileID == "" {
		return false, errors.New("airuntime: observation profile id is required")
	}
	if eventKey == "" || len([]byte(eventKey)) > MaxObservedEventKeyBytes || !safeObservedText(eventKey) {
		return false, errors.New("airuntime: observation event key is invalid")
	}
	if kind == "" || len([]byte(kind)) > MaxObservedKindBytes || !observedKindPattern.MatchString(kind) || !safeObservedKind(kind) {
		return false, errors.New("airuntime: observation kind is invalid")
	}
	if len([]byte(subject)) > MaxObservedSubjectBytes || !safeObservedText(subject) || sensitiveObservedText(subject) || inferredObservedText(subject) {
		return false, errors.New("airuntime: observation subject is invalid")
	}
	contentJSON, err := normalizeObservedContent(content)
	if err != nil {
		return false, err
	}
	hash := stableObservationEventKey(eventKey)
	// Check outside the write transaction so a missing profile reports the
	// same stable error as the other Store write APIs. The in-transaction
	// check below closes the deletion race before the memory insert.
	if _, err := store.GetProfile(ctx, profileID); err != nil {
		return false, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin game observation: %w", err)
	}
	defer tx.Rollback()
	createdAt := store.timestamp(store.now())
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ai_observation_dedupe
        (profile_id, event_key, created_at) VALUES(?, ?, ?)`, profileID, hash, createdAt)
	if err != nil {
		return false, fmt.Errorf("claim game observation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check game observation claim: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	if _, err := scanProfileTx(ctx, tx, profileID); err != nil {
		return false, err
	}

	detail, err := json.Marshal(struct {
		EventKey string          `json:"event_key"`
		Kind     string          `json:"kind"`
		Subject  string          `json:"subject,omitempty"`
		Content  json.RawMessage `json:"content"`
	}{EventKey: hash, Kind: kind, Subject: subject, Content: contentJSON})
	if err != nil {
		return false, fmt.Errorf("marshal game observation event: %w", err)
	}
	event, err := store.recordEventTx(ctx, tx, profileID, EventGameObservation, "game", detail)
	if err != nil {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO ai_memories
        (profile_id, kind, subject, content_json, source_event_id, confirmed, created_at)
        VALUES(?, ?, ?, ?, ?, 1, ?)`, profileID, kind, subject, string(contentJSON), event.ID, createdAt)
	if err != nil {
		return false, fmt.Errorf("record game observation memory: %w", err)
	}
	memoryID, err := result.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("read game observation memory id: %w", err)
	}
	linked, err := tx.ExecContext(ctx, `UPDATE ai_observation_dedupe
		SET event_id=?, memory_id=? WHERE profile_id=? AND event_key=?`, event.ID, memoryID, profileID, hash)
	if err != nil {
		return false, fmt.Errorf("link game observation: %w", err)
	}
	if count, err := linked.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return false, fmt.Errorf("check linked game observation: %w", err)
		}
		return false, errors.New("airuntime: game observation dedupe key disappeared")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit game observation: %w", err)
	}
	return true, nil
}

// StableGameObservationEventKey is useful to observation adapters which need
// to derive the same dedupe key without exposing the raw event identity. It
// returns a lowercase SHA-256 digest and never embeds account credentials or
// protocol data in the returned value.
func StableGameObservationEventKey(eventKey string) string {
	return stableObservationEventKey(strings.TrimSpace(eventKey))
}

func stableObservationEventKey(eventKey string) string {
	digest := sha256.Sum256([]byte(eventKey))
	return hex.EncodeToString(digest[:])
}

func normalizeObservedContent(content any) ([]byte, error) {
	var raw []byte
	switch value := content.(type) {
	case json.RawMessage:
		raw = append([]byte(nil), value...)
	case []byte:
		raw = append([]byte(nil), value...)
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed != "" && json.Valid([]byte(trimmed)) {
			raw = []byte(trimmed)
		} else {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, errors.New("airuntime: observation content is not JSON")
			}
			raw = encoded
		}
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("airuntime: observation content is not JSON")
		}
		raw = encoded
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxObservedContentBytes || !json.Valid(raw) {
		return nil, errors.New("airuntime: observation content is invalid or too large")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("airuntime: observation content is invalid")
	}
	if decoder.More() {
		return nil, errors.New("airuntime: observation content has trailing data")
	}
	if err := validateObservedJSON(value); err != nil {
		return nil, err
	}
	return append([]byte(nil), raw...), nil
}

func validateObservedJSON(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if !safeObservedFieldName(key) {
				return errors.New("airuntime: observation content contains a restricted field")
			}
			if err := validateObservedJSON(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateObservedJSON(child); err != nil {
				return err
			}
		}
	case string:
		if !safeObservedText(value) || sensitiveObservedText(value) {
			return errors.New("airuntime: observation content contains restricted text")
		}
	}
	return nil
}

func safeObservedKind(value string) bool {
	return !inferredObservedText(value)
}

func safeObservedFieldName(value string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(value))
	for _, term := range []string{
		"account", "account_id", "username", "password", "passwd", "secret", "api_key", "apikey",
		"token", "credential", "authorization", "cookie", "raw", "raw_packet", "packet", "wire",
		"network", "socket", "relationship", "friend", "friendship", "trust", "sentiment", "opinion",
		"rating", "evaluation", "inference",
	} {
		if normalized == term || strings.HasPrefix(normalized, term+"_") || strings.HasSuffix(normalized, "_"+term) || strings.Contains(normalized, "_"+term+"_") {
			return false
		}
	}
	return safeObservedText(value)
}

func safeObservedText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\t' && character != '\n' && character != '\r' {
			return false
		}
	}
	return true
}

func sensitiveObservedText(value string) bool {
	lower := strings.ToLower(value)
	for _, term := range []string{
		"password=", "passwd=", "api_key=", "apikey=", "secret=", "token=", "bearer ",
		"authorization:", "raw_packet", "packet_hex", "lssproto", "opcode=",
	} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func inferredObservedText(value string) bool {
	lower := strings.ToLower(value)
	for _, term := range []string{"relation", "friend", "trust", "sentiment", "opinion", "rating", "evaluation", "inference"} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}
