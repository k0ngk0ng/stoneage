package aimcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultMaxMessage     = 1 * 1024 * 1024
	defaultMaxOutput      = 1 * 1024 * 1024
	maxToolNameBytes      = 64
	maxMethodBytes        = 128
)

// Logger is intentionally tiny so command-line callers can send diagnostics
// to stderr and tests can capture them.  A logger must never be used for
// protocol responses.
type Logger interface {
	Printf(string, ...any)
}

type Config struct {
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
	RequestTimeout  time.Duration
	MaxMessageBytes int
	MaxOutputBytes  int
	Logger          Logger
}

func DefaultConfig() Config {
	return Config{
		ProtocolVersion: ProtocolVersion,
		ServerName:      ServerName,
		ServerVersion:   ServerVersion,
		RequestTimeout:  defaultRequestTimeout,
		MaxMessageBytes: defaultMaxMessage,
		MaxOutputBytes:  defaultMaxOutput,
		Logger:          log.New(io.Discard, "stoneage-game-mcp: ", log.LstdFlags|log.Lmicroseconds),
	}
}

func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(config.ProtocolVersion) == "" {
		config.ProtocolVersion = defaults.ProtocolVersion
	}
	if strings.TrimSpace(config.ServerName) == "" {
		config.ServerName = defaults.ServerName
	}
	if strings.TrimSpace(config.ServerVersion) == "" {
		config.ServerVersion = defaults.ServerVersion
	}
	if config.RequestTimeout <= 0 || config.RequestTimeout > 5*time.Minute {
		config.RequestTimeout = defaults.RequestTimeout
	}
	if config.MaxMessageBytes <= 0 || config.MaxMessageBytes > 4*1024*1024 {
		config.MaxMessageBytes = defaults.MaxMessageBytes
	}
	if config.MaxOutputBytes <= 0 || config.MaxOutputBytes > 4*1024*1024 {
		config.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if config.Logger == nil {
		config.Logger = defaults.Logger
	}
	return config
}

// Server is a sequential JSON-RPC-over-stdio MCP endpoint.  One Server must
// be created for one fixed role/control generation.  It has no method that
// changes the binding after construction.
type Server struct {
	backend Backend
	binding Binding
	config  Config

	mu          sync.Mutex
	initialized bool
	closed      bool
}

func NewServer(backend Backend, binding Binding, config Config) (*Server, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, fmt.Errorf("%w: nil backend", ErrBackend)
	}
	return &Server{backend: backend, binding: binding, config: normalizeConfig(config)}, nil
}

// Binding returns a copy for diagnostics and backend wiring.  It cannot be
// changed through the returned value.
func (s *Server) Binding() Binding {
	if s == nil {
		return Binding{}
	}
	return s.binding
}

// Serve processes newline-delimited JSON-RPC messages until EOF, context
// cancellation, or an unrecoverable output failure.  It emits no stdout-like
// diagnostics: every diagnostic goes through Config.Logger.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s == nil || s.backend == nil {
		return ErrBackend
	}
	if input == nil || output == nil {
		return errors.New("MCP input and output are required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("MCP server is closed")
	}
	s.mu.Unlock()

	reader := bufio.NewReaderSize(input, 64*1024)
	writer := bufio.NewWriterSize(output, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, tooLarge, err := readLineLimit(reader, s.config.MaxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.close()
				return nil
			}
			return err
		}
		if tooLarge {
			s.config.Logger.Printf("request rejected: message exceeds configured size limit")
			if err := writeRPCError(writer, nil, -32700, "request is too large"); err != nil {
				return err
			}
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		request, err := decodeRequest(line)
		if err != nil {
			s.config.Logger.Printf("request rejected: invalid JSON-RPC envelope")
			if err := writeRPCError(writer, nil, -32600, "invalid request"); err != nil {
				return err
			}
			continue
		}
		response, respond := s.handle(ctx, request)
		if !respond {
			continue
		}
		if err := writeJSONLine(writer, response, s.config.MaxOutputBytes); err != nil {
			return err
		}
	}
}

func (s *Server) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callResult struct {
	Content           []contentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ClientInfo      json.RawMessage `json:"clientInfo"`
	Meta            json.RawMessage `json:"_meta"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta"`
}

