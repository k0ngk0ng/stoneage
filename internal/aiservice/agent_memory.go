package aiservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// BoundAgentMemoryBackend adds the profile-scoped self-note capability to an
// already composed game backend. It embeds the game backend so all ordinary
// game operations retain their existing implementation, while note writes
// are fenced by the same character binding and ownership generation.
//
// The wrapper is constructed only by trusted server code. MCP requests carry
// note key/text but never a profile ID; the profile comes from Binding.
type BoundAgentMemoryBackend struct {
	aimcp.Backend
	Store   *airuntime.Store
	Binding aimcp.Binding
	Gate    *aicontrol.Gate
	Owner   aicontrol.Mode
}

func (backend *BoundAgentMemoryBackend) check(binding aimcp.Binding) error {
	if backend == nil || backend.Backend == nil || backend.Store == nil || backend.Gate == nil {
		return aimcp.ErrBackend
	}
	if binding != backend.Binding {
		return aimcp.ErrInvalidBinding
	}
	state := backend.Gate.State()
	if state.Generation != binding.Generation {
		return aicontrol.ErrStale
	}
	if state.Mode != backend.Owner {
		return aicontrol.ErrOwner
	}
	return nil
}

func (backend *GameBackend) agentNotesStore(binding aimcp.Binding) (*airuntime.Store, error) {
	if backend == nil {
		return nil, aimcp.ErrBackend
	}
	if err := backend.check(binding); err != nil {
		return nil, err
	}
	if backend.AgentNotes == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return nil, aimcp.ErrBackend
	}
	return backend.AgentNotes, nil
}

// WriteAgentNote implements the bound self-note capability for the native
// gameplay backend. The profile comes from the immutable binding.
func (backend *GameBackend) WriteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteWriteRequest) (aimcp.AgentNote, error) {
	store, err := backend.agentNotesStore(binding)
	if err != nil {
		return aimcp.AgentNote{}, err
	}
	if err := request.Validate(); err != nil {
		return aimcp.AgentNote{}, err
	}
	note, err := store.UpsertAgentNote(ctx, binding.ProfileID, request.Key, request.Text)
	if err != nil {
		return aimcp.AgentNote{}, err
	}
	return projectAgentNote(note), nil
}

// ListAgentNotes implements the bound self-note capability for the native
// gameplay backend and returns only notes owned by the bound profile.
func (backend *GameBackend) ListAgentNotes(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteListRequest) (aimcp.AgentNoteList, error) {
	store, err := backend.agentNotesStore(binding)
	if err != nil {
		return aimcp.AgentNoteList{}, err
	}
	if err := request.Validate(); err != nil {
		return aimcp.AgentNoteList{}, err
	}
	notes, err := store.ListAgentNotes(ctx, binding.ProfileID, request.Limit)
	if err != nil {
		return aimcp.AgentNoteList{}, err
	}
	result := aimcp.AgentNoteList{Notes: make([]aimcp.AgentNote, 0, len(notes))}
	for _, note := range notes {
		if note.ProfileID != binding.ProfileID {
			return aimcp.AgentNoteList{}, errors.New("aiservice: agent note profile mismatch")
		}
		result.Notes = append(result.Notes, projectAgentNote(note))
	}
	return result, nil
}

// DeleteAgentNote implements deletion of one bound profile's self-note.
func (backend *GameBackend) DeleteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteDeleteRequest) error {
	store, err := backend.agentNotesStore(binding)
	if err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	return store.DeleteAgentNote(ctx, binding.ProfileID, request.Key)
}

func (backend *BoundAgentMemoryBackend) WriteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteWriteRequest) (aimcp.AgentNote, error) {
	if err := backend.check(binding); err != nil {
		return aimcp.AgentNote{}, err
	}
	if err := request.Validate(); err != nil {
		return aimcp.AgentNote{}, err
	}
	note, err := backend.Store.UpsertAgentNote(ctx, binding.ProfileID, request.Key, request.Text)
	if err != nil {
		return aimcp.AgentNote{}, err
	}
	return projectAgentNote(note), nil
}

