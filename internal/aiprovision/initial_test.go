package aiprovision

import (
	"context"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"testing"
)

const initialTestPersonID = "pc1_11111111111111111111111111111111"

type fakeInitializer struct {
	calls     int
	limit     int
	validated []aiinitial.Resolved
	fail      bool
	identity  *string
	before    func(aiinitial.Resolved)
}

func (f *fakeInitializer) Validate(_ context.Context, r aiinitial.Resolved) error {
	f.validated = append(f.validated, r)
	for _, pet := range r.Pets {
		if f.limit > 0 && pet.Level > f.limit {
			return errors.New("template limit exceeded")
		}
	}
	return nil
}
func (f *fakeInitializer) Apply(_ context.Context, _ string, _ int, r aiinitial.Resolved) (playerdata.Snapshot, error) {
	f.calls++
	if f.before != nil {
		f.before(r)
	}
	if f.fail {
		return playerdata.Snapshot{}, errors.New("uncertain result")
	}
	id := initialTestPersonID
	if f.identity != nil {
		id = *f.identity
	}
	return playerdata.Snapshot{Name: "Scout", PersistentCharacterID: id, Attributes: []playerdata.Attribute{{Key: "lv", Value: int64(r.CharacterLevel)}}}, nil
}
func TestInitialStatePublishedOnlyAfterApplyAndNeverRerolled(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "unconfirmed"}[fail], func(t *testing.T) {
			ctx := context.Background()
			session := &fakeHeadlessSession{}
			connector := &fakeConnector{sessions: []*fakeHeadlessSession{session}}
			fixture := newProviderFixture(t, connector)
			p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Provider().Close()
			initializer := &fakeInitializer{fail: fail}
			initializer.before = func(r aiinitial.Resolved) {
				record, err := p.InitialState(ctx, "initial-1")
				if err != nil || record == nil || record.Status != "applying" || record.Resolved.CharacterLevel != r.CharacterLevel {
					t.Fatalf("plan not durable before apply: %+v %v", record, err)
				}
				if _, err := fixture.profiles.GetProfile(ctx, "initial-1"); !errors.Is(err, airuntime.ErrNotFound) {
					t.Fatalf("profile published early: %v", err)
				}
			}
			request := CreateRequest{ProfileID: "initial-1", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "custom", CharacterLevel: 60, Hometown: 3, Weights: characterbuild.Weights{Vital: 1, Strength: 2}}}
			_, err = p.CreateAI(ctx, request)
			if (err != nil) != fail {
				t.Fatalf("creation: %v", err)
			}
			if len(session.createdOptions) != 1 || session.createdOptions[0].Hometown != 3 {
				t.Fatal("initial hometown was not used for native creation")
			}
			record, err := p.InitialState(ctx, "initial-1")
			if err != nil || record == nil {
				t.Fatalf("missing journal: %v", err)
			}
			if fail {
				if record.Status != "failed_or_unconfirmed" || record.Actual != nil {
					t.Fatalf("journal: %+v", record)
				}
				if _, err := fixture.profiles.GetProfile(ctx, "initial-1"); !errors.Is(err, airuntime.ErrNotFound) {
					t.Fatalf("failed profile published: %v", err)
				}
				accounts, err := fixture.auth.ListAccounts(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range accounts {
					if a.ID != fixture.account.ID && a.Status != auth.AccountDisabled {
						t.Fatal("uncertain account still active")
					}
				}
			} else if record.Status != "published" || record.Actual == nil {
				t.Fatalf("journal: %+v", record)
			}
			request.InitialState = &aiinitial.Request{Mode: "birth"}
			if _, err := p.CreateAI(ctx, request); !errors.Is(err, ErrBindingConflict) {
				t.Fatalf("retry accepted: %v", err)
			}
			if initializer.calls != 1 || len(connector.accounts) != 1 {
				t.Fatal("retry performed side effects")
			}
		})
	}
}
func TestInitialReservationPersistsAcrossProvisionerRestart(t *testing.T) {
	fixture := newProviderFixture(t, &fakeConnector{})
	cfg := Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: &fakeConnector{}}, Profiles: fixture.profiles}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	request := &aiinitial.Request{Mode: "random", Random: &aiinitial.RandomRules{CharacterLevel: aiinitial.Range{Min: 10, Max: 60}, PetLevel: aiinitial.Range{Min: 1, Max: 30}, PetCount: aiinitial.Range{Min: 0, Max: 0}, Hometowns: []int{0, 3}}}
	first, err := p.reserveInitial(context.Background(), "reserved", request, &fakeInitializer{})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Provider().Close()
	if _, err := restarted.reserveInitial(context.Background(), "reserved", request, &fakeInitializer{}); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("reservation rerolled: %v", err)
	}
	saved, err := restarted.InitialState(context.Background(), "reserved")
	if err != nil || saved.Resolved.CharacterLevel != first.Resolved.CharacterLevel || saved.Resolved.Weights != first.Resolved.Weights {
		t.Fatalf("reservation changed: %+v %v", saved, err)
	}
}

