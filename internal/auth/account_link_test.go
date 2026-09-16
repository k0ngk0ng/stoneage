package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestCreateAccountJournalLinkIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.DB().Exec(`CREATE TABLE creation_journal(id TEXT PRIMARY KEY, account_id INTEGER REFERENCES accounts(id)); INSERT INTO creation_journal(id) VALUES('new-ai')`); err != nil {
		t.Fatal(err)
	}
	reject := errors.New("journal unavailable")
	if _, err := store.CreateAccountLinkedAs(ctx, nil, "linked_ai", []byte("test-pass"), func(tx *sql.Tx, id int64) error {
		if _, err := tx.ExecContext(ctx, "UPDATE creation_journal SET account_id=? WHERE id='new-ai'", id); err != nil {
			return err
		}
		return reject
	}); !errors.Is(err, reject) {
		t.Fatalf("callback error lost: %v", err)
	}
	if _, err := store.GetAccountByUsername(ctx, "linked_ai"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("account survived rolled-back link: %v", err)
	}
	var account sql.NullInt64
	if err := store.DB().QueryRow("SELECT account_id FROM creation_journal WHERE id='new-ai'").Scan(&account); err != nil || account.Valid {
		t.Fatal("journal survived account rollback")
	}
	created, err := store.CreateAccountLinkedAs(ctx, nil, "linked_ai", []byte("test-pass"), func(tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, "UPDATE creation_journal SET account_id=? WHERE id='new-ai'", id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow("SELECT account_id FROM creation_journal WHERE id='new-ai'").Scan(&account); err != nil || !account.Valid || account.Int64 != created.ID {
		t.Fatal("committed account has no journal link")
	}
	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM audit_events WHERE event='account_created' AND username='linked_ai'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("account audit not atomic: %d %v", count, err)
	}
}
