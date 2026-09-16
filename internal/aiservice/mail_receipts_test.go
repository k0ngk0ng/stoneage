package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type mailReceiptSession struct {
	mu           sync.Mutex
	snapshot     aigame.Snapshot
	observeCalls int
	writes       int
	actions      []aigame.Action
}

func (s *mailReceiptSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observeCalls++
	return s.snapshot, nil
}

func (s *mailReceiptSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Revision != revision {
		return aigame.ErrStaleRevision
	}
	s.writes++
	s.actions = append(s.actions, action)
	return nil
}

func mailReceiptFixture(t *testing.T, snapshot aigame.Snapshot, profile string) (*GameBackend, *mailReceiptSession) {
	t.Helper()
	gate := aicontrol.New()
	state, _, err := gate.Switch(1, aicontrol.Agent, "mail receipt test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(); gate.Close() })
	session := &mailReceiptSession{snapshot: snapshot}
	binding := aimcp.Binding{ProfileID: profile, AccountID: "account", CharacterID: "character-id", CharacterName: "character", Generation: state.Generation}
	return &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: session, Receipts: store}, session
}

func TestMailListReceiptRequiresFreshCompleteAB(t *testing.T) {
	b, session := mailReceiptFixture(t, aigame.Snapshot{
		Account: "account", Character: "character", Revision: 10, Connected: true, Phase: aigame.PhaseWorld,
		SessionToken: "session-one", AddressBookRevision: 3, AddressBookKnown: true,
		AddressBook: []aigame.AddressBookEntry{{Index: 0, Use: true, Name: "before"}},
	}, "profile-one")
	action := aimcp.TypedAction{Kind: "mail", Command: "list", ExpectedRevision: 10}
	receipt, err := b.GameAction(context.Background(), b.Binding, action)
	if err != nil || receipt.Status != aimcp.ReceiptUnknown || session.writes != 1 {
		t.Fatalf("mail/list submission = %+v err=%v writes=%d", receipt, err, session.writes)
	}
	storedAction, storedReceipt, err := b.Receipts.LoadAction(context.Background(), b.Binding, receipt.Handle)
	if err != nil || storedAction != action || storedReceipt.Status != aimcp.ReceiptUnknown {
		t.Fatalf("stored mail/list action = %+v receipt=%+v err=%v", storedAction, storedReceipt, err)
	}
	var evidence mailListEvidence
	if err := json.Unmarshal(storedReceipt.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.BeforeRevision != 10 || evidence.BeforeAddressBookRevision != 3 || evidence.SessionToken != "session-one" || evidence.GameOutcomeConfirmed {
		t.Fatalf("mail/list baseline evidence = %+v", evidence)
	}

	// Polling without a new complete AB table remains unknown.
	got, err := b.TaskStatus(context.Background(), b.Binding, receipt.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown || session.writes != 1 {
		t.Fatalf("cached table confirmed mail/list: %+v err=%v writes=%d", got, err, session.writes)
	}
	session.mu.Lock()
	session.snapshot.Revision = 11
	session.snapshot.AddressBook[0].Name = "abi-update"
	session.mu.Unlock()
	got, err = b.TaskStatus(context.Background(), b.Binding, receipt.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown || session.writes != 1 {
		t.Fatalf("ABI update confirmed mail/list: %+v err=%v writes=%d", got, err, session.writes)
	}

	// Long-running inhabitants accumulate sender-unacknowledged messages.
	// The explicit handle must remain pollable beyond the recent display cap.
	for i := uint64(0); i < 130; i++ {
		if _, err := b.Receipts.Prepare(context.Background(), b.Binding, aimcp.TypedAction{Kind: "chat", Text: "earlier message", ExpectedRevision: 100 + i}); err != nil {
			t.Fatal(err)
		}
	}
	// A later full table is the only confirmation signal. It is observed by
	// polling and must not cause a second AB write.
	session.mu.Lock()
	session.snapshot.Revision = 12
	session.snapshot.AddressBookRevision = 4
	session.snapshot.AddressBook = []aigame.AddressBookEntry{{Index: 0, Use: true, Name: "after"}}
	session.mu.Unlock()
	got, err = b.TaskStatus(context.Background(), b.Binding, receipt.Handle)
	if err != nil || got.Status != aimcp.ReceiptConfirmed || session.writes != 1 {
		t.Fatalf("fresh AB did not confirm mail/list: %+v err=%v writes=%d", got, err, session.writes)
	}
	if got.Evidence == nil {
		t.Fatal("confirmed mail/list has no evidence")
	}
	if err := json.Unmarshal(got.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.GameOutcomeConfirmed || evidence.After == nil || evidence.After.AddressBookRevision != 4 || evidence.After.AddressBook[0].Name != "after" {
		t.Fatalf("mail/list confirmation evidence = %+v", evidence)
	}
	observes := session.observeCalls
	got, err = b.TaskStatus(context.Background(), b.Binding, receipt.Handle)
	if err != nil || got.Status != aimcp.ReceiptConfirmed || session.writes != 1 || session.observeCalls != observes {
		t.Fatalf("confirmed mail/list was repolled or rewritten: %+v err=%v writes=%d observes=%d", got, err, session.writes, session.observeCalls)
	}
}

func TestMailListReconciliationRejectsDifferentSessionAndGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.db")
	store, err := OpenReceiptStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gate := aicontrol.New()
	state, _, err := gate.Switch(1, aicontrol.Agent, "mail receipt test")
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	binding := aimcp.Binding{ProfileID: "profile-one", AccountID: "account", CharacterID: "character-id", CharacterName: "character", Generation: state.Generation}
	receipt, _, err := store.PrepareOnce(context.Background(), binding, aimcp.TypedAction{Kind: "mail", Command: "list", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence := mailListEvidence{ProfileID: binding.ProfileID, AccountID: binding.AccountID, CharacterID: binding.CharacterID, Generation: binding.Generation, SessionToken: "old-session", BeforeRevision: 1, BeforeAddressBookRevision: 0}
	receipt.Evidence, err = json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), binding, receipt); err != nil {
		t.Fatal(err)
	}

	newSession := &mailReceiptSession{snapshot: aigame.Snapshot{Account: "account", Character: "character", Revision: 2, Connected: true, Phase: aigame.PhaseWorld, SessionToken: "new-session", AddressBookRevision: 1, AddressBookKnown: true}}
	b := &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: newSession, Receipts: store}
	if got, err := b.TaskStatus(context.Background(), binding, receipt.Handle); err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("different session falsely confirmed old AB: %+v err=%v", got, err)
	}

	// A different generation is also ineligible even if the session token and
	// complete-table sequence happen to match.
	if _, _, err := gate.Switch(state.Generation, aicontrol.Agent, "new generation"); err != nil {
		t.Fatal(err)
	}
	newGeneration := gate.State().Generation
	b.Binding.Generation = newGeneration
	newSession.snapshot.SessionToken = "old-session"
	if got, err := b.TaskStatus(context.Background(), b.Binding, receipt.Handle); err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("different generation falsely confirmed old AB: %+v err=%v", got, err)
	}
}
