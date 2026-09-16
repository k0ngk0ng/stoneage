package aiservice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func memoryProfile(id string) airuntime.Profile {
	return airuntime.Profile{ID: id, Account: airuntime.AccountIdentity{ID: id + "-account"}, Character: airuntime.CharacterIdentity{ID: id + "-character", Name: id}}
}

type optionalMemoryGameBackend struct {
	aimcp.UnavailableBackend
	active   []aimcp.TaskReceipt
	pending  []aimcp.TaskReceipt
	schedule aimcp.Schedule
}

func (backend *optionalMemoryGameBackend) CreateSchedule(_ context.Context, _ aimcp.Binding, request aimcp.ScheduleRequest) (aimcp.Schedule, error) {
	backend.schedule = aimcp.Schedule{ID: "schedule-forwarded", Kind: request.Kind, Prompt: request.Prompt, RunAt: "2026-09-16T12:00:00Z", Status: "pending"}
	return backend.schedule, nil
}

func (backend *optionalMemoryGameBackend) ListSchedules(context.Context, aimcp.Binding, aimcp.ScheduleListRequest) (aimcp.ScheduleList, error) {
	return aimcp.ScheduleList{Schedules: []aimcp.Schedule{backend.schedule}}, nil
}

func (backend *optionalMemoryGameBackend) CancelSchedule(_ context.Context, _ aimcp.Binding, request aimcp.ScheduleCancelRequest) (aimcp.Schedule, error) {
	backend.schedule.ID = request.ScheduleID
	backend.schedule.Status = "cancelled"
	return backend.schedule, nil
}

func (backend *optionalMemoryGameBackend) Active(context.Context) ([]aimcp.TaskReceipt, error) {
	return backend.active, nil
}

func (backend *optionalMemoryGameBackend) PendingActions(context.Context) ([]aimcp.TaskReceipt, error) {
	return backend.pending, nil
}

