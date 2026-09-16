package aiprovision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

type fakeHeadlessSession struct {
	mu             sync.Mutex
	characters     []aigame.Character
	events         chan aigame.Event
	refreshErr     error
	createErr      error
	createdOptions []aigame.CharacterCreate
	enterErr       error
	entered        []string
	closed         atomic.Int32
	closeOnce      sync.Once
}

func (session *fakeHeadlessSession) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld}, nil
}

func (*fakeHeadlessSession) ExecuteExpected(context.Context, uint64, aigame.Action) error { return nil }

func (session *fakeHeadlessSession) Characters() []aigame.Character {
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]aigame.Character(nil), session.characters...)
}

func (session *fakeHeadlessSession) RefreshCharacters(context.Context) ([]aigame.Character, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.refreshErr != nil {
		return nil, session.refreshErr
	}
	return append([]aigame.Character(nil), session.characters...), nil
}

func (session *fakeHeadlessSession) CreateCharacter(_ context.Context, create aigame.CharacterCreate) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.createErr != nil {
		return session.createErr
	}
	session.createdOptions = append(session.createdOptions, create)
	session.characters = append(session.characters, aigame.Character{Slot: int(create.DataPlace), Name: create.Name})
	return nil
}

func (session *fakeHeadlessSession) EnterCharacter(_ context.Context, name string) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.enterErr != nil {
		return session.enterErr
	}
	session.entered = append(session.entered, name)
	return nil
}

func (session *fakeHeadlessSession) Events() <-chan aigame.Event { return session.events }

func (session *fakeHeadlessSession) Close() error {
	session.closeOnce.Do(func() {
		session.closed.Add(1)
		if session.events != nil {
			close(session.events)
		}
	})
	return nil
}

type fakeConnector struct {
	mu        sync.Mutex
	sessions  []*fakeHeadlessSession
	accounts  []string
	passwords []string
	errors    []error
}

func (connector *fakeConnector) Login(_ context.Context, _ aigame.Config, credentials aigame.Credentials) (HeadlessSession, error) {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	connector.accounts = append(connector.accounts, credentials.Account)
	connector.passwords = append(connector.passwords, string(credentials.PasswordBytes))
	index := len(connector.sessions)
	if index >= len(connector.errors) {
		index = len(connector.errors) - 1
	}
	if index >= 0 && connector.errors[index] != nil {
		return nil, connector.errors[index]
	}
	if len(connector.sessions) == 0 {
		return nil, ErrGameSession
	}
	session := connector.sessions[0]
	connector.sessions = connector.sessions[1:]
	return session, nil
}

type providerFixture struct {
	auth     *auth.Store
	profiles *airuntime.Store
	secrets  *SecretStore
	provider *ProfileSessionProvider
	profile  airuntime.Profile
	account  auth.Account
}

func newProviderFixture(t *testing.T, connector SessionConnector) providerFixture {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("build", "ai"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "aiprovision-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	authStore, err := auth.Open(filepath.Join(directory, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := authStore.Migrate(context.Background()); err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	if err := ensureBindingSchema(context.Background(), authStore.DB()); err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	profiles, err := airuntime.OpenStore(filepath.Join(directory, "ai.db"))
	if err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	secrets, err := NewSecretStore(filepath.Join(directory, "game-secrets"))
	if err != nil {
		_ = authStore.Close()
		_ = profiles.Close()
		t.Fatal(err)
	}
	account, err := authStore.CreateAccount(context.Background(), "ai-test", []byte("Test@123"))
	if err != nil {
		_ = authStore.Close()
		_ = profiles.Close()
		t.Fatal(err)
	}
	binding := Binding{ProfileID: "profile-1", AccountID: account.ID, AccountUsername: account.Username,
		CharacterSlot: 1, CharacterID: account.Username + ":1", CharacterName: "Scout", CreatedAt: time.Now().UTC()}
	if err := insertBinding(context.Background(), authStore.DB(), binding); err != nil {
		_ = authStore.Close()
		_ = profiles.Close()
		t.Fatal(err)
	}
	profile, err := profiles.CreateProfile(context.Background(), airuntime.Profile{
		ID: "profile-1", Account: airuntime.AccountIdentity{ID: account.Username, Username: account.Username},
		Character: airuntime.CharacterIdentity{ID: binding.CharacterID, Name: binding.CharacterName},
		Status:    airuntime.ProfileStatusStopped,
	})
	if err != nil {
		_ = authStore.Close()
		_ = profiles.Close()
		t.Fatal(err)
	}
	provider, err := NewProfileSessionProvider(ProviderConfig{Auth: authStore, Secrets: secrets, Game: connector, WakeBuffer: 2})
	if err != nil {
		_ = authStore.Close()
		_ = profiles.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
		_ = authStore.Close()
		_ = profiles.Close()
	})
	if err := secrets.Put(account.ID, []byte("Test@123")); err != nil {
		t.Fatal(err)
	}
	return providerFixture{auth: authStore, profiles: profiles, secrets: secrets, provider: provider, profile: profile, account: account}
}

