package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "stoneage-auth.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAccountAuthenticationAndLockout(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.CreateAccount(ctx, "probe", []byte("local")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, "probe", []byte("wrong"), "127.0.0.1"); err == nil {
		t.Fatal("wrong password unexpectedly accepted")
	}
	account, err := store.Authenticate(ctx, "probe", []byte("local"), "127.0.0.1")
	if err != nil || account.Username != "probe" {
		t.Fatalf("authenticate = %#v, %v", account, err)
	}
	for i := 0; i < maxLoginFailures; i++ {
		_, _ = store.Authenticate(ctx, "probe", []byte("wrong"), "127.0.0.1")
	}
	account, err = store.GetAccountByUsername(ctx, "probe")
	if err != nil {
		t.Fatal(err)
	}
	if account.LockedUntil == nil || !account.LockedUntil.After(time.Now()) {
		t.Fatalf("account was not locked: %#v", account)
	}
	if _, err := store.Authenticate(ctx, "probe", []byte("local"), "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("locked account error = %v", err)
	}
	if err := store.SetAccountPassword(ctx, account.ID, []byte("newpass"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, "probe", []byte("newpass"), "127.0.0.1"); err != nil {
		t.Fatalf("reset password did not unlock account: %v", err)
	}
}

func TestGameAccountNamesAreCaseInsensitive(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	account, err := store.CreateAccount(ctx, "  ProBe  ", []byte("local"))
	if err != nil {
		t.Fatal(err)
	}
	if account.Username != "probe" {
		t.Fatalf("stored username = %q, want probe", account.Username)
	}
	if _, err := store.Authenticate(ctx, "PROBE", []byte("local"), "127.0.0.1"); err != nil {
		t.Fatalf("uppercase account login failed: %v", err)
	}
}

func TestAdminAuthenticationLockoutAndSession(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	admin, err := store.CreateAdmin(ctx, "admin", []byte("secret123"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxLoginFailures; i++ {
		if _, err := store.AuthenticateAdmin(ctx, "admin", []byte("badpass"), "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("bad admin password error = %v", err)
		}
	}
	if _, err := store.AuthenticateAdmin(ctx, "admin", []byte("secret123"), "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("locked administrator accepted: %v", err)
	}
	locked, err := store.GetAdmin(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if locked.LockedUntil == nil {
		t.Fatal("administrator was not locked")
	}
	// A fresh administrator can create and validate a session; use a second
	// account so the lockout assertion above remains meaningful.
	second, err := store.CreateAdmin(ctx, "operator", []byte("secret456"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateAdmin(ctx, second.Username, []byte("secret456"), "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	token, expires, err := store.CreateSession(ctx, second.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if expires.Before(time.Now()) {
		t.Fatal("session expiry is in the past")
	}
	validated, err := store.ValidateSession(ctx, token)
	if err != nil || validated.AdminUserID != second.ID {
		t.Fatalf("session = %#v, %v", validated, err)
	}
	if err := store.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateSession(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("deleted session error = %v", err)
	}
}

func TestAuthDatabasePermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if sidecar, statErr := os.Stat(path + suffix); statErr == nil && sidecar.Mode().Perm() != 0o600 {
			t.Fatalf("database %s mode = %o, want 600", suffix, sidecar.Mode().Perm())
		}
	}
}

func TestConcurrentMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()
	var group sync.WaitGroup
	errorsFound := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		group.Add(1)
		go func(store *Store) {
			defer group.Done()
			errorsFound <- store.Migrate(context.Background())
		}(store)
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent migration failed: %v", err)
		}
	}
}
