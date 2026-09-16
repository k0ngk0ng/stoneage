package airuntime

import (
	"context"
	"fmt"
	"strings"
)

// ListConfirmedSubjectMemories recalls bounded history for an exact durable
// subject within one profile. Callers must not equate transient world/chat
// object IDs with persistent character identities.
func (store *Store) ListConfirmedSubjectMemories(ctx context.Context, profileID, subject string, limit int) ([]Memory, error) {
	if strings.TrimSpace(profileID) == "" || strings.TrimSpace(subject) == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > 32 {
		limit = 32
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, profile_id, kind, subject,
 content_json, source_event_id, confirmed, created_at FROM ai_memories
 WHERE profile_id=? AND subject=? AND confirmed=1 ORDER BY id DESC LIMIT ?`, profileID, subject, limit)
	if err != nil {
		return nil, fmt.Errorf("recall AI subject memories: %w", err)
	}
	return scanMemories(rows)
}
