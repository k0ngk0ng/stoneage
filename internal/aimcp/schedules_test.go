package aimcp

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type scheduleTestBackend struct {
	fakeBackend
	created ScheduleRequest
	listed  ScheduleListRequest
	cancel  ScheduleCancelRequest
}

func (backend *scheduleTestBackend) CreateSchedule(_ context.Context, _ Binding, request ScheduleRequest) (Schedule, error) {
	backend.created = request
	return Schedule{ID: "schedule-1", Kind: request.Kind, Title: request.Title, Prompt: request.Prompt,
		RunAt: "2026-09-16T12:01:00Z", Status: "pending"}, nil
}

func (backend *scheduleTestBackend) ListSchedules(_ context.Context, _ Binding, request ScheduleListRequest) (ScheduleList, error) {
	backend.listed = request
	return ScheduleList{Schedules: []Schedule{{ID: "schedule-1", Kind: "reminder", Prompt: "check mail", RunAt: "2026-09-16T12:01:00Z", Status: "pending"}}}, nil
}

func (backend *scheduleTestBackend) CancelSchedule(_ context.Context, _ Binding, request ScheduleCancelRequest) (Schedule, error) {
	backend.cancel = request
	return Schedule{ID: request.ScheduleID, Kind: "reminder", Prompt: "check mail", RunAt: "2026-09-16T12:01:00Z", Status: "cancelled"}, nil
}

func TestScheduleToolsAreOptionalAndBound(t *testing.T) {
	backend := &scheduleTestBackend{}
	server, binding := testServer(t, backend)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"game_schedule_create","arguments":{"kind":"reminder","title":"mail","prompt":"Check new mail.","delay_seconds":60,"idempotency_key":"mail-1"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"game_schedule_list","arguments":{"status":"pending","limit":10}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"game_schedule_cancel","arguments":{"schedule_id":"schedule-1","reason":"done"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	list := rpcLine(t, output.Bytes(), 1)["result"].(map[string]any)["tools"].([]any)
	if len(list) != 10 {
		t.Fatalf("schedule tool count = %d", len(list))
	}
	create := rpcLine(t, output.Bytes(), 2)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if create["id"] != "schedule-1" || create["status"] != "pending" {
		t.Fatalf("create result = %#v", create)
	}
	if backend.created.DelaySeconds != 60 || backend.created.IdempotencyKey != "mail-1" || backend.created.Prompt != "Check new mail." {
		t.Fatalf("create request = %#v", backend.created)
	}
	if got := rpcLine(t, output.Bytes(), 3)["result"].(map[string]any)["structuredContent"].(map[string]any)["schedules"].([]any); len(got) != 1 {
		t.Fatalf("list result = %#v", got)
	}
	if backend.listed.Status != "pending" || backend.listed.Limit != 10 {
		t.Fatalf("list request = %#v", backend.listed)
	}
	if cancel := rpcLine(t, output.Bytes(), 4)["result"].(map[string]any)["structuredContent"].(map[string]any); cancel["status"] != "cancelled" {
		t.Fatalf("cancel result = %#v", cancel)
	}
	if backend.cancel.ScheduleID != "schedule-1" || backend.cancel.Reason != "done" {
		t.Fatalf("cancel request = %#v", backend.cancel)
	}
	if binding.CharacterID != "char-fixed" {
		t.Fatal("test binding unexpectedly changed")
	}
}

func TestScheduleToolsRejectUnsafeInputsBeforeBackend(t *testing.T) {
	backend := &scheduleTestBackend{}
	server, _ := testServer(t, backend)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_schedule_create","arguments":{"kind":"reminder","prompt":"x","run_at":"2026-09-16T12:01:00Z","delay_seconds":60}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"game_schedule_create","arguments":{"kind":"reminder","prompt":"x","delay_seconds":0}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{1, 2} {
		response := rpcLine(t, output.Bytes(), index)
		if response["error"].(map[string]any)["code"] != float64(-32602) {
			t.Fatalf("invalid schedule response = %#v", response)
		}
	}
	if backend.created.Prompt != "" {
		t.Fatal("invalid schedule reached backend")
	}
}
