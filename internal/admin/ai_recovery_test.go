package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

type fakeUnknownAdminRuntime struct {
	reviewed []aisupervisor.UnknownRecoveryRequest
}

func (*fakeUnknownAdminRuntime) ProfileStatus(context.Context, string) (AIExecutionStatus, error) {
	return AIExecutionStatus{State: "paused"}, nil
}
func (*fakeUnknownAdminRuntime) StartProfile(context.Context, string) error { return nil }
func (*fakeUnknownAdminRuntime) PauseProfile(context.Context, string) error { return nil }
func (*fakeUnknownAdminRuntime) StopProfile(context.Context, string) error  { return nil }
func (*fakeUnknownAdminRuntime) UnknownRecovery(_ context.Context, id string) (aisupervisor.UnknownRecoveryStatus, error) {
	return aisupervisor.UnknownRecoveryStatus{ProfileID: id, AttemptID: "attempt-1", ProfileVersion: 2, CheckpointVersion: 1, AttemptUpdatedAt: time.Unix(100, 0).UTC(), Ready: true, ReservedTokens: 17, Execution: aisupervisor.UnknownExecution{RequestID: "broker-1", State: "unknown", UpdatedAt: time.Unix(100, 0).UTC()}}, nil
}
func (r *fakeUnknownAdminRuntime) ReviewUnknown(_ context.Context, request aisupervisor.UnknownRecoveryRequest) error {
	r.reviewed = append(r.reviewed, request)
	return nil
}

func TestAIUnknownRecoveryAuthenticatedReview(t *testing.T) {
	runtime := &fakeUnknownAdminRuntime{}
	authStore, _, server, client, csrf := newAIProvisionHTTPFixture(t, nil, runtime)
	url := server.URL + "/api/ai/profiles/profile-1/recovery"
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Recovery aisupervisor.UnknownRecoveryStatus `json:"recovery"`
	}
	if err = json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || value.Recovery.AttemptID != "attempt-1" {
		t.Fatalf("recovery status=%d %+v", response.StatusCode, value)
	}
	body, _ := json.Marshal(aisupervisor.UnknownRecoveryRequest{UnknownRecoveryStatus: value.Recovery, Reason: "accept_uncertain_outcome"})
	for _, token := range []string{"", csrf} {
		request, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", token)
		response, err = client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(response.Body)
		response.Body.Close()
		expected := http.StatusOK
		if token == "" {
			expected = http.StatusForbidden
		}
		if response.StatusCode != expected {
			t.Fatalf("POST status=%d body=%s", response.StatusCode, content)
		}
	}
	if len(runtime.reviewed) != 1 || runtime.reviewed[0].Actor != "admin" {
		t.Fatalf("reviews=%+v", runtime.reviewed)
	}
	var spoof map[string]any
	_ = json.Unmarshal(body, &spoof)
	spoof["actor"] = "someone-else"
	body, _ = json.Marshal(spoof)
	request, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || len(runtime.reviewed) != 1 {
		t.Fatalf("actor spoof status=%d reviews=%d", response.StatusCode, len(runtime.reviewed))
	}
	if _, err = authStore.DB().ExecContext(context.Background(), "UPDATE admin_users SET role='operator' WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || len(runtime.reviewed) != 1 {
		t.Fatalf("non-admin review status=%d calls=%d", response.StatusCode, len(runtime.reviewed))
	}

}