func TestBoundAgentMemoryBackendScopesNotesAndFencesTakeover(t *testing.T) {
	store, err := airuntime.OpenStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	profile, err := store.CreateProfile(ctx, memoryProfile("memory-bound-one"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProfile(ctx, memoryProfile("memory-bound-two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAgentNote(ctx, other.ID, "private", "other profile"); err != nil {
		t.Fatal(err)
	}

	gate := aicontrol.New()
	state, _, err := gate.Switch(1, aicontrol.Agent, "test")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{ProfileID: profile.ID, CharacterID: profile.Character.ID, CharacterName: profile.Character.Name, Generation: state.Generation}
	backend, err := NewBoundAgentMemoryBackend(aimcp.UnavailableBackend{}, store, binding, gate, aicontrol.Agent)
	if err != nil {
		t.Fatal(err)
	}

	note, err := backend.WriteAgentNote(ctx, binding, aimcp.AgentNoteWriteRequest{Key: "plan", Text: "return to town"})
	if err != nil || note.Key != "plan" || note.Text != "return to town" {
		t.Fatalf("write note = %#v, %v", note, err)
	}
	notes, err := backend.ListAgentNotes(ctx, binding, aimcp.AgentNoteListRequest{})
	if err != nil || len(notes.Notes) != 1 || notes.Notes[0].Key != "plan" {
		t.Fatalf("bound list = %#v, %v", notes, err)
	}
	if err := backend.DeleteAgentNote(ctx, binding, aimcp.AgentNoteDeleteRequest{Key: "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := backend.DeleteAgentNote(ctx, binding, aimcp.AgentNoteDeleteRequest{Key: "private"}); !errors.Is(err, airuntime.ErrNotFound) {
		t.Fatalf("cross-profile note delete = %v", err)
	}
	if _, err := backend.WriteAgentNote(ctx, binding, aimcp.AgentNoteWriteRequest{Key: "invalid", Text: ""}); !errors.Is(err, aimcp.ErrInvalidParams) {
		t.Fatalf("invalid note write = %v", err)
	}

	if _, err := gate.Takeover("human takeover"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.WriteAgentNote(ctx, binding, aimcp.AgentNoteWriteRequest{Key: "late", Text: "must be fenced"}); !errors.Is(err, aicontrol.ErrStale) {
		t.Fatalf("write after takeover = %v", err)
	}
}

func TestAgentMemoryRemoteGatewayRoundTripUsesBoundProfile(t *testing.T) {
	store, err := airuntime.OpenStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profile, err := store.CreateProfile(context.Background(), memoryProfile("memory-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	gate := aicontrol.New()
	state, _, err := gate.Switch(1, aicontrol.Agent, "test")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{ProfileID: profile.ID, CharacterID: profile.Character.ID, Generation: state.Generation}
	backend, err := NewBoundAgentMemoryBackend(aimcp.UnavailableBackend{}, store, binding, gate, aicontrol.Agent)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway()
	defer gateway.Close()
	token, revoke, err := gateway.Register(binding, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	server := httptest.NewServer(gateway)
	defer server.Close()
	remote, err := aimcp.NewRemoteBackend(aimcp.RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token})
	if err != nil {
		t.Fatal(err)
	}

	note, err := remote.WriteAgentNote(context.Background(), binding, aimcp.AgentNoteWriteRequest{Key: "journey", Text: "remember this path"})
	if err != nil || note.Key != "journey" {
		t.Fatalf("remote write = %#v, %v", note, err)
	}
	list, err := remote.ListAgentNotes(context.Background(), binding, aimcp.AgentNoteListRequest{Limit: 10})
	if err != nil || len(list.Notes) != 1 || list.Notes[0].Text != "remember this path" {
		t.Fatalf("remote list = %#v, %v", list, err)
	}
	if err := remote.DeleteAgentNote(context.Background(), binding, aimcp.AgentNoteDeleteRequest{Key: "journey"}); err != nil {
		t.Fatal(err)
	}
	if notes, err := store.ListAgentNotes(context.Background(), profile.ID, 10); err != nil || len(notes) != 0 {
		t.Fatalf("durable notes after remote delete = %#v, %v", notes, err)
	}

	if response := call(gateway, token, `{"operation":"memory_list","arguments":{"profile_id":"other"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("gateway accepted profile override: %d", response.Code)
	}
}

func TestBoundAgentMemoryBackendForwardsExistingOptionalCapabilities(t *testing.T) {
	store, err := airuntime.OpenStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	profile, err := store.CreateProfile(context.Background(), memoryProfile("memory-forwarding"))
	if err != nil {
		t.Fatal(err)
	}
	gate := aicontrol.New()
	state, _, err := gate.Switch(1, aicontrol.Agent, "test")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{ProfileID: profile.ID, CharacterID: profile.Character.ID, Generation: state.Generation}
	underlying := &optionalMemoryGameBackend{active: []aimcp.TaskReceipt{{Handle: "active", Status: aimcp.ReceiptRunning}}, pending: []aimcp.TaskReceipt{{Handle: "pending", Status: aimcp.ReceiptUnknown}}}
	backend, err := NewBoundAgentMemoryBackend(underlying, store, binding, gate, aicontrol.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if !backend.SupportsSchedules() {
		t.Fatal("schedule capability was lost while adding notes")
	}
	if _, err := backend.CreateSchedule(context.Background(), binding, aimcp.ScheduleRequest{Kind: "reminder", Prompt: "check"}); err != nil {
		t.Fatal(err)
	}
	if schedules, err := backend.ListSchedules(context.Background(), binding, aimcp.ScheduleListRequest{Limit: 1}); err != nil || len(schedules.Schedules) != 1 {
		t.Fatalf("forwarded schedules = %#v, %v", schedules, err)
	}
	if active, err := backend.Active(context.Background()); err != nil || len(active) != 1 || active[0].Handle != "active" {
		t.Fatalf("forwarded active tasks = %#v, %v", active, err)
	}
	if pending, err := backend.PendingActions(context.Background()); err != nil || len(pending) != 1 || pending[0].Handle != "pending" {
		t.Fatalf("forwarded pending actions = %#v, %v", pending, err)
	}

	plain, err := NewBoundAgentMemoryBackend(aimcp.UnavailableBackend{}, store, binding, gate, aicontrol.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if plain.SupportsSchedules() {
		t.Fatal("wrapper advertised an unavailable schedule capability")
	}
}