func (backend *BoundAgentMemoryBackend) ListAgentNotes(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteListRequest) (aimcp.AgentNoteList, error) {
	if err := backend.check(binding); err != nil {
		return aimcp.AgentNoteList{}, err
	}
	if err := request.Validate(); err != nil {
		return aimcp.AgentNoteList{}, err
	}
	notes, err := backend.Store.ListAgentNotes(ctx, binding.ProfileID, request.Limit)
	if err != nil {
		return aimcp.AgentNoteList{}, err
	}
	result := aimcp.AgentNoteList{Notes: make([]aimcp.AgentNote, 0, len(notes))}
	for _, note := range notes {
		if note.ProfileID != binding.ProfileID {
			return aimcp.AgentNoteList{}, errors.New("aiservice: agent note profile mismatch")
		}
		result.Notes = append(result.Notes, projectAgentNote(note))
	}
	return result, nil
}

func (backend *BoundAgentMemoryBackend) DeleteAgentNote(ctx context.Context, binding aimcp.Binding, request aimcp.AgentNoteDeleteRequest) error {
	if err := backend.check(binding); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	return backend.Store.DeleteAgentNote(ctx, binding.ProfileID, request.Key)
}

func projectAgentNote(note airuntime.AgentNote) aimcp.AgentNote {
	return aimcp.AgentNote{
		ID:        note.ID,
		Key:       note.Key,
		Text:      note.Text,
		CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: note.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// NewBoundAgentMemoryBackend is the explicit composition helper used by
// server code that already owns an authenticated game backend. The gate and
// binding are required so memory operations cannot outlive that lease.
func NewBoundAgentMemoryBackend(backend aimcp.Backend, store *airuntime.Store, binding aimcp.Binding, gate *aicontrol.Gate, owner aicontrol.Mode) (*BoundAgentMemoryBackend, error) {
	if backend == nil || store == nil {
		return nil, fmt.Errorf("%w: game backend and memory store are required", aimcp.ErrBackend)
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if gate == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return nil, fmt.Errorf("%w: memory binding is incomplete", aimcp.ErrInvalidBinding)
	}
	if owner == "" {
		return nil, fmt.Errorf("%w: memory owner is required", aimcp.ErrInvalidBinding)
	}
	return &BoundAgentMemoryBackend{Backend: backend, Store: store, Binding: binding, Gate: gate, Owner: owner}, nil
}

func (backend *BoundAgentMemoryBackend) Close() {
	if backend == nil || backend.Backend == nil {
		return
	}
	if closer, ok := backend.Backend.(interface{ Close() }); ok {
		closer.Close()
	}
}

// ScheduleBackend and the supervisor's optional task/receipt views are
// forwarded when a custom backend is wrapped. This keeps adding notes from
// removing capabilities that were already present on the game backend.
func (backend *BoundAgentMemoryBackend) CreateSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleRequest) (aimcp.Schedule, error) {
	schedules, ok := backend.Backend.(aimcp.ScheduleBackend)
	if !ok {
		return aimcp.Schedule{}, aimcp.ErrUnknownTool
	}
	return schedules.CreateSchedule(ctx, binding, request)
}

func (backend *BoundAgentMemoryBackend) ListSchedules(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleListRequest) (aimcp.ScheduleList, error) {
	schedules, ok := backend.Backend.(aimcp.ScheduleBackend)
	if !ok {
		return aimcp.ScheduleList{}, aimcp.ErrUnknownTool
	}
	return schedules.ListSchedules(ctx, binding, request)
}

func (backend *BoundAgentMemoryBackend) CancelSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleCancelRequest) (aimcp.Schedule, error) {
	schedules, ok := backend.Backend.(aimcp.ScheduleBackend)
	if !ok {
		return aimcp.Schedule{}, aimcp.ErrUnknownTool
	}
	return schedules.CancelSchedule(ctx, binding, request)
}

func (backend *BoundAgentMemoryBackend) SupportsSchedules() bool {
	if backend == nil || backend.Backend == nil {
		return false
	}
	_, ok := backend.Backend.(aimcp.ScheduleBackend)
	return ok
}

func (backend *BoundAgentMemoryBackend) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if source, ok := backend.Backend.(interface {
		Active(context.Context) ([]aimcp.TaskReceipt, error)
	}); ok {
		return source.Active(ctx)
	}
	return nil, nil
}

func (backend *BoundAgentMemoryBackend) PendingActions(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if source, ok := backend.Backend.(interface {
		PendingActions(context.Context) ([]aimcp.TaskReceipt, error)
	}); ok {
		return source.PendingActions(ctx)
	}
	return nil, nil
}

var _ aimcp.AgentMemoryBackend = (*BoundAgentMemoryBackend)(nil)
var _ aimcp.AgentMemoryBackend = (*GameBackend)(nil)
