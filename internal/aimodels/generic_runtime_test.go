package aimodels_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
)

const (
	genericRuntimeOptIn = "STONEAGE_GENERIC_RUNTIME_TEST"
	genericModel        = "stoneage-generic-local-test-model"
	dummyKey            = "stoneage-generic-dummy-key"
	expectedText        = "STONEAGE_GENERIC_RUNTIME_OK"
)

// TestGenericRuntimeWithLocalResponses is intentionally opt-in. It starts the
// real local Codex executable, but the provider endpoint is always an
// httptest listener; no third-party model or credential is contacted.
func TestGenericRuntimeWithLocalResponses(t *testing.T) {
	if os.Getenv(genericRuntimeOptIn) != "1" {
		t.Skip("set STONEAGE_GENERIC_RUNTIME_TEST=1 to run the real local Codex integration")
	}
	binary := os.Getenv("STONEAGE_CODEX_BINARY")
	if binary == "" {
		binary = "/Users/jason/.local/bin/codex"
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("STONEAGE_CODEX_BINARY must be an absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatalf("real Codex executable is unavailable: %s", binary)
	}

	repoRoot := repositoryRoot(t)
	root := filepath.Join(repoRoot, "build", "ai", fmt.Sprintf("generic-runtime-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	for _, test := range []struct {
		name       string
		provider   string
		providerID string
	}{
		{name: "custom", provider: "custom", providerID: "custom"},
		{name: "logical-openai", provider: "openai", providerID: "stoneage_openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runGenericRuntimeCase(t, root, binary, test.provider, test.providerID)
		})
	}
}

type responsesCapture struct {
	mu          sync.Mutex
	path        string
	authority   string
	contentType string
	body        map[string]json.RawMessage
	requests    int
}

func (c *responsesCapture) handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	c.mu.Lock()
	c.requests++
	c.path = r.URL.Path
	c.authority = r.Header.Get("Authorization")
	c.contentType = r.Header.Get("Content-Type")
	if json.Unmarshal(body, &c.body) != nil {
		c.body = nil
	}
	c.mu.Unlock()
	if err != nil {
		http.Error(w, "request read failed", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	for _, frame := range responsesSSEFrames() {
		_, _ = io.WriteString(w, frame)
		flusher.Flush()
	}
}

func runGenericRuntimeCase(t *testing.T, root, binary, provider, providerID string) {
	t.Helper()
	capture := &responsesCapture{}
	server := httptest.NewServer(http.HandlerFunc(capture.handler))
	t.Cleanup(server.Close)

	caseRoot := filepath.Join(root, providerID)
	codexHome := filepath.Join(caseRoot, "codex-home")
	files, err := aimodels.Materialize(codexHome, aimodels.RuntimeSettings{
		Provider: provider, Model: genericModel, BaseURL: server.URL + "/v1", APIKey: dummyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files.ProviderID != providerID {
		t.Fatalf("provider ID=%q, want %q", files.ProviderID, providerID)
	}
	if files.CatalogPath != "" || files.CatalogSHA256 != "" {
		t.Fatalf("generic model unexpectedly received DeepSeek catalog: %+v", files)
	}
	config, err := os.ReadFile(files.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte(aimodels.DeepSeekFlash)) {
		t.Fatal("unknown generic model inherited the DeepSeek model catalog")
	}

	workspaceRoot := filepath.Join(caseRoot, "workspaces")
	stateRoot := filepath.Join(caseRoot, "state")
	runner, err := aicodex.New(aicodex.Config{
		Binary: binary, WorkspaceRoot: workspaceRoot, StateRoot: stateRoot,
		CodexHome: codexHome, ConfigFile: files.ConfigPath,
		Model: genericModel, Provider: aicodex.ProviderConfig{Name: files.ProviderID, BaseURL: server.URL + "/v1", WireAPI: "responses"},
		ApprovalPolicy: "never", SandboxMode: "danger-full-access",
		Limits:           aicodex.Limits{MaxEventBytes: 1 << 20, MaxEvents: 256, MaxStdoutBytes: 8 << 20},
		TerminationGrace: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, aicodex.RunRequest{
		ProfileID: "generic-runtime",
		Prompt:    "Reply with exactly STONEAGE_GENERIC_RUNTIME_OK and nothing else. Do not use tools or run commands.",
	})
	if err != nil {
		t.Fatalf("Codex runtime failed (provider output suppressed): %T", err)
	}
	if result.Process.Status != aicodex.ProcessExited || result.Process.ExitCode != 0 || result.Turn.Status != aicodex.TurnCompleted {
		t.Fatalf("incomplete Codex turn: process=%+v turn=%+v", result.Process, result.Turn)
	}
	if result.ThreadID == "" || result.LastMessage != expectedText {
		t.Fatalf("missing complete response: thread=%q last_message=%q", result.ThreadID, result.LastMessage)
	}
	if !hasEvent(result, "thread.started") || !hasEvent(result, "turn.completed") || !hasExpectedAgentMessage(result) {
		t.Fatalf("Codex JSON event evidence incomplete: events=%d", len(result.Events))
	}

	capture.mu.Lock()
	gotPath, gotAuth, gotContentType, gotBody, requests := capture.path, capture.authority, capture.contentType, capture.body, capture.requests
	capture.mu.Unlock()
	if requests != 1 || gotPath != "/v1/responses" {
		t.Fatalf("provider requests=%d path=%q", requests, gotPath)
	}
	if gotAuth != "Bearer "+dummyKey {
		t.Fatalf("authorization header=%q", gotAuth)
	}
	if !strings.HasPrefix(strings.ToLower(gotContentType), "application/json") {
		t.Fatalf("content type=%q", gotContentType)
	}
	var model string
	if err := json.Unmarshal(gotBody["model"], &model); err != nil || model != genericModel {
		t.Fatalf("request model=%q err=%v", model, err)
	}
	if string(gotBody["stream"]) != "true" {
		t.Fatalf("request was not a Responses stream: %s", gotBody["stream"])
	}
	if bytes.Contains(mustJSON(gotBody), []byte(aimodels.DeepSeekFlash)) {
		t.Fatal("generic request unexpectedly used DeepSeek model metadata")
	}

	profileState := filepath.Join(stateRoot, "generic-runtime")
	processHome := filepath.Join(profileState, "process-home")
	processTemp := filepath.Join(profileState, "tmp")
	workspace := filepath.Join(workspaceRoot, "generic-runtime")
	for _, path := range []string{codexHome, files.ConfigPath, processHome, processTemp, filepath.Join(processHome, "cache"), filepath.Join(processHome, "xdg-config"), workspace, filepath.Join(workspace, ".git"), filepath.Join(profileState, "thread.json")} {
		if !under(path, root) {
			t.Fatalf("runtime path escaped build/ai root: %s", path)
		}
	}
	for _, path := range []string{codexHome, processHome, processTemp, filepath.Join(processHome, "cache"), filepath.Join(processHome, "xdg-config"), workspace, filepath.Join(workspace, ".git"), filepath.Join(profileState, "thread.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("isolated runtime path missing %s: %v", path, err)
		}
	}
	gitRoot, err := exec.Command("git", "-C", workspace, "rev-parse", "--show-toplevel").Output()
	if err != nil || filepath.Clean(strings.TrimSpace(string(gitRoot))) != filepath.Clean(workspace) {
		t.Fatalf("git workspace escaped isolation: root=%q err=%v", strings.TrimSpace(string(gitRoot)), err)
	}
}

func responsesSSEFrames() []string {
	response := func(status string, output any) string {
		value := map[string]any{"id": "resp-stoneage-generic", "object": "response", "status": status, "model": genericModel, "output": output}
		return string(mustJSONValue(value))
	}
	messageInProgress := map[string]any{"id": "msg-stoneage-generic", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	messageDone := map[string]any{"id": "msg-stoneage-generic", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": expectedText, "annotations": []any{}}}}
	part := map[string]any{"type": "output_text", "text": expectedText, "annotations": []any{}}
	frame := func(event string, data any) string {
		return "event: " + event + "\ndata: " + string(mustJSONValue(data)) + "\n\n"
	}
	return []string{
		frame("response.created", map[string]any{"type": "response.created", "response": json.RawMessage(response("in_progress", []any{}))}),
		frame("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": messageInProgress}),
		frame("response.content_part.added", map[string]any{"type": "response.content_part.added", "item_id": "msg-stoneage-generic", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}),
		frame("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg-stoneage-generic", "output_index": 0, "content_index": 0, "delta": expectedText}),
		frame("response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": "msg-stoneage-generic", "output_index": 0, "content_index": 0, "text": expectedText}),
		frame("response.content_part.done", map[string]any{"type": "response.content_part.done", "item_id": "msg-stoneage-generic", "output_index": 0, "content_index": 0, "part": part}),
		frame("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": messageDone}),
		frame("response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(response("completed", []any{messageDone}))}),
		"data: [DONE]\n\n",
	}
}

func hasEvent(result aicodex.Result, eventType string) bool {
	for _, event := range result.Events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func hasExpectedAgentMessage(result aicodex.Result) bool {
	for _, event := range result.Events {
		if event.Type != "item.completed" || event.ItemType != "agent_message" {
			continue
		}
		var item struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Text == expectedText {
			return true
		}
	}
	return false
}

func mustJSON(value map[string]json.RawMessage) []byte {
	data, _ := json.Marshal(value)
	return data
}

func mustJSONValue(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func under(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
