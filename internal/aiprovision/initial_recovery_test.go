package aiprovision

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type fakeInitialVerifier struct {
	calls    int
	fail     bool
	online   bool
	identity *string
}

func (v *fakeInitialVerifier) Verify(_ context.Context, _ string, _ int, _ aiinitial.Resolved) (playerdata.Snapshot, error) {
	v.calls++
	if v.fail {
		return playerdata.Snapshot{}, errors.New("archive not confirmed")
	}
	id := initialTestPersonID
	if v.identity != nil {
		id = *v.identity
	}
	return playerdata.Snapshot{Name: "Scout", Online: v.online, PersistentCharacterID: id}, nil
}

func TestRecoverConfirmedInitializationWithoutReapplying(t *testing.T) {
	for _, halfPublished := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-profile", true: "half-published-profile"}[halfPublished], func(t *testing.T) {
			ctx := context.Background()
			connector := &fakeConnector{sessions: []*fakeHeadlessSession{{}}}
			fixture := newProviderFixture(t, connector)
			cfg := Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles}
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Provider().Close()
			if _, err := fixture.profiles.DB().Exec(`CREATE TRIGGER reject_initial_profile BEFORE INSERT ON ai_profiles WHEN NEW.id='recover-initial' BEGIN SELECT RAISE(ABORT,'publication unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			initializer := &fakeInitializer{}
			req := CreateRequest{ProfileID: "recover-initial", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "custom", CharacterLevel: 60, Hometown: 3, Weights: characterbuild.Weights{Vital: 1}}, Profile: airuntime.Profile{Status: airuntime.ProfileStatusActive}}
			if _, err := p.CreateAI(ctx, req); err == nil {
				t.Fatal("publication failure not surfaced")
			}
			record, err := p.InitialState(ctx, req.ProfileID)
			if err != nil || !initialPublicationReady(record) || record.Status != "publication_pending" {
				t.Fatalf("confirmed publication lost: %+v %v", record, err)
			}
			if record.Resolved.CharacterLevel != 60 || record.Binding == nil || record.PendingProfile == nil {
				t.Fatal("recovery draft missing")
			}
			account, err := fixture.auth.GetAccount(ctx, record.Binding.AccountID)
			if err != nil || account.Status != auth.AccountDisabled {
				t.Fatal("failed creation account not quarantined")
			}
			if _, err := fixture.profiles.DB().Exec(`DROP TRIGGER reject_initial_profile`); err != nil {
				t.Fatal(err)
			}
			if halfPublished {
				profile, err := fixture.profiles.CreateProfileAs(ctx, *record.PendingProfile, "test")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.Provider().Open(ctx, profile); !errors.Is(err, ErrInitialUnconfirmed) {
					t.Fatalf("half-published profile opened: %v", err)
				}
			}
			// A fresh provisioner reads only the persisted journal, never in-memory choices.
			restarted, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Provider().Close()
			views, err := restarted.ListInitialRecoveries(ctx)
			if err != nil || len(views) != 1 || !views[0].Recoverable {
				t.Fatalf("recovery list: %+v %v", views, err)
			}
			verifier := &fakeInitialVerifier{fail: true}
			if _, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrInitialUnconfirmed) {
				t.Fatalf("unverified archive accepted: %v", err)
			}
			verifier.fail = false
			verifier.online = true
			if _, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrInitialUnconfirmed) {
				t.Fatalf("live memory accepted as archive: %v", err)
			}
			verifier.online = false
			for _, wrong := range []string{"", "malformed", "pc1_22222222222222222222222222222222"} {
				verifier.identity = &wrong
				if _, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrInitialUnconfirmed) {
					t.Fatalf("replacement character accepted: %v", err)
				}
				account, _ := fixture.auth.GetAccount(ctx, record.Binding.AccountID)
				if account.Status != auth.AccountDisabled {
					t.Fatal("identity mismatch enabled account")
				}
				saved, err := restarted.InitialState(ctx, req.ProfileID)
				if err != nil || saved.Actual.PersistentCharacterID != initialTestPersonID || saved.Status != "publication_pending" {
					t.Fatal("identity mismatch changed original journal")
				}
			}
			verifier.identity = nil
			originalID := record.Actual.PersistentCharacterID
			record.Actual.PersistentCharacterID = ""
			if err := restarted.saveInitial(ctx, req.ProfileID, record.Binding.AccountID, record); err != nil {
				t.Fatal(err)
			}
			views, err = restarted.ListInitialRecoveries(ctx)
			if err != nil || len(views) != 1 || views[0].Recoverable {
				t.Fatal("legacy journal advertised recoverable")
			}
			calls := verifier.calls
			if _, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrInitialUnconfirmed) || verifier.calls != calls {
				t.Fatal("legacy journal adopted current identity")
			}
			record.Actual.PersistentCharacterID = originalID
			if err := restarted.saveInitial(ctx, req.ProfileID, record.Binding.AccountID, record); err != nil {
				t.Fatal(err)
			}
			profile, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Status != airuntime.ProfileStatusStopped || !profile.UnlimitedFunds || profile.Character.Name != "Scout" {
				t.Fatalf("recovery published wrong profile: %+v", profile)
			}
			if initializer.calls != 1 || len(connector.accounts) != 1 {
				t.Fatal("recovery reran native initialization or logged in")
			}
			if err := checkInitialPublication(ctx, fixture.auth.DB(), req.ProfileID); err != nil {
				t.Fatal(err)
			}
			if views, err := restarted.ListInitialRecoveries(ctx); err != nil || len(views) != 0 {
				t.Fatalf("published record still pending: %+v %v", views, err)
			}
			// A retry is read-only even if the administrator subsequently disables it.
			if err := fixture.auth.SetAccountStatus(ctx, record.Binding.AccountID, auth.AccountDisabled); err != nil {
				t.Fatal(err)
			}
			calls = verifier.calls
			if _, err := restarted.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); err != nil {
				t.Fatal(err)
			}
			account, _ = fixture.auth.GetAccount(ctx, record.Binding.AccountID)
			if account.Status != auth.AccountDisabled || verifier.calls != calls {
				t.Fatal("published retry changed account or reran verification")
			}
		})
	}
}

func TestUnconfirmedInitialCannotRecoverOrReapply(t *testing.T) {
	ctx := context.Background()
	connector := &fakeConnector{sessions: []*fakeHeadlessSession{{}}}
	fixture := newProviderFixture(t, connector)
	p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	initializer := &fakeInitializer{fail: true}
	req := CreateRequest{ProfileID: "unknown-initial", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "custom", CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}}}
	if _, err := p.CreateAI(ctx, req); err == nil {
		t.Fatal("unknown apply accepted")
	}
	verifier := &fakeInitialVerifier{}
	if _, err := p.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrInitialUnconfirmed) {
		t.Fatalf("unknown initialization recovered: %v", err)
	}
	views, err := p.ListInitialRecoveries(ctx)
	if err != nil || len(views) != 1 || views[0].Recoverable || verifier.calls != 0 || initializer.calls != 1 {
		t.Fatal("unconfirmed initialization retried or hidden")
	}
	release, err := p.lockInitialPublication()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := p.RecoverInitial(ctx, req.ProfileID, "admin", nil, verifier); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("concurrent publication was not excluded: %v", err)
	}
}

func TestStartupQuarantinesInterruptedInitialAccountWithoutApply(t *testing.T) {
	ctx := context.Background()
	fixture := newProviderFixture(t, &fakeConnector{})
	p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: &fakeConnector{}}, Profiles: fixture.profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	initial, err := p.reserveInitial(ctx, "interrupted", &aiinitial.Request{Mode: "custom", CharacterLevel: 20, Weights: characterbuild.Weights{Vital: 1}}, &fakeInitializer{})
	if err != nil {
		t.Fatal(err)
	}
	account, err := fixture.auth.CreateAccountAs(ctx, nil, "pending_ai", []byte("test-pass"))
	if err != nil {
		t.Fatal(err)
	}
	initial.Status = "applying"
	if err := p.saveInitial(ctx, "interrupted", account.ID, initial); err != nil {
		t.Fatal(err)
	}
	if err := p.ReconcileInitialAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	account, err = fixture.auth.GetAccount(ctx, account.ID)
	if err != nil || account.Status != auth.AccountDisabled {
		t.Fatal("interrupted account not quarantined")
	}
	record, err := p.InitialState(ctx, "interrupted")
	if err != nil || record.Status != "failed_or_unconfirmed" || record.Resolved.CharacterLevel != 20 {
		t.Fatalf("startup changed original choices: %+v %v", record, err)
	}
	if err := p.ReconcileInitialAccounts(ctx); err != nil {
		t.Fatal(err)
	}
}