func decodeRequest(raw []byte) (rpcRequest, error) {
	if len(raw) == 0 || len(raw) > defaultMaxMessage {
		return rpcRequest{}, ErrInvalidRequest
	}
	if err := rejectDuplicateKeys(raw, 0); err != nil {
		return rpcRequest{}, ErrInvalidRequest
	}
	var request rpcRequest
	if err := decodeStrict(raw, &request); err != nil {
		return rpcRequest{}, ErrInvalidRequest
	}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" || len([]byte(request.Method)) > maxMethodBytes {
		return rpcRequest{}, ErrInvalidRequest
	}
	if len(request.ID) > 0 {
		if bytes.Equal(bytes.TrimSpace(request.ID), []byte("null")) || !validRPCID(request.ID) {
			return rpcRequest{}, ErrInvalidRequest
		}
	}
	if request.Params != nil && len(request.Params) > maxJSONValueBytes {
		return rpcRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func validRPCID(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 128 {
		return false
	}
	if raw[0] == '"' {
		var value string
		return json.Unmarshal(raw, &value) == nil && len([]byte(value)) <= 128
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil || number.String() == "" {
		return false
	}
	// JSON-RPC permits a Number ID.  Reject non-finite-looking values and
	// absurdly long representations while preserving the original bytes.
	_, err := strconv.ParseFloat(number.String(), 64)
	return err == nil
}

func (s *Server) handle(parent context.Context, request rpcRequest) (rpcResponse, bool) {
	response := rpcResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), request.ID...)}
	method := request.Method
	if request.ID == nil && method != "notifications/initialized" {
		// A tool call is a side effect and must never be silently executed as a
		// notification.  Unknown notifications are ignored per JSON-RPC.
		if method == "tools/call" {
			s.config.Logger.Printf("notification rejected: tool calls require an id")
		}
		return rpcResponse{}, false
	}

	switch method {
	case "initialize":
		result, err := s.initialize(request.Params)
		if err != nil {
			response.Error = &rpcError{Code: errorCode(err), Message: publicError(err)}
			return response, true
		}
		response.Result = result
		return response, true
	case "notifications/initialized":
		if err := s.markInitializedNotification(request.Params); err != nil {
			s.config.Logger.Printf("initialized notification rejected: invalid parameters")
		}
		return rpcResponse{}, false
	case "ping":
		if err := s.requireInitialized(false); err != nil {
			response.Error = &rpcError{Code: errorCode(err), Message: publicError(err)}
			return response, true
		}
		response.Result = map[string]any{}
		return response, true
	case "tools/list":
		if err := s.requireInitialized(true); err != nil {
			response.Error = &rpcError{Code: errorCode(err), Message: publicError(err)}
			return response, true
		}
		if err := requireRequestMetaOnly(request.Params); err != nil {
			response.Error = &rpcError{Code: -32602, Message: "invalid parameters"}
			return response, true
		}
		schedules := supportsSchedules(s.backend)
		_, agentMemory := s.backend.(AgentMemoryBackend)
		response.Result = map[string]any{"tools": toolDefinitions(schedules, agentMemory)}
		return response, true
	case "tools/call":
		if err := s.requireInitialized(true); err != nil {
			response.Error = &rpcError{Code: errorCode(err), Message: publicError(err)}
			return response, true
		}
		result, err := s.callTool(parent, request.Params)
		if err != nil {
			if errors.Is(err, ErrUnknownTool) {
				response.Error = &rpcError{Code: -32601, Message: "unknown tool"}
			} else if errors.Is(err, ErrInvalidParams) {
				response.Error = &rpcError{Code: -32602, Message: "invalid parameters"}
			} else {
				s.config.Logger.Printf("tool call failed: name=%q error=%T", toolNameFromParams(request.Params), err)
				response.Result = errorCallResult(publicError(err))
			}
			return response, true
		}
		response.Result = result
		return response, true
	default:
		response.Error = &rpcError{Code: -32601, Message: "method not found"}
		return response, true
	}
}

func supportsSchedules(backend Backend) bool {
	if capability, ok := backend.(interface{ SupportsSchedules() bool }); ok {
		return capability.SupportsSchedules()
	}
	_, ok := backend.(ScheduleBackend)
	return ok
}

func (s *Server) initialize(raw json.RawMessage) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized {
		return nil, ErrAlreadyStarted
	}
	var params initializeParams
	if err := decodeObjectOrEmpty(raw, &params, false); err != nil {
		return nil, ErrInvalidParams
	}
	if params.ProtocolVersion == "" || len([]byte(params.ProtocolVersion)) > 64 {
		return nil, ErrInvalidParams
	}
	if params.Capabilities == nil || !isJSONObject(params.Capabilities) || params.ClientInfo == nil || !isJSONObject(params.ClientInfo) {
		return nil, ErrInvalidParams
	}
	if err := validateRequestMeta(params.Meta); err != nil {
		return nil, ErrInvalidParams
	}
	// This minimal endpoint only needs the tools capability.  Accept known
	// client protocol versions and answer with our stable version.
	if !supportedProtocol(params.ProtocolVersion) {
		return nil, fmt.Errorf("%w: unsupported protocol", ErrInvalidParams)
	}
	s.initialized = true
	return map[string]any{
		"protocolVersion": s.config.ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.config.ServerName,
			"version": s.config.ServerVersion,
		},
	}, nil
}

func supportedProtocol(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		return true
	default:
		return false
	}
}

func (s *Server) markInitializedNotification(raw json.RawMessage) error {
	if err := s.requireInitialized(false); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	return requireRequestMetaOnly(raw)
}

