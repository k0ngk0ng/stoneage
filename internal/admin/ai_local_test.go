package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type localExecutorFixture struct{ calls int }

func (f *localExecutorFixture) CreateCommand(context.Context, string, string) (AILocalCommand, error) {
	f.calls++
	return AILocalCommand{Command: "fixture local command", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (*localExecutorFixture) ExecutorStatus(context.Context, string) (AIExecutorStatus, error) {
	return AIExecutorStatus{Location: "local", Connected: false}, nil
}

func TestLocalInvitationRequiresAdminCSRFAndIdlePlayer(t *testing.T) {
	authStore, store, _, client, csrf := newAIProvisionHTTPFixture(t, nil)
	ctx := context.Background()
	for _, status := range []string{airuntime.ProfileStatusStopped, airuntime.ProfileStatusActive} {
		_, err := store.CreateProfile(ctx, airuntime.Profile{ID: status, Account: airuntime.AccountIdentity{ID: status}, Character: airuntime.CharacterIdentity{ID: status}, Status: status})
		if err != nil {
			t.Fatal(err)
		}
	}
	f := &localExecutorFixture{}
	control, err := NewServer(authStore, Options{AIStore: store, AILocalExecutor: f, CSRFSecret: []byte("ai-provision-secret")})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(control.Handler())
	defer server.Close()
	for _, test := range []struct {
		profile, csrf, origin string
		want                  int
	}{
		{"stopped", "", "", http.StatusForbidden},
		{"active", csrf, "", http.StatusConflict},
		{"stopped", csrf, "https://unrelated.invalid", http.StatusBadRequest},
		{"stopped", csrf, "", http.StatusOK},
	} {
		r, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/"+test.profile+"/local-command", nil)
		r.Header.Set("X-CSRF-Token", test.csrf)
		r.Header.Set("Origin", test.origin)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("%+v: status %d", test, response.StatusCode)
		}
	}
	if f.calls != 1 {
		t.Fatalf("issued %d invitations", f.calls)
	}
	if _, err := authStore.DB().ExecContext(ctx, "UPDATE admin_users SET role='operator' WHERE username='admin'"); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/stopped/local-command", nil)
	r.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || f.calls != 1 {
		t.Fatal("operator could issue invitation")
	}
}
