package aimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxEndpointBytes     = 2048
	maxTokenBytes        = 4096
	maxRemoteBody        = 1 * 1024 * 1024
	defaultRemoteTimeout = 30 * time.Second
	maxRemoteTimeout     = 5 * time.Minute
)

// RemoteBackend connects the MCP sidecar to the server-owned game session.
// Endpoint and Token are process configuration; neither can be supplied by
// an MCP tool call.  The gateway associates the bearer token with one role and
// control generation, so the JSON body intentionally contains no binding.
type RemoteBackend struct {
	endpoint string
	token    string
	client   *http.Client
}

type RemoteBackendConfig struct {
	Endpoint       string
	Token          string
	Client         *http.Client
	RequestTimeout time.Duration
}

func NewRemoteBackend(config RemoteBackendConfig) (*RemoteBackend, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	if len([]byte(endpoint)) == 0 || len([]byte(endpoint)) > maxEndpointBytes {
		return nil, fmt.Errorf("%w: invalid game endpoint", ErrBackend)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || (parsed.Path != "/v1/game" && parsed.Path != "/api/ai/worker/v1/game") {
		return nil, fmt.Errorf("%w: invalid game endpoint", ErrBackend)
	}
	if !validCapabilityToken(config.Token) {
		return nil, fmt.Errorf("%w: game capability is required", ErrBackend)
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRemoteTimeout
	}
	if config.RequestTimeout > maxRemoteTimeout {
		return nil, fmt.Errorf("%w: request timeout exceeds maximum", ErrBackend)
	}
	client := config.Client
	if client == nil {
		transport := http.DefaultTransport
		client = &http.Client{Transport: transport, CheckRedirect: noRedirect}
	}
	// A caller-supplied client may have a redirect policy that leaks the
	// capability to another host.  Replace it with a shallow copy and enforce
	// the policy at this boundary.
	copyClient := *client
	copyClient.CheckRedirect = noRedirect
	// Preserve a shorter caller-supplied client deadline while ensuring a
	// zero-timeout client cannot wait forever on a stalled game gateway.
	if copyClient.Timeout <= 0 || config.RequestTimeout < copyClient.Timeout {
		copyClient.Timeout = config.RequestTimeout
	}
	return &RemoteBackend{endpoint: endpoint, token: config.Token, client: &copyClient}, nil
}

func validCapabilityToken(token string) bool {
	if len(token) != 43 || !validText(token, maxTokenBytes) {
		return false
	}
	for _, character := range token {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func noRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

type remoteRequest struct {
	Operation string `json:"operation"`
	Arguments any    `json:"arguments"`
}

type remoteEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (backend *RemoteBackend) call(ctx context.Context, operation string, arguments any, result any) error {
	if backend == nil || backend.client == nil || backend.endpoint == "" || backend.token == "" {
		return ErrBackend
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(remoteRequest{Operation: operation, Arguments: arguments})
	if err != nil {
		return ErrBackend
	}
	if len(payload) > maxJSONValueBytes {
		return ErrBackend
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.endpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrBackend
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+backend.token)
	response, err := backend.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBackend
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteBody+1))
	if err != nil || len(body) > maxRemoteBody {
		return ErrBackend
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Do not include status text or response body: either can contain a
		// gateway diagnostic, credential, or upstream request material.
		return ErrBackend
	}
	var envelope remoteEnvelope
	if err := decodeStrictRemote(body, &envelope); err != nil {
		return ErrBackend
	}
	if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return ErrBackend
	}
	if len(envelope.Result) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
		return ErrBackend
	}
	if result == nil {
		return nil
	}
	if err := decodeStrictRemote(envelope.Result, result); err != nil {
		return ErrBackend
	}
	return nil
}

func decodeStrictRemote(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxRemoteBody {
		return errors.New("remote JSON is too large")
	}
	if err := rejectDuplicateKeys(raw, 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing remote JSON")
	}
	return nil
}

func (backend *RemoteBackend) Observe(ctx context.Context, binding Binding) (Observation, error) {
	var result Observation
	if err := backend.call(ctx, "observe", struct{}{}, &result); err != nil {
		return Observation{}, err
	}
	if err := validateObservation(result, binding); err != nil {
		return Observation{}, ErrBackend
	}
	if result.CharacterID == "" {
		result.CharacterID = binding.CharacterID
	}
	return result, nil
}