func (s *Server) requireInitialized(require bool) error {
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	if require && !initialized {
		return ErrNotInitialized
	}
	return nil
}

func (s *Server) callTool(parent context.Context, raw json.RawMessage) (callResult, error) {
	var params callParams
	if err := decodeObjectOrEmpty(raw, &params, false); err != nil || params.Name == "" || len([]byte(params.Name)) > maxToolNameBytes {
		return callResult{}, ErrInvalidParams
	}
	if err := validateRequestMeta(params.Meta); err != nil {
		return callResult{}, ErrInvalidParams
	}
	if params.Arguments == nil {
		params.Arguments = json.RawMessage(`{}`)
	}
	if !isJSONObject(params.Arguments) || len(params.Arguments) > maxJSONValueBytes {
		return callResult{}, ErrInvalidParams
	}
	if err := rejectDuplicateKeys(params.Arguments, 0); err != nil {
		return callResult{}, ErrInvalidParams
	}

	requestCtx, cancel := context.WithTimeout(parent, s.config.RequestTimeout)
	defer cancel()
	var value any
	var err error
	switch params.Name {
	case "game_observe":
		var args emptyParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			var result Observation
			result, err = s.backend.Observe(requestCtx, s.binding)
			if err == nil {
				if result.CharacterID == "" {
					result.CharacterID = s.binding.CharacterID
				}
				err = validateObservation(result, s.binding)
				value = result
			}
		}
	case "game_query_knowledge":
		var args knowledgeParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			query := KnowledgeQuery{Kind: args.Kind, ID: args.ID, Text: args.Text, Limit: args.Limit}
			err = validateKnowledgeQuery(query)
			if err == nil {
				var result KnowledgeResult
				result, err = s.backend.QueryKnowledge(requestCtx, s.binding, query)
				if err == nil {
					err = validateKnowledgeResult(result)
					value = result
				}
			}
		}
	case "game_start_task":
		var args taskParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			req := TaskRequest{TaskID: args.TaskID, Parameters: args.Parameters}
			err = validateTaskRequest(req)
			if err == nil {
				var result TaskReceipt
				result, err = s.backend.StartTask(requestCtx, s.binding, req)
				if err == nil {
					err = validReceipt(result)
					value = result
				}
			}
		}
	case "game_start_leveling":
		var args levelingParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			req := LevelingRequest{TargetKind: args.TargetKind, TargetID: args.TargetID, TargetLevel: args.TargetLevel, TargetPolicy: args.TargetPolicy, MaximumSeconds: args.MaximumSeconds, MaximumDeaths: args.MaximumDeaths, Parameters: args.Parameters}
			err = validateLevelingRequest(req)
			if err == nil {
				var result TaskReceipt
				result, err = s.backend.StartLeveling(requestCtx, s.binding, req)
				if err == nil {
					err = validReceipt(result)
					value = result
				}
			}
		}
	case "game_task_status":
		var args handleParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			err = validateHandle(args.Handle)
			if err == nil {
				var result TaskReceipt
				result, err = s.backend.TaskStatus(requestCtx, s.binding, args.Handle)
				if err == nil {
					err = validReceipt(result)
					value = result
				}
			}
		}
	case "game_cancel":
		var args cancelParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			if len([]byte(args.Reason)) > maxTextBytes || !validText(args.Reason, maxTextBytes) {
				err = ErrInvalidParams
			} else {
				err = validateHandle(args.Handle)
			}
			if err == nil {
				var result TaskReceipt
				result, err = s.backend.Cancel(requestCtx, s.binding, CancelRequest{Handle: args.Handle, Reason: args.Reason})
				if err == nil {
					err = validReceipt(result)
					value = result
				}
			}
		}
	case "game_action":
		var args actionParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			var action TypedAction
			action, err = args.action()
			if err == nil {
				var result ActionReceipt
				result, err = s.backend.GameAction(requestCtx, s.binding, action)
				if err == nil {
					err = validReceipt(result)
					value = result
				}
			}
		}
	case "game_memory_write":
		backend, ok := s.backend.(AgentMemoryBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args AgentNoteWriteRequest
		if err = decodeObject(params.Arguments, &args); err == nil {
			err = args.Validate()
			if err == nil {
				var result AgentNote
				result, err = backend.WriteAgentNote(requestCtx, s.binding, args)
				if err == nil {
					err = result.Validate()
					value = result
				}
			}
		}
	case "game_memory_list":
		backend, ok := s.backend.(AgentMemoryBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args AgentNoteListRequest
		if err = decodeObject(params.Arguments, &args); err == nil {
			err = args.Validate()
			if err == nil {
				var result AgentNoteList
				result, err = backend.ListAgentNotes(requestCtx, s.binding, args)
				if err == nil {
					err = result.Validate()
					value = result
				}
			}
		}
	case "game_memory_delete":
		backend, ok := s.backend.(AgentMemoryBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args AgentNoteDeleteRequest
		if err = decodeObject(params.Arguments, &args); err == nil {
			err = args.Validate()
			if err == nil {
				err = backend.DeleteAgentNote(requestCtx, s.binding, args)
				if err == nil {
					value = AgentNoteDeleteResult{Deleted: true, Key: strings.TrimSpace(args.Key)}
				}
			}
		}
	case "game_schedule_create":
		backend, ok := s.backend.(ScheduleBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args scheduleCreateParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			request := ScheduleRequest{Kind: args.Kind, Title: args.Title, Prompt: args.Prompt,
				RunAt: args.RunAt, DelaySeconds: args.DelaySeconds, RepeatSeconds: args.RepeatSeconds,
				IdempotencyKey: args.IdempotencyKey}
			err = validateScheduleRequest(request)
			if err == nil {
				var result Schedule
				result, err = backend.CreateSchedule(requestCtx, s.binding, request)
				if err == nil {
					err = validateSchedule(result)
					value = result
				}
			}
		}
	case "game_schedule_list":
		backend, ok := s.backend.(ScheduleBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args scheduleListParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			request := ScheduleListRequest{Status: args.Status, Limit: args.Limit}
			err = validateScheduleListRequest(request)
			if err == nil {
				var result ScheduleList
				result, err = backend.ListSchedules(requestCtx, s.binding, request)
				if err == nil {
					if len(result.Schedules) > 100 {
						err = ErrInvalidParams
					} else {
						for _, schedule := range result.Schedules {
							if err = validateSchedule(schedule); err != nil {
								break
							}
						}
					}
					value = result
				}
			}
		}
	case "game_schedule_cancel":
		backend, ok := s.backend.(ScheduleBackend)
		if !ok {
			return callResult{}, ErrUnknownTool
		}
		var args scheduleCancelParams
		if err = decodeObject(params.Arguments, &args); err == nil {
			request := ScheduleCancelRequest{ScheduleID: args.ScheduleID, Reason: args.Reason}
			err = validateScheduleCancelRequest(request)
			if err == nil {
				var result Schedule
				result, err = backend.CancelSchedule(requestCtx, s.binding, request)
				if err == nil {
					err = validateSchedule(result)
					value = result
				}
			}
		}
	default:
		return callResult{}, ErrUnknownTool
	}
	if err != nil {
		if errors.Is(err, ErrInvalidParams) || errors.Is(err, ErrUnknownTool) {
			return callResult{}, ErrInvalidParams
		}
		return errorCallResult(publicError(err)), nil
	}
	return successCallResult(value)
}

type emptyParams struct{}

type knowledgeParams struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Text  string `json:"text"`
	Limit int    `json:"limit"`
}