func TestProfileSessionProviderBindsCharacterAndForwardsEvents(t *testing.T) {
	session := &fakeHeadlessSession{
		characters: []aigame.Character{{Slot: 1, Name: "Scout"}}, events: make(chan aigame.Event, 2),
	}
	connector := &fakeConnector{sessions: []*fakeHeadlessSession{session}}
	fixture := newProviderFixture(t, connector)

	lease, err := fixture.provider.Open(context.Background(), fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Session != session || lease.Binding.CharacterID != fixture.profile.Character.ID || lease.Wake == nil || lease.Close == nil {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	connector.mu.Lock()
	if len(connector.accounts) != 1 || connector.accounts[0] != "ai-test" || len(connector.passwords) != 1 || connector.passwords[0] != "Test@123" {
		t.Fatalf("login credentials = %#v / %#v", connector.accounts, connector.passwords)
	}
	connector.mu.Unlock()

	session.events <- aigame.Event{Function: "P1"}
	select {
	case <-lease.Wake:
	case <-time.After(time.Second):
		t.Fatal("game event did not wake the lease")
	}
	if _, err := fixture.provider.Open(context.Background(), fixture.profile); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("duplicate open error = %v", err)
	}

	lease.Close()
	lease.Close()
	if got := session.closed.Load(); got != 1 {
		t.Fatalf("session close count = %d, want 1", got)
	}
	reopenedSession := &fakeHeadlessSession{
		characters: []aigame.Character{{Slot: 1, Name: "Scout"}}, events: make(chan aigame.Event, 1),
	}
	connector.mu.Lock()
	connector.sessions = append(connector.sessions, reopenedSession)
	connector.mu.Unlock()
	if _, err := fixture.provider.Open(context.Background(), fixture.profile); err != nil {
		t.Fatalf("profile could not reopen after lease close: %v", err)
	}
	if err := fixture.provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.provider.Open(context.Background(), fixture.profile); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after provider close = %v", err)
	}
}

func TestProfileSessionProviderRejectsBindingMismatchAndDisabledAccount(t *testing.T) {
	session := &fakeHeadlessSession{characters: []aigame.Character{{Slot: 1, Name: "Scout"}}}
	connector := &fakeConnector{sessions: []*fakeHeadlessSession{session}}
	fixture := newProviderFixture(t, connector)

	tampered := fixture.profile
	tampered.Character.Name = "Other"
	if _, err := fixture.provider.Open(context.Background(), tampered); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("tampered profile error = %v", err)
	}
	if err := fixture.auth.SetAccountStatus(context.Background(), fixture.account.ID, auth.AccountDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.provider.Open(context.Background(), fixture.profile); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatalf("disabled account error = %v", err)
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if len(connector.accounts) != 0 {
		t.Fatalf("disabled account reached connector: %#v", connector.accounts)
	}
}

func TestCreateAIDefaultsToUnlimitedFundsAndCleansFailedSecret(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("build", "ai"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "aiprovision-create-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	authStore, err := auth.Open(filepath.Join(directory, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authStore.Close() })
	if err := authStore.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	profiles, err := airuntime.OpenStore(filepath.Join(directory, "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profiles.Close() })
	secrets, err := NewSecretStore(filepath.Join(directory, "game-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	successSession := &fakeHeadlessSession{}
	connector := &fakeConnector{sessions: []*fakeHeadlessSession{successSession}}
	provisioner, err := New(Config{ProviderConfig: ProviderConfig{Auth: authStore, Secrets: secrets, Game: connector}, Profiles: profiles, AccountPrefix: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateRequest{ProfileID: "created-1", CharacterSlot: 0, CharacterName: "Scout"}
	created, err := provisioner.CreateAI(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Profile.UnlimitedFunds {
		t.Fatalf("default unlimited funds = false: %#v", created.Profile)
	}
	if _, err := os.Stat(filepath.Join(directory, "game-secrets", "account-"+itoa(created.Account.ID)+".key")); err != nil {
		t.Fatalf("successful credential was not retained: %v", err)
	}

	falseValue := false
	failedSession := &fakeHeadlessSession{createErr: errors.New("no character")}
	connector.mu.Lock()
	connector.sessions = append(connector.sessions, failedSession)
	connector.mu.Unlock()
	failed, err := provisioner.CreateAI(context.Background(), CreateRequest{ProfileID: "created-2", CharacterSlot: 0, CharacterName: "Scout", UnlimitedFunds: &falseValue})
	if err == nil || !errors.Is(err, ErrCharacterUnavailable) {
		t.Fatalf("failed provisioning error = %v", err)
	}
	if failed.Account.ID != 0 {
		t.Fatalf("failed provisioning leaked result: %#v", failed)
	}
	accounts, err := authStore.ListAccounts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("account count = %d, want 2", len(accounts))
	}
	var failedAccount auth.Account
	for _, account := range accounts {
		if account.Status == auth.AccountDisabled {
			failedAccount = account
			break
		}
	}
	if failedAccount.ID == 0 {
		t.Fatalf("failed account was not disabled: %#v", accounts)
	}
	if _, err := os.Stat(filepath.Join(directory, "game-secrets", "account-"+itoa(failedAccount.ID)+".key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed credential still exists: %v", err)
	}
}

func itoa(value int64) string {
	if value < 0 {
		return "-" + itoa(-value)
	}
	if value == 0 {
		return "0"
	}
	var result [32]byte
	index := len(result)
	for value > 0 {
		index--
		result[index] = byte('0' + value%10)
		value /= 10
	}
	return string(result[index:])
}
