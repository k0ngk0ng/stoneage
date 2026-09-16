// Package aiservice connects authenticated, character-bound game sessions to
// Codex's MCP sidecar. It is a private service, not a public login endpoint.
package aiservice

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type gameCapability struct {
	binding aimcp.Binding
	backend aimcp.Backend
	ctx     context.Context
	cancel  context.CancelFunc
}

type Gateway struct {
	mu         sync.RWMutex
	sessions   map[[32]byte]*gameCapability
	characters map[string][32]byte
}

func NewGateway() *Gateway {
	return &Gateway{sessions: map[[32]byte]*gameCapability{}, characters: map[string][32]byte{}}
}

// Register is called by the trusted session manager after game login. Neither
// a model nor an MCP client can choose or rebind the identity behind a token.
func (g *Gateway) Register(binding aimcp.Binding, backend aimcp.Backend) (string, func(), error) {
	if err := binding.Validate(); err != nil {
		return "", nil, err
	}
	if backend == nil {
		return "", nil, errors.New("game backend required")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	identity := binding.AccountID + "\x00" + binding.CharacterID
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.characters[identity]; exists {
		return "", nil, errors.New("character already has an agent session")
	}
	ctx, cancel := context.WithCancel(context.Background())
	capability := &gameCapability{binding: binding, backend: backend, ctx: ctx, cancel: cancel}
	g.sessions[hash] = capability
	g.characters[identity] = hash
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.sessions, hash)
			if current, ok := g.characters[identity]; ok && current == hash {
				delete(g.characters, identity)
			}
			cancel()
			g.mu.Unlock()
		})
	}
	return token, revoke, nil
}

func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.sessions {
		s.cancel()
	}
	g.sessions = map[[32]byte]*gameCapability{}
	g.characters = map[string][32]byte{}
}

func gatewayJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/game" {
		gatewayJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		gatewayJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	// This private capability API never accepts browser-originated requests.
	if r.Header.Get("Origin") != "" {
		gatewayJSON(w, 403, map[string]string{"error": "origin_not_allowed"})
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		gatewayJSON(w, 401, map[string]string{"error": "invalid_game_session"})
		return
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	if len(token) != 43 {
		gatewayJSON(w, 401, map[string]string{"error": "invalid_game_session"})
		return
	}
	hash := sha256.Sum256([]byte(token))
	g.mu.RLock()
	session := g.sessions[hash]
	g.mu.RUnlock()
	if session == nil {
		gatewayJSON(w, 401, map[string]string{"error": "invalid_game_session"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		gatewayJSON(w, 415, map[string]string{"error": "json_required"})
		return
	}
	var request struct {
		Operation string          `json:"operation"`
		Arguments json.RawMessage `json:"arguments"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		gatewayJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	defer cancel()
	if session.ctx.Err() != nil {
		gatewayJSON(w, 409, map[string]string{"error": "game_session_revoked"})
		return
	}
	result, err := dispatchGame(ctx, session, request.Operation, request.Arguments)
	if err != nil {
		status, code := http.StatusBadGateway, "game_operation_failed"
		if errors.Is(err, aimcp.ErrInvalidParams) || errors.Is(err, aimcp.ErrUnknownTool) {
			status, code = 400, "invalid_operation"
		}
		if errors.Is(err, aimcp.ErrInvalidBinding) || session.ctx.Err() != nil {
			status, code = 409, "game_session_revoked"
		}
		if errors.Is(err, aimcp.ErrBackend) {
			status, code = 503, "game_backend_unavailable"
		}
		gatewayJSON(w, status, map[string]string{"error": code})
		return
	}
	gatewayJSON(w, 200, map[string]any{"result": result})
}

func decodeArguments(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return aimcp.ErrInvalidParams
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return aimcp.ErrInvalidParams
	}
	if d.Decode(new(any)) != io.EOF {
		return aimcp.ErrInvalidParams
	}
	return nil
}

func dispatchGame(ctx context.Context, s *gameCapability, operation string, raw json.RawMessage) (any, error) {
	switch operation {
	case "observe":
		var input struct{}
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return s.backend.Observe(ctx, s.binding)
	case "knowledge":
		var input aimcp.KnowledgeQuery
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return s.backend.QueryKnowledge(ctx, s.binding, input)
	case "start_task":
		var input aimcp.TaskRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return s.backend.StartTask(ctx, s.binding, input)
	case "start_leveling":
		var input aimcp.LevelingRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return s.backend.StartLeveling(ctx, s.binding, input)
	case "task_status":
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := decodeArguments(raw, &input); err != nil || input.TaskID == "" {
			return nil, aimcp.ErrInvalidParams
		}
		return s.backend.TaskStatus(ctx, s.binding, input.TaskID)
	case "cancel":
		var input aimcp.CancelRequest
		if err := decodeArguments(raw, &input); err != nil || input.Handle == "" {
			return nil, aimcp.ErrInvalidParams
		}
		return s.backend.Cancel(ctx, s.binding, input)
	case "action":
		var input aimcp.TypedAction
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return s.backend.GameAction(ctx, s.binding, input)
	case "memory_write":
		backend, ok := s.backend.(aimcp.AgentMemoryBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.AgentNoteWriteRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		return backend.WriteAgentNote(ctx, s.binding, input)
	case "memory_list":
		backend, ok := s.backend.(aimcp.AgentMemoryBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.AgentNoteListRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		return backend.ListAgentNotes(ctx, s.binding, input)
	case "memory_delete":
		backend, ok := s.backend.(aimcp.AgentMemoryBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.AgentNoteDeleteRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		if err := backend.DeleteAgentNote(ctx, s.binding, input); err != nil {
			return nil, err
		}
		return aimcp.AgentNoteDeleteResult{Deleted: true, Key: strings.TrimSpace(input.Key)}, nil
	case "schedule_create":
		backend, ok := s.backend.(aimcp.ScheduleBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.ScheduleRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return backend.CreateSchedule(ctx, s.binding, input)
	case "schedule_list":
		backend, ok := s.backend.(aimcp.ScheduleBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.ScheduleListRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return backend.ListSchedules(ctx, s.binding, input)
	case "schedule_cancel":
		backend, ok := s.backend.(aimcp.ScheduleBackend)
		if !ok {
			return nil, aimcp.ErrUnknownTool
		}
		var input aimcp.ScheduleCancelRequest
		if err := decodeArguments(raw, &input); err != nil {
			return nil, err
		}
		return backend.CancelSchedule(ctx, s.binding, input)
	default:
		return nil, aimcp.ErrUnknownTool
	}
}