type taskParams struct {
	TaskID     string                     `json:"task_id"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}

type levelingParams struct {
	TargetKind     string                     `json:"target_kind"`
	TargetID       string                     `json:"target_id"`
	TargetLevel    int                        `json:"target_level"`
	TargetPolicy   string                     `json:"target_policy"`
	MaximumSeconds int                        `json:"maximum_seconds"`
	MaximumDeaths  int                        `json:"maximum_deaths"`
	Parameters     map[string]json.RawMessage `json:"parameters"`
}

type handleParams struct {
	Handle string `json:"handle"`
}

type cancelParams struct {
	Handle string `json:"handle"`
	Reason string `json:"reason"`
}

type scheduleCreateParams struct {
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Prompt         string `json:"prompt"`
	RunAt          string `json:"run_at"`
	DelaySeconds   int64  `json:"delay_seconds"`
	RepeatSeconds  int64  `json:"repeat_seconds"`
	IdempotencyKey string `json:"idempotency_key"`
}

type scheduleListParams struct {
	Status string `json:"status"`
	Limit  int    `json:"limit"`
}

type scheduleCancelParams struct {
	ScheduleID string `json:"schedule_id"`
	Reason     string `json:"reason"`
}

// Pointers retain whether a value was supplied, so zero is not confused with
// an omitted coordinate, slot, or protocol option.
type actionParams struct {
	Kind             string  `json:"kind"`
	ExpectedRevision *uint64 `json:"expected_revision"`
	X                *int32  `json:"x"`
	Y                *int32  `json:"y"`
	Direction        *int32  `json:"direction"`
	Route            string  `json:"route"`
	TargetID         *int32  `json:"target_id"`
	Index            *int32  `json:"index"`
	Value            *int32  `json:"value"`
	Value2           *int32  `json:"value2"`
	WindowType       *int32  `json:"window_type"`
	WindowButton     *int32  `json:"window_button"`
	WindowSequence   *int32  `json:"window_sequence"`
	WindowObjectID   *int32  `json:"window_object_id"`
	WindowSelect     *int32  `json:"window_select"`
	Command          string  `json:"command"`
	Text             string  `json:"text"`
	Color            *int32  `json:"color"`
	Range            *int32  `json:"range"`
	PartyRequest     *int32  `json:"party_request"`
	PetSlot          *int32  `json:"pet_slot"`
}

func (a actionParams) action() (TypedAction, error) {
	if a.Kind == "" || len([]byte(a.Kind)) > maxToolNameBytes {
		return TypedAction{}, ErrInvalidParams
	}
	if a.ExpectedRevision == nil || *a.ExpectedRevision == 0 {
		return TypedAction{}, ErrInvalidParams
	}
	allowed := map[string]bool{"move": true, "look": true, "talk": true, "window": true, "battle": true, "battle-end": true, "party": true, "duel": true, "chat": true, "mail": true, "item": true, "pet": true, "status": true, "allocate-stat": true, "social-setting": true, "trade": true}
	if !allowed[a.Kind] {
		return TypedAction{}, ErrInvalidParams
	}
	if !validText(a.Route, maxRouteBytes) || !validText(a.Command, maxTextBytes) || !validText(a.Text, maxTextBytes) {
		return TypedAction{}, ErrInvalidParams
	}
	checkInt32 := func(value *int32, minimum, maximum int32) bool {
		return value == nil || (*value >= minimum && *value <= maximum)
	}
	if !checkInt32(a.X, -1000000, 1000000) || !checkInt32(a.Y, -1000000, 1000000) || !checkInt32(a.Direction, 0, 7) || !checkInt32(a.TargetID, 0, 2147483647) || !checkInt32(a.Index, 0, 255) || !checkInt32(a.WindowType, 0, 2147483647) || !checkInt32(a.WindowButton, 0, 2147483647) || !checkInt32(a.WindowSequence, 0, 2147483647) || !checkInt32(a.WindowObjectID, 0, 2147483647) || !checkInt32(a.WindowSelect, 0, 2147483647) || !checkInt32(a.Color, 0, 255) || !checkInt32(a.Range, 0, 100) || !checkInt32(a.PartyRequest, 0, 1) || !checkInt32(a.PetSlot, 0, 9) {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "move" {
		if a.X == nil || a.Y == nil || a.Route == "" {
			return TypedAction{}, ErrInvalidParams
		}
	}
	if a.Kind == "look" && a.Direction == nil {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "talk" && (a.X == nil || a.Y == nil || a.Command == "") {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "window" && (a.X == nil || a.Y == nil || a.WindowSequence == nil || a.WindowObjectID == nil || a.WindowSelect == nil) {
		return TypedAction{}, ErrInvalidParams
	}
	if (a.Kind == "battle" && a.Command == "") || (a.Kind == "chat" && a.Text == "") {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "mail" {
		switch a.Command {
		case "list":
			if a.Index != nil || a.Text != "" {
				return TypedAction{}, ErrInvalidParams
			}
		case "add":
			if a.X == nil || a.Y == nil {
				return TypedAction{}, ErrInvalidParams
			}
		case "send":
			if a.Index == nil || a.Text == "" || *a.Index < 0 || *a.Index >= 80 {
				return TypedAction{}, ErrInvalidParams
			}
		default:
			return TypedAction{}, ErrInvalidParams
		}
	}
	if (a.Kind == "party" || a.Kind == "duel") && (a.X == nil || a.Y == nil) {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "item" && a.Index == nil {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "allocate-stat" && (a.Index == nil || *a.Index < 0 || *a.Index > 3 || a.Value != nil || a.Value2 != nil || a.Command != "") {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "pet" && a.Command != "standby" && a.PetSlot == nil {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "pet" && a.Command == "standby" && a.Value == nil {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "social-setting" && (a.Value == nil || !validSocialSetting(a.Command, *a.Value)) {
		return TypedAction{}, ErrInvalidParams
	}
	if a.Kind == "trade" {
		switch a.Command {
		case "request":
			if a.TargetID == nil {
				return TypedAction{}, ErrInvalidParams
			}
		case "offer-item", "offer-gold":
			if a.Index == nil || a.Value == nil {
				return TypedAction{}, ErrInvalidParams
			}
		case "offer-pet":
			if a.PetSlot == nil {
				return TypedAction{}, ErrInvalidParams
			}
		case "lock", "confirm", "cancel":
		default:
			return TypedAction{}, ErrInvalidParams
		}
	}
	if a.Kind == "status" && a.Command == "" {
		return TypedAction{}, ErrInvalidParams
	}

	result := TypedAction{Kind: a.Kind, ExpectedRevision: *a.ExpectedRevision, Route: a.Route, Command: a.Command, Text: a.Text}
	copyInt32 := func(pointer *int32) int32 {
		if pointer == nil {
			return 0
		}
		return *pointer
	}
	result.X, result.Y, result.Direction = copyInt32(a.X), copyInt32(a.Y), copyInt32(a.Direction)
	result.TargetID, result.Index = copyInt32(a.TargetID), copyInt32(a.Index)
	result.Value, result.Value2 = copyInt32(a.Value), copyInt32(a.Value2)
	result.WindowType, result.WindowButton = copyInt32(a.WindowType), copyInt32(a.WindowButton)
	result.WindowSequence, result.WindowObjectID, result.WindowSelect = copyInt32(a.WindowSequence), copyInt32(a.WindowObjectID), copyInt32(a.WindowSelect)
	result.Color, result.Range = copyInt32(a.Color), copyInt32(a.Range)
	result.PartyRequest, result.PetSlot = copyInt32(a.PartyRequest), copyInt32(a.PetSlot)
	if result.Kind == "chat" && len([]byte(result.Text)) > 70 {
		return TypedAction{}, ErrInvalidParams
	}
	if result.Kind == "mail" && result.Command == "send" && len([]byte(result.Text)) > 70 {
		return TypedAction{}, ErrInvalidParams
	}
	if result.Kind == "trade" && !validTradeAction(result) {
		return TypedAction{}, ErrInvalidParams
	}
	if !validActionIndex(result) {
		return TypedAction{}, ErrInvalidParams
	}
	if result.Kind == "battle" && !allowedBattleCommand(result.Command) {
		return TypedAction{}, ErrInvalidParams
	}
	if result.Kind == "talk" && !strings.HasPrefix(result.Command, "P|") {
		return TypedAction{}, ErrInvalidParams
	}
	return result, nil
}

func validActionIndex(action TypedAction) bool {
	if action.Kind == "mail" {
		return action.Index >= 0 && action.Index <= 79
	}
	if action.Index >= 0 && action.Index <= 19 {
		return true
	}
	return action.Kind == "battle" && strings.EqualFold(strings.TrimSpace(action.Command), "pet") && action.Index == 255 && action.TargetID == 255
}

func allowedBattleCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "attack", "defend", "escape", "guard", "pet", "item", "skill", "wait":
		return true
	default:
		return false
	}
}

func validateObservation(observation Observation, binding Binding) error {
	if !validTradeObservation(observation.Trade) {
		return ErrInvalidParams
	}
	if observation.CharacterID != "" && observation.CharacterID != binding.CharacterID {
		return fmt.Errorf("%w: observation belongs to another character", ErrInvalidBinding)
	}
	if observation.CharacterID == "" {
		observation.CharacterID = binding.CharacterID
	}
	if len(observation.Pets) > 64 || len(observation.Party) > 16 || len(observation.Actors) > 256 || len(observation.InventoryItems) > 20 || len(observation.Windows) > 64 || len(observation.Chat) > 256 || len(observation.AddressBook) > 80 || len(observation.Battle.Participants) > 64 {
		return ErrInvalidParams
	}
	if len(observation.Inventory) > 256 || len(observation.Flags) > 512 {
		return ErrInvalidParams
	}
	checkEntity := func(entity Entity) error {
		if len([]byte(entity.ID)) > maxCharacterIDBytes || len([]byte(entity.Name)) > maxTextBytes || len(entity.Skills) > 128 || !validText(entity.ID, maxCharacterIDBytes) || !validText(entity.Name, maxTextBytes) {
			return ErrInvalidParams
		}
		for _, skill := range entity.Skills {
			if len([]byte(skill)) > maxTextBytes || !validText(skill, maxTextBytes) {
				return ErrInvalidParams
			}
		}
		return nil
	}
	if err := checkEntity(observation.Character); err != nil {
		return err
	}
	for _, pet := range observation.Pets {
		if err := checkEntity(pet); err != nil {
			return err
		}
	}
	for _, member := range observation.Party {
		if len([]byte(member.ID)) > maxCharacterIDBytes || len([]byte(member.Name)) > maxTextBytes || !validText(member.ID, maxCharacterIDBytes) || !validText(member.Name, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	if observation.ActiveWindow != nil && (len([]byte(observation.ActiveWindow.Data)) > maxTextBytes || !validDisplayText(observation.ActiveWindow.Data, maxTextBytes)) {
		return ErrInvalidParams
	}
	if !validText(observation.CharacterName, maxTextBytes) {
		return ErrInvalidParams
	}
	for _, actor := range observation.Actors {
		if len([]byte(actor.Name)) > maxTextBytes || len([]byte(actor.FreeName)) > maxTextBytes || len([]byte(actor.Title)) > maxTextBytes || len([]byte(actor.PetName)) > maxTextBytes || !validText(actor.Name, maxTextBytes) || !validText(actor.FreeName, maxTextBytes) || !validText(actor.Title, maxTextBytes) || !validText(actor.PetName, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	for _, window := range observation.Windows {
		if len([]byte(window.Data)) > maxTextBytes || !validDisplayText(window.Data, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	for _, message := range observation.Chat {
		if len([]byte(message.At)) > maxTextBytes || len([]byte(message.Channel)) > maxTextBytes || len([]byte(message.Text)) > maxTextBytes || !validText(message.At, maxTextBytes) || !validText(message.Channel, maxTextBytes) || !validDisplayText(message.Text, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	seenAddressBook := make(map[int]struct{}, len(observation.AddressBook))
	for _, entry := range observation.AddressBook {
		if entry.Index < 0 || entry.Index >= 80 || entry.Level < 0 || entry.DuelPoint < 0 || entry.Graphic < 0 || entry.Transmigration < 0 || len([]byte(entry.Name)) > maxTextBytes || !validText(entry.Name, maxTextBytes) {
			return ErrInvalidParams
		}
		if _, exists := seenAddressBook[entry.Index]; exists {
			return ErrInvalidParams
		}
		seenAddressBook[entry.Index] = struct{}{}
	}
	for _, item := range observation.InventoryItems {
		if item.Index < 0 || item.Index >= 20 || len([]byte(item.Name)) > maxTextBytes || len([]byte(item.Name2)) > maxTextBytes || len([]byte(item.Memo)) > maxTextBytes || !validText(item.Name, maxTextBytes) || !validText(item.Name2, maxTextBytes) || !validDisplayText(item.Memo, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	for _, participant := range observation.Battle.Participants {
		if participant.ID < 0 || len([]byte(participant.Name)) > maxTextBytes || !validText(participant.Name, maxTextBytes) {
			return ErrInvalidParams
		}
	}
	return nil
}

func validateKnowledgeQuery(query KnowledgeQuery) error {
	switch query.Kind {
	case "facts", "task", "leveling", "route", "rule":
	default:
		return ErrInvalidParams
	}
	if len([]byte(query.ID)) > maxTaskIDBytes || len([]byte(query.Text)) > 512 || !validText(query.ID, maxTaskIDBytes) || !validText(query.Text, 512) {
		return ErrInvalidParams
	}
	if query.Limit == 0 {
		query.Limit = 10
	}
	if query.Limit < 1 || query.Limit > 50 {
		return ErrInvalidParams
	}
	return nil
}

func validateKnowledgeResult(result KnowledgeResult) error {
	if len([]byte(result.Revision)) > 128 || len([]byte(result.Kind)) > 64 || len(result.Entries) > 50 {
		return ErrInvalidParams
	}
	for _, entry := range result.Entries {
		if len([]byte(entry.ID)) > maxTaskIDBytes || len([]byte(entry.Title)) > maxTextBytes || len([]byte(entry.Summary)) > maxTextBytes || len([]byte(entry.Source)) > maxTextBytes || len(entry.Data) > maxJSONValueBytes || (entry.Data != nil && !json.Valid(entry.Data)) {
			return ErrInvalidParams
		}
	}
	return nil
}

func validateTaskRequest(request TaskRequest) error {
	if len([]byte(request.TaskID)) == 0 || len([]byte(request.TaskID)) > maxTaskIDBytes || !validText(request.TaskID, maxTaskIDBytes) {
		return ErrInvalidParams
	}
	return validateParameters(request.Parameters)
}

func validateLevelingRequest(request LevelingRequest) error {
	if request.TargetKind != "character" && request.TargetKind != "pet" {
		return ErrInvalidParams
	}
	if request.TargetKind == "pet" && (len([]byte(request.TargetID)) == 0 || len([]byte(request.TargetID)) > maxCharacterIDBytes) {
		return ErrInvalidParams
	}
	if request.TargetLevel < 1 || request.TargetLevel > 1000 || !validText(request.TargetID, maxCharacterIDBytes) {
		return ErrInvalidParams
	}
	if request.TargetPolicy == "" {
		request.TargetPolicy = "all"
	}
	if request.TargetPolicy != "all" && request.TargetPolicy != "any" {
		return ErrInvalidParams
	}
	if request.MaximumSeconds < 0 || request.MaximumSeconds > 30*24*60*60 || request.MaximumDeaths < 0 || request.MaximumDeaths > 1000 {
		return ErrInvalidParams
	}
	return validateParameters(request.Parameters)
}

func validateParameters(parameters map[string]json.RawMessage) error {
	if len(parameters) > 32 {
		return ErrInvalidParams
	}
	for key, raw := range parameters {
		if len([]byte(key)) == 0 || len([]byte(key)) > 128 || !validText(key, 128) || len(raw) == 0 || len(raw) > maxJSONValueBytes || !json.Valid(raw) {
			return ErrInvalidParams
		}
		if err := rejectDuplicateKeys(raw, 0); err != nil {
			return ErrInvalidParams
		}
	}
	return nil
}

func validateHandle(handle string) error {
	if len([]byte(handle)) == 0 || len([]byte(handle)) > maxHandleBytes || !validText(handle, maxHandleBytes) {
		return ErrInvalidParams
	}
	return nil
}

// Display fields carry legacy line breaks; identifiers and tool arguments
// continue to use validText's single-line contract.
func validDisplayText(value string, maximum int) bool {
	if len([]byte(value)) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\t' && character != '\n' && character != '\r' {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	if len([]byte(value)) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\t' {
			return false
		}
	}
	return true
}

func successCallResult(value any) (callResult, error) {
	encoded, err := encodeJSON(value)
	if err != nil {
		return callResult{}, ErrInvalidParams
	}
	return callResult{Content: []contentBlock{{Type: "text", Text: string(encoded)}}, StructuredContent: value}, nil
}

func errorCallResult(message string) callResult {
	value := map[string]string{"error": message}
	encoded, _ := json.Marshal(value)
	return callResult{Content: []contentBlock{{Type: "text", Text: string(encoded)}}, StructuredContent: value, IsError: true}
}

func publicError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, ErrInvalidParams) {
		return "invalid parameters"
	}
	if errors.Is(err, ErrNotInitialized) {
		return "MCP session is not initialized"
	}
	if errors.Is(err, ErrBackend) {
		return "game service unavailable"
	}
	return "game operation failed"
}

func errorCode(err error) int {
	switch {
	case errors.Is(err, ErrInvalidParams):
		return -32602
	case errors.Is(err, ErrNotInitialized), errors.Is(err, ErrAlreadyStarted):
		return -32002
	default:
		return -32603
	}
}

func writeRPCError(writer *bufio.Writer, id json.RawMessage, code int, message string) error {
	return writeJSONLine(writer, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}, defaultMaxOutput)
}

func writeJSONLine(writer *bufio.Writer, value any, limit int) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > limit {
		return errors.New("MCP response exceeds configured size limit")
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}

func readLineLimit(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		limit = defaultMaxMessage
	}
	line := make([]byte, 0, minInt(limit, 64*1024))
	tooLarge := false
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 && !tooLarge {
			if len(line)+len(part) > limit {
				tooLarge = true
			} else {
				line = append(line, part...)
			}
		}
		if err == nil {
			return line, tooLarge, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 && !tooLarge {
				return nil, false, io.EOF
			}
			return line, tooLarge, nil
		}
		return nil, false, err
	}
}

func decodeStrict(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxJSONValueBytes {
		return ErrInvalidParams
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON values")
	}
	return nil
}

func decodeObject(raw []byte, target any) error {
	if !isJSONObject(raw) || rejectDuplicateKeys(raw, 0) != nil {
		return ErrInvalidParams
	}
	if err := decodeStrict(raw, target); err != nil {
		return ErrInvalidParams
	}
	return nil
}

func decodeObjectOrEmpty(raw []byte, target any, allowEmpty bool) error {
	if raw == nil || len(bytes.TrimSpace(raw)) == 0 {
		if allowEmpty {
			return nil
		}
		return ErrInvalidParams
	}
	return decodeObject(raw, target)
}

func requireEmptyObject(raw []byte) error {
	if raw == nil {
		return nil
	}
	var value map[string]json.RawMessage
	if err := decodeObject(raw, &value); err != nil || len(value) != 0 {
		return ErrInvalidParams
	}
	return nil
}

// MCP reserves _meta for request metadata such as progressToken. Keep the
// method parameter surface closed while accepting that standard extension.
// Tool arguments remain separately decoded and cannot use metadata to smuggle
// arbitrary game fields into an action.
func requireRequestMetaOnly(raw []byte) error {
	if raw == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if !isJSONObject(raw) || rejectDuplicateKeys(raw, 0) != nil {
		return ErrInvalidParams
	}
	var fields map[string]json.RawMessage
	if err := decodeStrict(raw, &fields); err != nil {
		return ErrInvalidParams
	}
	for key, value := range fields {
		if key != "_meta" {
			return ErrInvalidParams
		}
		if err := validateRequestMeta(value); err != nil {
			return err
		}
	}
	return nil
}

func validateRequestMeta(raw json.RawMessage) error {
	if raw == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if len(raw) > maxJSONValueBytes || !isJSONObject(raw) || rejectDuplicateKeys(raw, 0) != nil {
		return ErrInvalidParams
	}
	return nil
}

func isJSONObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) >= 2 && raw[0] == '{' && raw[len(raw)-1] == '}'
}

// rejectDuplicateKeys walks JSON tokens and rejects duplicate object keys at
// every depth.  This closes the common "last duplicate wins" ambiguity before
// a strict struct decoder sees the request.
func rejectDuplicateKeys(raw []byte, depth int) error {
	if depth > 32 {
		return ErrInvalidParams
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSON(decoder, depth); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidParams
	}
	return nil
}

func walkJSON(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	if depth >= 32 {
		return ErrInvalidParams
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			keyString, ok := key.(string)
			if !ok {
				return ErrInvalidParams
			}
			if _, exists := seen[keyString]; exists {
				return ErrInvalidParams
			}
			seen[keyString] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return ErrInvalidParams
	}
}

func toolNameFromParams(raw json.RawMessage) string {
	var value struct {
		Name string `json:"name"`
	}
	if decodeStrict(raw, &value) == nil {
		return value.Name
	}
	return "?"
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
