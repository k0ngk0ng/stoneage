package aiservice

import (
	"context"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// HasUnknownStateChange fences character-state decisions without treating a
// message lacking a sender acknowledgement as an unresolved stat mutation.
// Message receipts remain unknown, visible, and subject to no-replay rules.
// Query the entire history: a recent display limit must not hide an old write.
func (s *ReceiptStore) HasUnknownStateChange(ctx context.Context, b aimcp.Binding, exceptHandle string) (bool, error) {
	var present bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_game_receipts
		WHERE account=? AND character_id=? AND profile_id=? AND handle<>? AND json_extract(receipt,'$.status')='unknown'
		AND NOT (
			COALESCE(json_extract(action,'$.kind'),'')='chat'
			OR (COALESCE(json_extract(action,'$.kind'),'')='mail'
				AND COALESCE(json_extract(action,'$.command'),'') IN ('list','send'))
		))`, b.AccountID, b.CharacterID, b.ProfileID, exceptHandle).Scan(&present)
	return present, err
}
