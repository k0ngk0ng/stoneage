package aimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type agentMemoryTestBackend struct {
	fakeBackend
	writes  []AgentNoteWriteRequest
	deletes []AgentNoteDeleteRequest
}

func (backend *agentMemoryTestBackend) WriteAgentNote(_ context.Context, _ Binding, request AgentNoteWriteRequest) (AgentNote, error) {
	backend.writes = append(backend.writes, request)
	return AgentNote{ID: 1, Key: strings.TrimSpace(request.Key), Text: strings.TrimSpace(request.Text), CreatedAt: "2026-09-16T12:00:00Z", UpdatedAt: "2026-09-16T12:00:00Z"}, nil
}

func (backend *agentMemoryTestBackend) ListAgentNotes(_ context.Context, _ Binding, request AgentNoteListRequest) (AgentNoteList, error) {
	return AgentNoteList{Notes: []AgentNote{{ID: 1, Key: "plan", Text: "return to town", CreatedAt: "2026-09-16T12:00:00Z", UpdatedAt: "2026-09-16T12:00:00Z"}}}, nil
}

func (backend *agentMemoryTestBackend) DeleteAgentNote(_ context.Context, _ Binding, request AgentNoteDeleteRequest) error {
	backend.deletes = append(backend.deletes, request)
	return nil
}

func TestAgentMemoryToolsAreBoundAndProfileIDIsNotAccepted(t *testing.T) {
	backend := &agentMemoryTestBackend{}
	server, binding := testServer(t, backend)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"game_memory_write","arguments":{"key":" plan ","text":" keep this route "}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"game_memory_list","arguments":{"limit":10}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"game_memory_delete","arguments":{"key":"plan"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"game_memory_write","arguments":{"profile_id":"other","key":"x","text":"must reject"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	tools := rpcLine(t, output.Bytes(), 1)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 10 {
		t.Fatalf("tool count = %d, want 10", len(tools))
	}
	write := rpcLine(t, output.Bytes(), 2)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if write["key"] != "plan" || write["text"] != "keep this route" {
		t.Fatalf("write result = %#v", write)
	}
	list := rpcLine(t, output.Bytes(), 3)["result"].(map[string]any)["structuredContent"].(map[string]any)["notes"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["key"] != "plan" {
		t.Fatalf("list result = %#v", list)
	}
	deleted := rpcLine(t, output.Bytes(), 4)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if deleted["deleted"] != true || deleted["key"] != "plan" {
		t.Fatalf("delete result = %#v", deleted)
	}
	if rpcLine(t, output.Bytes(), 5)["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("profile_id was accepted: %s", output.String())
	}
	if len(backend.writes) != 1 || backend.writes[0].Key != " plan " || backend.writes[0].Text != " keep this route " {
		t.Fatalf("write request was altered: %#v", backend.writes)
	}
	if len(backend.deletes) != 1 || backend.deletes[0].Key != "plan" || binding.CharacterID != "char-fixed" {
		t.Fatalf("delete request/binding = %#v/%+v", backend.deletes, binding)
	}
}

func TestAgentMemoryToolsRejectUnboundedValuesAndUnavailableCapability(t *testing.T) {
	backend := &agentMemoryTestBackend{}
	server, _ := testServer(t, backend)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_memory_write","arguments":{"key":"x","text":"` + strings.Repeat("t", MaxAgentNoteTextBytes+1) + `"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"game_memory_list","arguments":{"limit":101}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{1, 2} {
		if code := rpcLine(t, output.Bytes(), index)["error"].(map[string]any)["code"]; code != float64(-32602) {
			t.Fatalf("invalid memory request %d code = %v", index, code)
		}
	}
	if len(backend.writes) != 0 {
		t.Fatal("invalid memory request reached backend")
	}

	plain, _ := testServer(t, &fakeBackend{})
	output.Reset()
	input = strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_memory_list","arguments":{}}}`,
	}, "\n") + "\n"
	if err := plain.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if code := rpcLine(t, output.Bytes(), 1)["error"].(map[string]any)["code"]; code != float64(-32601) {
		t.Fatalf("unavailable memory tool code = %v", code)
	}
}

func TestRemoteBackendAgentMemoryUsesPrivateGatewayOperations(t *testing.T) {
	token := strings.Repeat("a", 43)
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Operation string          `json:"operation"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		operations = append(operations, body.Operation)
		if bytes.Contains(body.Arguments, []byte("profile_id")) || bytes.Contains(body.Arguments, []byte("char-fixed")) {
			t.Errorf("remote memory request leaked binding: %s", body.Arguments)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch body.Operation {
		case "memory_write":
			_, _ = writer.Write([]byte(`{"result":{"id":2,"key":"plan","text":"remember","created_at":"2026-09-16T12:00:00Z","updated_at":"2026-09-16T12:00:00Z"}}`))
		case "memory_list":
			_, _ = writer.Write([]byte(`{"result":{"notes":[{"id":2,"key":"plan","text":"remember","created_at":"2026-09-16T12:00:00Z","updated_at":"2026-09-16T12:00:00Z"}]}}`))
		case "memory_delete":
			_, _ = writer.Write([]byte(`{"result":{"deleted":true,"key":"plan"}}`))
		default:
			_, _ = writer.Write([]byte(`{"error":"unexpected"}`))
		}
	}))
	defer server.Close()
	remote, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{ProfileID: "private-profile", CharacterID: "char-fixed", Generation: 7}
	if note, err := remote.WriteAgentNote(context.Background(), binding, AgentNoteWriteRequest{Key: "plan", Text: "remember"}); err != nil || note.Key != "plan" {
		t.Fatalf("remote write = %#v, %v", note, err)
	}
	if notes, err := remote.ListAgentNotes(context.Background(), binding, AgentNoteListRequest{Limit: 10}); err != nil || len(notes.Notes) != 1 {
		t.Fatalf("remote list = %#v, %v", notes, err)
	}
	if err := remote.DeleteAgentNote(context.Background(), binding, AgentNoteDeleteRequest{Key: "plan"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(operations, ",") != "memory_write,memory_list,memory_delete" {
		t.Fatalf("remote operations = %v", operations)
	}

	if _, err := remote.WriteAgentNote(context.Background(), binding, AgentNoteWriteRequest{Key: "", Text: "bad"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("invalid remote write = %v", err)
	}
}
