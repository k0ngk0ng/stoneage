package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestGiftSchemaAndPackageCRUD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	var version int
	if err := store.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}
	for _, table := range []string{"gift_packages", "gift_runs", "gift_deliveries"} {
		var name string
		if err := store.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
			t.Fatalf("gift table %s missing: %v", table, err)
		}
	}

	definition := json.RawMessage(`{"items":[{"id":7,"quantity":2}],"pets":[{"id":9,"quantity":1}]}`)
	packageValue, err := store.CreateGiftPackage(ctx, "  Starter  ", definition)
	if err != nil {
		t.Fatal(err)
	}
	if packageValue.Name != "Starter" || string(packageValue.Definition) != string(definition) {
		t.Fatalf("created package = %#v", packageValue)
	}
	definition[0] = 'x'
	got, err := store.GetGiftPackage(ctx, packageValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Definition) != `{"items":[{"id":7,"quantity":2}],"pets":[{"id":9,"quantity":1}]}` {
		t.Fatalf("stored definition changed through caller buffer: %s", got.Definition)
	}
	if _, err := store.UpdateGiftPackage(ctx, packageValue.ID, "Updated", json.RawMessage(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	packages, err := store.ListGiftPackages(ctx)
	if err != nil || len(packages) != 1 || packages[0].Name != "Updated" {
		t.Fatalf("packages = %#v, %v", packages, err)
	}
	if err := store.DeleteGiftPackage(ctx, packageValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetGiftPackage(ctx, packageValue.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted package error = %v", err)
	}
	if _, err := store.CreateGiftPackage(ctx, "bad", json.RawMessage(`not-json`)); err == nil {
		t.Fatal("invalid package JSON unexpectedly accepted")
	}
}

func TestGiftRunSnapshotClaimCompleteAndIdempotence(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	account, err := store.CreateAccount(ctx, "gift-target", []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	packageValue, err := store.CreateGiftPackage(ctx, "Starter", json.RawMessage(`{"items":[{"id":7,"quantity":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateGiftRun(ctx, packageValue.ID, GiftTargetSingle, 0, []GiftTarget{{
		AccountID: account.ID, CharacterSlot: 1, CharacterName: "角色一",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != GiftRunPending || run.PackageName != "Starter" || string(run.Definition) != `{"items":[{"id":7,"quantity":2}]}` {
		t.Fatalf("run snapshot = %#v", run)
	}
	details, err := store.GetGiftRunDetails(ctx, run.ID, "", 10)
	if err != nil || len(details.Deliveries) != 1 {
		t.Fatalf("details = %#v, %v", details, err)
	}
	deliveryID := details.Deliveries[0].ID
	claimed, err := store.ClaimGiftDelivery(ctx, deliveryID)
	if err != nil || claimed.Status != GiftDeliverySending {
		t.Fatalf("first claim = %#v, %v", claimed, err)
	}
	duplicate, err := store.ClaimGiftDelivery(ctx, deliveryID)
	if !errors.Is(err, ErrGiftDeliveryNotPending) || duplicate.Status != GiftDeliverySending {
		t.Fatalf("duplicate claim = %#v, %v", duplicate, err)
	}
	result := json.RawMessage(`{"item_ids":[101,102]}`)
	completed, err := store.CompleteGiftDelivery(ctx, deliveryID, GiftDeliveryApplied, nil, result, "")
	if err != nil || completed.Status != GiftDeliveryApplied || string(completed.Result) != string(result) {
		t.Fatalf("complete = %#v, %v", completed, err)
	}
	repeated, err := store.CompleteGiftDelivery(ctx, deliveryID, GiftDeliveryApplied, nil, result, "")
	if err != nil || repeated.Status != GiftDeliveryApplied {
		t.Fatalf("idempotent complete = %#v, %v", repeated, err)
	}
	if _, err := store.CompleteGiftDelivery(ctx, deliveryID, GiftDeliveryFailed, nil, nil, "late failure"); !errors.Is(err, ErrGiftDeliveryFinalized) {
		t.Fatalf("terminal overwrite error = %v", err)
	}
	run, err = store.GetGiftRun(ctx, run.ID)
	if err != nil || run.Status != GiftRunCompleted {
		t.Fatalf("run after complete = %#v, %v", run, err)
	}
}

func TestGiftRecoveryLeavesPendingAndMarksSendingUncertain(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	packageValue, err := store.CreateGiftPackage(ctx, "Recovery", json.RawMessage(`{"pets":[{"id":3,"quantity":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var targets []GiftTarget
	for slot := 0; slot < 2; slot++ {
		account, createErr := store.CreateAccount(ctx, fmt.Sprintf("recover-%d", slot), []byte("pass"))
		if createErr != nil {
			t.Fatal(createErr)
		}
		targets = append(targets, GiftTarget{AccountID: account.ID, CharacterSlot: 0})
	}
	run, err := store.CreateGiftRun(ctx, packageValue.ID, GiftTargetAll, 0, targets)
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimNextGiftDelivery(ctx)
	if err != nil || !ok {
		t.Fatalf("claim next = %#v, %v, %v", first, ok, err)
	}
	if count, err := store.RecoverInterruptedGiftDeliveries(ctx); err != nil || count != 1 {
		t.Fatalf("recovery = %d, %v", count, err)
	}
	first, err = store.GetGiftDelivery(ctx, first.ID)
	if err != nil || first.Status != GiftDeliveryUncertain {
		t.Fatalf("recovered delivery = %#v, %v", first, err)
	}
	second, ok, err := store.ClaimNextGiftDelivery(ctx)
	if err != nil || !ok || second.Status != GiftDeliverySending || second.ID == first.ID {
		t.Fatalf("pending delivery after recovery = %#v, %v, %v", second, ok, err)
	}
	if _, err := store.CompleteGiftDelivery(ctx, first.ID, GiftDeliveryApplied, nil, nil, ""); !errors.Is(err, ErrGiftDeliveryFinalized) {
		t.Fatalf("uncertain delivery was allowed to complete: %v", err)
	}
	if _, err := store.CompleteGiftDelivery(ctx, second.ID, GiftDeliveryApplied, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	run, err = store.GetGiftRun(ctx, run.ID)
	if err != nil || run.Status != GiftRunUncertain {
		t.Fatalf("run after uncertain target = %#v, %v", run, err)
	}
}

func TestGiftRunPreflightSkipsAreTerminal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	packageValue, err := store.CreateGiftPackage(ctx, "Full", json.RawMessage(`{"items":[{"id":1,"quantity":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, "skip-target", []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateGiftRun(ctx, packageValue.ID, GiftTargetSingle, 0, []GiftTarget{{
		AccountID: account.ID, CharacterSlot: 0, SkipReason: "inventory capacity is insufficient",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != GiftRunCompleted {
		t.Fatalf("all-skipped run status = %q, want completed", run.Status)
	}
	details, err := store.GetGiftRunDetails(ctx, run.ID, "", 10)
	if err != nil || len(details.Deliveries) != 1 {
		t.Fatalf("skipped details = %#v, %v", details, err)
	}
	if details.Deliveries[0].Status != GiftDeliverySkipped || details.Deliveries[0].Error != "inventory capacity is insufficient" {
		t.Fatalf("skipped delivery = %#v", details.Deliveries[0])
	}
	if _, ok, err := store.ClaimNextGiftDelivery(ctx); err != nil || ok {
		t.Fatalf("skipped delivery entered pending queue: ok=%v err=%v", ok, err)
	}
}

func TestListAccountsPageIncludesDisabledWithoutFiveHundredLimit(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := store.timestamp(store.now())
	const count = 503
	for i := 1; i <= count; i++ {
		status := AccountActive
		if i%2 == 0 {
			status = AccountDisabled
		}
		_, err := store.db.ExecContext(ctx, `
INSERT INTO accounts(username,password_hash,status,must_change_password,created_at,updated_at)
VALUES(?,?,?,?,?,?)`, fmt.Sprintf("bulk-%04d", i), "test", status, 0, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	var (
		cursor   string
		seen     int
		active   int
		disabled int
	)
	for {
		page, err := store.ListAccountsPage(ctx, cursor, 37)
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Accounts)
		for _, account := range page.Accounts {
			switch account.Status {
			case AccountActive:
				active++
			case AccountDisabled:
				disabled++
			default:
				t.Fatalf("unexpected account status %q", account.Status)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != count || active != (count+1)/2 || disabled != count/2 {
		t.Fatalf("paged accounts seen=%d active=%d disabled=%d", seen, active, disabled)
	}
	if _, err := store.ListAccountsPage(ctx, "not-a-cursor", 37); err == nil {
		t.Fatal("invalid account cursor unexpectedly accepted")
	}

	// Assert the pagination method does not accidentally inherit ListAccounts's
	// legacy cap when a caller requests a large page.
	page, err := store.ListAccountsPage(ctx, "", 600)
	if err == nil || page.Accounts != nil {
		t.Fatalf("oversized page should be rejected: %#v, %v", page, err)
	}
}
