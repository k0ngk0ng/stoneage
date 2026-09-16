package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestAILifeStateIsAuthenticatedScopedBoundedAndReadOnly(t *testing.T) {
	_, store, server, client, _ := newAIProvisionHTTPFixture(t, nil)
	ctx := context.Background()
	for _, id := range []string{"life-one", "life-two"} {
		_, err := store.CreateProfile(ctx, airuntime.Profile{ID: id, Account: airuntime.AccountIdentity{ID: id}, Character: airuntime.CharacterIdentity{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 52; i++ {
		if _, err := store.UpsertAgentNote(ctx, "life-one", fmt.Sprint(i), "private plan"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertAgentNote(ctx, "life-two", "other", "other-profile-secret-plan"); err != nil {
		t.Fatal(err)
	}
	reminder, err := store.CreateSchedule(ctx, "life-one", airuntime.ScheduleInput{Kind: "reminder", Prompt: "meet later", RunAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := server.URL + "/api/ai/profiles/life-one/life-state"
	unauth, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(unauth.Body)
	unauth.Body.Close()
	if strings.Contains(string(body), "private plan") || strings.Contains(string(body), "meet later") {
		t.Fatal("unauthenticated life state exposure")
	}
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	var state struct {
		Available      bool                  `json:"available"`
		Notes          []airuntime.AgentNote `json:"notes"`
		Schedules      []airuntime.Schedule  `json:"schedules"`
		NotesTruncated bool                  `json:"notes_truncated"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &state) != nil || !state.Available || len(state.Notes) != 50 || !state.NotesTruncated || len(state.Schedules) != 1 {
		t.Fatalf("invalid bounded life state: status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "other-profile-secret-plan") || strings.Contains(string(body), "claim_token") || strings.Contains(string(body), "delivery_attempt_id") {
		t.Fatal("view leaked another profile or delivery internals")
	}
	for _, note := range state.Notes {
		if note.ProfileID != "life-one" {
			t.Fatal("wrong note scope")
		}
	}
	after, err := store.GetSchedule(ctx, "life-one", reminder.ID)
	if err != nil || after.Status != airuntime.SchedulePending || after.Occurrences != 0 {
		t.Fatal("reading state changed schedule")
	}
	missing, err := client.Get(server.URL + "/api/ai/profiles/missing/life-state")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing profile status = %d", missing.StatusCode)
	}
}