func (backend *RemoteBackend) QueryKnowledge(ctx context.Context, binding Binding, query KnowledgeQuery) (KnowledgeResult, error) {
	if err := validateKnowledgeQuery(query); err != nil {
		return KnowledgeResult{}, err
	}
	var result KnowledgeResult
	if err := backend.call(ctx, "knowledge", query, &result); err != nil {
		return KnowledgeResult{}, err
	}
	if err := validateKnowledgeResult(result); err != nil {
		return KnowledgeResult{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) StartTask(ctx context.Context, binding Binding, request TaskRequest) (TaskReceipt, error) {
	if err := validateTaskRequest(request); err != nil {
		return TaskReceipt{}, err
	}
	var result TaskReceipt
	if err := backend.call(ctx, "start_task", request, &result); err != nil {
		return TaskReceipt{}, err
	}
	if err := validReceipt(result); err != nil {
		return TaskReceipt{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) StartLeveling(ctx context.Context, binding Binding, request LevelingRequest) (TaskReceipt, error) {
	if err := validateLevelingRequest(request); err != nil {
		return TaskReceipt{}, err
	}
	var result TaskReceipt
	if err := backend.call(ctx, "start_leveling", request, &result); err != nil {
		return TaskReceipt{}, err
	}
	if err := validReceipt(result); err != nil {
		return TaskReceipt{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) TaskStatus(ctx context.Context, binding Binding, handle string) (TaskReceipt, error) {
	if err := validateHandle(handle); err != nil {
		return TaskReceipt{}, err
	}
	var result TaskReceipt
	if err := backend.call(ctx, "task_status", struct {
		TaskID string `json:"task_id"`
	}{TaskID: handle}, &result); err != nil {
		return TaskReceipt{}, err
	}
	if err := validReceipt(result); err != nil {
		return TaskReceipt{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) Cancel(ctx context.Context, binding Binding, request CancelRequest) (TaskReceipt, error) {
	if err := validateHandle(request.Handle); err != nil || !validText(request.Reason, maxTextBytes) {
		return TaskReceipt{}, ErrInvalidParams
	}
	var result TaskReceipt
	if err := backend.call(ctx, "cancel", request, &result); err != nil {
		return TaskReceipt{}, err
	}
	if err := validReceipt(result); err != nil {
		return TaskReceipt{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) CreateSchedule(ctx context.Context, binding Binding, request ScheduleRequest) (Schedule, error) {
	if err := validateScheduleRequest(request); err != nil {
		return Schedule{}, err
	}
	var result Schedule
	if err := backend.call(ctx, "schedule_create", request, &result); err != nil {
		return Schedule{}, err
	}
	if err := validateSchedule(result); err != nil {
		return Schedule{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) ListSchedules(ctx context.Context, binding Binding, request ScheduleListRequest) (ScheduleList, error) {
	if err := validateScheduleListRequest(request); err != nil {
		return ScheduleList{}, err
	}
	var result ScheduleList
	if err := backend.call(ctx, "schedule_list", request, &result); err != nil {
		return ScheduleList{}, err
	}
	if len(result.Schedules) > 100 {
		return ScheduleList{}, ErrBackend
	}
	for _, schedule := range result.Schedules {
		if err := validateSchedule(schedule); err != nil {
			return ScheduleList{}, ErrBackend
		}
	}
	return result, nil
}

func (backend *RemoteBackend) CancelSchedule(ctx context.Context, binding Binding, request ScheduleCancelRequest) (Schedule, error) {
	if err := validateScheduleCancelRequest(request); err != nil {
		return Schedule{}, err
	}
	var result Schedule
	if err := backend.call(ctx, "schedule_cancel", request, &result); err != nil {
		return Schedule{}, err
	}
	if err := validateSchedule(result); err != nil {
		return Schedule{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) WriteAgentNote(ctx context.Context, binding Binding, request AgentNoteWriteRequest) (AgentNote, error) {
	if err := request.Validate(); err != nil {
		return AgentNote{}, err
	}
	var result AgentNote
	if err := backend.call(ctx, "memory_write", request, &result); err != nil {
		return AgentNote{}, err
	}
	if err := result.Validate(); err != nil {
		return AgentNote{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) ListAgentNotes(ctx context.Context, binding Binding, request AgentNoteListRequest) (AgentNoteList, error) {
	if err := request.Validate(); err != nil {
		return AgentNoteList{}, err
	}
	var result AgentNoteList
	if err := backend.call(ctx, "memory_list", request, &result); err != nil {
		return AgentNoteList{}, err
	}
	if err := result.Validate(); err != nil {
		return AgentNoteList{}, ErrBackend
	}
	return result, nil
}

func (backend *RemoteBackend) DeleteAgentNote(ctx context.Context, binding Binding, request AgentNoteDeleteRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	var result AgentNoteDeleteResult
	if err := backend.call(ctx, "memory_delete", request, &result); err != nil {
		return err
	}
	if !result.Deleted || (result.Key != "" && result.Key != strings.TrimSpace(request.Key)) {
		return ErrBackend
	}
	return nil
}

func (backend *RemoteBackend) GameAction(ctx context.Context, binding Binding, action TypedAction) (ActionReceipt, error) {
	// Reuse the same closed vocabulary checks as the MCP decoder.  The remote
	// backend is also a public adapter and must not become a raw-action escape
	// hatch for callers other than MCP.
	validated, err := validateTypedAction(action)
	if err != nil {
		return ActionReceipt{}, err
	}
	var result ActionReceipt
	if err := backend.call(ctx, "action", validated, &result); err != nil {
		return ActionReceipt{}, err
	}
	if err := validReceipt(result); err != nil {
		return ActionReceipt{}, ErrBackend
	}
	return result, nil
}

func validateTypedAction(action TypedAction) (TypedAction, error) {
	if !validText(action.Kind, maxToolNameBytes) || action.Kind == "" || action.ExpectedRevision == 0 {
		return TypedAction{}, ErrInvalidParams
	}
	allowed := map[string]bool{"move": true, "look": true, "talk": true, "window": true, "battle": true, "battle-end": true, "party": true, "duel": true, "chat": true, "mail": true, "item": true, "pet": true, "status": true, "allocate-stat": true, "social-setting": true, "trade": true}
	if !allowed[action.Kind] || !validText(action.Route, maxRouteBytes) || !validText(action.Command, maxTextBytes) || !validText(action.Text, maxTextBytes) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.X < -1000000 || action.X > 1000000 || action.Y < -1000000 || action.Y > 1000000 || action.Direction < 0 || action.Direction > 7 || action.TargetID < 0 || !validActionIndex(action) || action.WindowType < 0 || action.WindowButton < 0 || action.WindowSequence < 0 || action.WindowObjectID < 0 || action.WindowSelect < 0 || action.Color < 0 || action.Color > 255 || action.Range < 0 || action.Range > 100 || action.PartyRequest < 0 || action.PartyRequest > 1 || action.PetSlot < 0 || action.PetSlot > 9 {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "social-setting" && !validSocialSetting(action.Command, action.Value) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "trade" && !validTradeAction(action) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "move" && action.Route == "" {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "look" && (action.Direction < 0 || action.Direction > 7) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "talk" && action.Command == "" {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "talk" && !strings.HasPrefix(action.Command, "P|") {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "window" && (action.WindowSequence < 0 || action.WindowObjectID < 0 || action.WindowSelect < 0) {
		return TypedAction{}, ErrInvalidParams
	}
	if (action.Kind == "battle" && action.Command == "") || (action.Kind == "chat" && action.Text == "") {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "mail" {
		switch action.Command {
		case "list":
			if action.Index != 0 || action.Text != "" {
				return TypedAction{}, ErrInvalidParams
			}
		case "add":
			// The game backend checks the coordinates against the current
			// authoritative position; the remote adapter only validates shape.
		case "send":
			if action.Index < 0 || action.Index >= 80 || action.Text == "" || len([]byte(action.Text)) > 70 {
				return TypedAction{}, ErrInvalidParams
			}
		default:
			return TypedAction{}, ErrInvalidParams
		}
	}
	if (action.Kind == "party" || action.Kind == "duel") && (action.X < -1000000 || action.X > 1000000 || action.Y < -1000000 || action.Y > 1000000) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "item" && (action.Index < 0 || action.Index > 19) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "allocate-stat" && (action.Index < 0 || action.Index > 3 || action.Value != 0 || action.Value2 != 0 || action.Command != "") {
		return TypedAction{}, ErrInvalidParams
	}
	if (action.Kind == "pet" || action.Kind == "status") && (action.PetSlot < 0 || action.PetSlot > 9) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Range < 0 || action.Range > 100 || action.Color < 0 {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "chat" && (action.Text == "" || len([]byte(action.Text)) > 70) {
		return TypedAction{}, ErrInvalidParams
	}
	if action.Kind == "battle" && !allowedBattleCommand(action.Command) {
		return TypedAction{}, ErrInvalidParams
	}
	return action, nil
}
