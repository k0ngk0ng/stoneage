package aiservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// This creates a fresh identity on the local QA server and reads its address
// book twice. It sends no chat or mail and never opens an operator account.
func TestLiveMailListFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_MAIL_LIST_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_MAIL_LIST_LIVE_TEST=1 for local QA address-book receipts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f := movementCrossMapLiveProvisionFreshAI(t, ctx, "mail-list", "mail-list-live-test")
	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "isolated mail list QA")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{ProfileID: f.Profile.ID, AccountID: f.Created.Account.Username,
		CharacterID: f.Created.Binding.CharacterID, CharacterName: f.Created.Binding.CharacterName, Generation: state.Generation}
	receipts, err := OpenReceiptStore(filepath.Join(f.Root, "mail-receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	backend := &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: f.Lease.Session, Receipts: receipts}
	var previousHandle string
	for round := 0; round < 2; round++ {
		before, err := f.Lease.Session.Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := backend.GameAction(ctx, binding, aimcp.TypedAction{Kind: "mail", Command: "list", ExpectedRevision: before.Revision})
		if err != nil || receipt.Status != aimcp.ReceiptUnknown || receipt.Handle == "" || receipt.Handle == previousHandle {
			t.Fatalf("mail list submission: status=%s err=%v", receipt.Status, err)
		}
		previousHandle = receipt.Handle
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		confirmed := false
		for !confirmed {
			select {
			case <-ctx.Done():
				t.Fatal("QA address-book context expired")
			case <-deadline.C:
				t.Fatal("server did not confirm a new complete address-book table")
			case <-ticker.C:
				status, err := backend.TaskStatus(ctx, binding, receipt.Handle)
				if err != nil {
					t.Fatal(err)
				}
				confirmed = status.Status == aimcp.ReceiptConfirmed
			}
		}
		ticker.Stop()
		deadline.Stop()
		after, err := f.Lease.Session.Observe(ctx)
		if err != nil || !after.AddressBookKnown || after.AddressBookRevision <= before.AddressBookRevision {
			t.Fatalf("mail list lacks fresh table: err=%v", err)
		}
		t.Logf("query %d confirmed with a new complete AB table", round+1)
	}
}