func TestRandomPetRangeValidatedBeforeAccountCreation(t *testing.T) {
	ctx := context.Background()
	connector := &fakeConnector{}
	fixture := newProviderFixture(t, connector)
	p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	initializer := &fakeInitializer{limit: 80}
	request := CreateRequest{ProfileID: "over-limit", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "random", Random: &aiinitial.RandomRules{CharacterLevel: aiinitial.Range{Min: 1, Max: 60}, PetLevel: aiinitial.Range{Min: 1, Max: 100}, PetCount: aiinitial.Range{Min: 1, Max: 2}, PetTemplates: []int{42}, Hometowns: []int{0}}}}
	if _, err := p.CreateAI(ctx, request); err == nil {
		t.Fatal("accepted random range above template limit")
	}
	if len(initializer.validated) != 1 || initializer.validated[0].Pets[0].Level != 100 {
		t.Fatal("random candidates not checked at upper bound")
	}
	accounts, err := fixture.auth.ListAccounts(ctx, "")
	if err != nil || len(accounts) != 1 {
		t.Fatalf("validation created account: %d %v", len(accounts), err)
	}
	if len(connector.accounts) != 0 || initializer.calls != 0 {
		t.Fatal("validation performed game side effects")
	}
	if state, err := p.InitialState(ctx, "over-limit"); err != nil || state != nil {
		t.Fatalf("invalid configuration reserved a random result: %+v %v", state, err)
	}
}

func TestInitializationWithoutPersistedIdentityCannotPublish(t *testing.T) {
	for _, id := range []string{"", "malformed"} {
		t.Run(id, func(t *testing.T) {
			ctx := context.Background()
			connector := &fakeConnector{sessions: []*fakeHeadlessSession{{}}}
			fixture := newProviderFixture(t, connector)
			p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Provider().Close()
			initializer := &fakeInitializer{identity: &id}
			req := CreateRequest{ProfileID: "no-native-identity", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "custom", CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}}}
			if _, err := p.CreateAI(ctx, req); !errors.Is(err, ErrInitialUnconfirmed) {
				t.Fatalf("published unconfirmed identity: %v", err)
			}
			record, err := p.InitialState(ctx, req.ProfileID)
			if err != nil || record == nil || record.Actual != nil || initialPublicationReady(record) {
				t.Fatalf("identity was journaled as confirmed: %+v %v", record, err)
			}
			if _, err := fixture.profiles.GetProfile(ctx, req.ProfileID); !errors.Is(err, airuntime.ErrNotFound) {
				t.Fatal("profile published")
			}
			if _, err := p.Binding(ctx, req.ProfileID); !errors.Is(err, ErrBindingNotFound) {
				t.Fatal("binding published")
			}
			account, err := fixture.auth.GetAccount(ctx, record.Binding.AccountID)
			if err != nil || account.Status != auth.AccountDisabled || initializer.calls != 1 {
				t.Fatal("failed initialization not quarantined or repeated")
			}
		})
	}
}
