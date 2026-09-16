package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type fakeAIInitialRecoveryProvider struct {
	initializations []aiprovision.InitialRecoveryView
	profile         airuntime.Profile
	listErr         error
	recoverErr      error
	listCalls       int
	recoverCalls    int
	actor           string
	actorID         *int64
	profileID       string
}

func (provider *fakeAIInitialRecoveryProvider) Provision(context.Context, AIPlayerProvisionRequest) (airuntime.Profile, error) {
	return provider.profile, nil
}

func (provider *fakeAIInitialRecoveryProvider) ListInitialRecoveries(context.Context) ([]aiprovision.InitialRecoveryView, error) {
	provider.listCalls++
	if provider.listErr != nil {
		return nil, provider.listErr
	}
	return provider.initializations, nil
}

func (provider *fakeAIInitialRecoveryProvider) RecoverInitial(_ context.Context, id, actor string, actorID *int64) (airuntime.Profile, error) {
	provider.recoverCalls++
	provider.profileID = id
	provider.actor = actor
	provider.actorID = actorID
	if provider.recoverErr != nil {
		return airuntime.Profile{}, provider.recoverErr
	}
	return provider.profile, nil
}

func recoveryTestProfile(id string) airuntime.Profile {
	return airuntime.Profile{
		ID:        id,
		Account:   airuntime.AccountIdentity{ID: "account-1", Username: "ai_000000001"},
		Character: airuntime.CharacterIdentity{ID: "ai_000000001:0", Name: "恢复角色"},
		Status:    airuntime.ProfileStatusStopped,
	}
}

func TestAIInitializationsListUsesSafeShapeAndEmptyArray(t *testing.T) {
	provider := &fakeAIInitialRecoveryProvider{}
	_, _, server, client, _ := newAIProvisionHTTPFixture(t, provider)
	response, err := client.Get(server.URL + "/api/ai/initializations")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value struct {
		Initializations []aiprovision.InitialRecoveryView `json:"initializations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || value.Initializations == nil || len(value.Initializations) != 0 {
		t.Fatalf("list status=%d value=%#v", response.StatusCode, value.Initializations)
	}
	if provider.listCalls != 1 {
		t.Fatalf("list calls=%d", provider.listCalls)
	}
}

func TestAIInitializationsListReturnsViewFields(t *testing.T) {
	updated := time.Date(2026, 9, 16, 8, 9, 10, 0, time.UTC)
	provider := &fakeAIInitialRecoveryProvider{initializations: []aiprovision.InitialRecoveryView{{
		ProfileID: "pending-1", CharacterName: "待发布", Status: "publication_pending", Recoverable: true, UpdatedAt: updated,
	}}}
	_, _, server, client, _ := newAIProvisionHTTPFixture(t, provider)
	response, err := client.Get(server.URL + "/api/ai/initializations")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value struct {
		Initializations []map[string]any `json:"initializations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(value.Initializations) != 1 {
		t.Fatalf("list status=%d value=%#v", response.StatusCode, value.Initializations)
	}
	entry := value.Initializations[0]
	for key, want := range map[string]any{"profile_id": "pending-1", "character_name": "待发布", "status": "publication_pending", "recoverable": true, "updated_at": updated.Format(time.RFC3339Nano)} {
		if entry[key] != want {
			t.Fatalf("field %s=%#v, want %#v; entry=%#v", key, entry[key], want, entry)
		}
	}
}

func TestAIInitialRecoveryRequiresAdminAndCSRF(t *testing.T) {
	provider := &fakeAIInitialRecoveryProvider{profile: recoveryTestProfile("pending-1")}
	authStore, _, server, client, csrf := newAIProvisionHTTPFixture(t, provider)
	body := bytes.NewBufferString(`{}`)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/ai/initializations/pending-1/recover", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || provider.recoverCalls != 0 {
		t.Fatalf("missing CSRF status=%d calls=%d", response.StatusCode, provider.recoverCalls)
	}

	if _, err := authStore.DB().ExecContext(context.Background(), "UPDATE admin_users SET role='operator' WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/api/ai/initializations/pending-1/recover", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || provider.recoverCalls != 0 {
		t.Fatalf("operator status=%d calls=%d", response.StatusCode, provider.recoverCalls)
	}
}

func TestAIInitialRecoveryAuditsActorAndReturnsProfileView(t *testing.T) {
	provider := &fakeAIInitialRecoveryProvider{profile: recoveryTestProfile("pending-1")}
	authStore, _, server, client, csrf := newAIProvisionHTTPFixture(t, provider)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/initializations/pending-1/recover", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody)
	}
	var value struct {
		Profile aiProfileView `json:"profile"`
	}
	if err := json.Unmarshal(responseBody, &value); err != nil {
		t.Fatal(err)
	}
	if value.Profile.ID != "pending-1" || value.Profile.Character.Name != "恢复角色" || value.Profile.ProfileStatus != airuntime.ProfileStatusStopped {
		t.Fatalf("profile=%#v", value.Profile)
	}
	if provider.recoverCalls != 1 || provider.profileID != "pending-1" || provider.actor != "admin" || provider.actorID == nil || *provider.actorID <= 0 {
		t.Fatalf("provider call id=%q actor=%q actorID=%v calls=%d", provider.profileID, provider.actor, provider.actorID, provider.recoverCalls)
	}
	events, err := authStore.RecentAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event == "ai_initialization_recovered" && event.Username == "pending-1" && event.Detail == `{"result":"published","version":0}` {
			found = true
		}
	}
	if !found {
		t.Fatalf("recovery audit missing: %#v", events)
	}
}

func TestAIInitialRecoveryErrorDoesNotExposeProviderDetails(t *testing.T) {
	provider := &fakeAIInitialRecoveryProvider{recoverErr: errors.New("password=do-not-leak account_secret=hidden")}
	_, _, server, client, csrf := newAIProvisionHTTPFixture(t, provider)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/initializations/pending-1/recover", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || bytes.Contains(body, []byte("password")) || bytes.Contains(body, []byte("account_secret")) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}
