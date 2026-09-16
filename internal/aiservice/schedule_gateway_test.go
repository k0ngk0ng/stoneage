package aiservice

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestScheduleRemoteGatewayKeepsProfileScopeAndRevocation(t *testing.T) {
	ctx := context.Background()
	store := memoryTestStore(t)
	profile, err := store.CreateProfile(ctx, memoryTestProfile("schedule-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProfile(ctx, memoryTestProfile("other-schedule-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	backend, game := gameFixture(t)
	backend.Binding.ProfileID = profile.ID
	backend.Schedules = store
	withMemory, err := NewBoundAgentMemoryBackend(backend, store, backend.Binding, backend.Gate, aicontrol.Agent)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway()
	defer gateway.Close()
	token, revoke, err := gateway.Register(backend.Binding, withMemory)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()
	remote, err := aimcp.NewRemoteBackend(aimcp.RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	spoof := backend.Binding
	spoof.ProfileID = other.ID
	created, err := remote.CreateSchedule(ctx, spoof, aimcp.ScheduleRequest{Kind: "reminder", Prompt: "check plans", DelaySeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := store.GetSchedule(ctx, profile.ID, created.ID)
	if err != nil || owned.ProfileID != profile.ID {
		t.Fatalf("schedule not bound to capability owner: %+v %v", owned, err)
	}
	if _, err := store.GetSchedule(ctx, other.ID, created.ID); err == nil {
		t.Fatal("remote caller selected another profile")
	}
	listed, err := remote.ListSchedules(ctx, spoof, aimcp.ScheduleListRequest{})
	if err != nil || len(listed.Schedules) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	if _, err := remote.CancelSchedule(ctx, spoof, aimcp.ScheduleCancelRequest{ScheduleID: created.ID}); err != nil {
		t.Fatal(err)
	}
	revoke()
	if _, err := remote.CreateSchedule(ctx, spoof, aimcp.ScheduleRequest{Kind: "reminder", Prompt: "late", DelaySeconds: 60}); err == nil {
		t.Fatal("revoked gateway created a reminder")
	}
	if game.writes != 0 {
		t.Fatal("scheduling wrote gameplay packets")
	}
}
