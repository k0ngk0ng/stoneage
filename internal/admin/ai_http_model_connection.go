package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const (
	defaultAIHTTPConnectionTimeout = 60 * time.Second
	defaultAIHTTPMaxResponseBytes  = 1 << 20
)

var (
	errAIHTTPRedirect        = errors.New("model redirect rejected")
	errAIHTTPResponseTooBig  = errors.New("model response is too large")
	errAIHTTPInvalidResponse = errors.New("model returned an invalid response")
)

// AIHTTPModelConnectionTesterOptions contains the server-owned dependencies
// for a direct Responses API connection test. It deliberately has no Codex,
// container, Gateway, or game runtime dependency.
type AIHTTPModelConnectionTesterOptions struct {
	Models            AIModelConfigReader
	Secrets           AIModelSecretReader
	HTTPClient        *http.Client
	ConnectionTimeout time.Duration
	MaxResponseBytes  int64
}

// AIHTTPModelConnectionTester sends a small, capability-free request directly
// to the configured model endpoint. A single process-wide lock prevents two
// administrators from starting simultaneous tests and consuming duplicate
// provider quota.
type AIHTTPModelConnectionTester struct {
	models            AIModelConfigReader
	secrets           AIModelSecretReader
	httpClient        *http.Client
	connectionTimeout time.Duration
	maxResponseBytes  int64
}

var (
	_                       AIModelConnectionTester = (*AIHTTPModelConnectionTester)(nil)
	aiHTTPModelConnectionMu sync.Mutex
)

// NewAIHTTPModelConnectionTester constructs a direct HTTP tester. The
// redirect policy is always replaced, even for a caller-supplied client, so
// an API key cannot be sent to a redirected host.
func NewAIHTTPModelConnectionTester(options AIHTTPModelConnectionTesterOptions) (*AIHTTPModelConnectionTester, error) {
	if options.Models == nil {
		return nil, errors.New("AI model store is required")
	}
	if options.Secrets == nil {
		return nil, errors.New("AI secret store is required")
	}
	if options.ConnectionTimeout <= 0 {
		options.ConnectionTimeout = defaultAIHTTPConnectionTimeout
	}
	if options.ConnectionTimeout > 10*time.Minute {
		return nil, errors.New("HTTP connection test timeout is too long")
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultAIHTTPMaxResponseBytes
	}

	client := http.Client{Transport: http.DefaultTransport}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
		if client.Transport == nil {
			client.Transport = http.DefaultTransport
		}
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errAIHTTPRedirect
	}

	return &AIHTTPModelConnectionTester{
		models:            options.Models,
		secrets:           options.Secrets,
		httpClient:        &client,
		connectionTimeout: options.ConnectionTimeout,
		maxResponseBytes:  options.MaxResponseBytes,
	}, nil
}

// TestModelConfig performs one direct Responses API request. All provider
// response bodies and transport errors are reduced to reviewed categories at
// this boundary; in particular, a provider response can never expose the API
// key through the admin HTTP result.
func (tester *AIHTTPModelConnectionTester) TestModelConfig(ctx context.Context, modelID string) error {
	if tester == nil || tester.models == nil || tester.secrets == nil || tester.httpClient == nil {
		return newAIModelConnectionTestFailure("runtime_unavailable", ErrAIRuntimeUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return classifyAIHTTPContextError(err)
	}
	if !aiHTTPModelConnectionMu.TryLock() {
		return newAIModelConnectionTestFailure("busy", errors.New("model connection test is already running"))
	}
	defer aiHTTPModelConnectionMu.Unlock()

	started := time.Now()
	serverDeadline := started.Add(tester.connectionTimeout)
	testCtx, cancel := context.WithDeadline(ctx, serverDeadline)
	defer cancel()
	config, err := tester.models.GetModelConfig(testCtx, modelID)
	if err != nil {
		if ctxErr := testCtx.Err(); ctxErr != nil {
			return classifyAIHTTPContextError(ctxErr)
		}
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	if ctxErr := testCtx.Err(); ctxErr != nil {
		return classifyAIHTTPContextError(ctxErr)
	}

	wireAPI := strings.TrimSpace(config.WireAPI)
	if wireAPI != "" && wireAPI != airuntime.ModelProviderResponses {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	if strings.TrimSpace(config.Model) == "" {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	endpoint, err := aiResponsesEndpoint(config.BaseURL)
	if err != nil {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	key, err := tester.secrets.ReadKey(modelID)
	key = strings.TrimSpace(key)
	if err != nil || key == "" {
		return newAIModelConnectionTestFailure("missing_key", errors.New("model API key is unavailable"))
	}
	if ctxErr := testCtx.Err(); ctxErr != nil {
		return classifyAIHTTPContextError(ctxErr)
	}

	requestDeadline := serverDeadline
	if config.Timeout > 0 {
		modelDeadline := started.Add(config.Timeout)
		if modelDeadline.Before(requestDeadline) {
			requestDeadline = modelDeadline
		}
	}
	requestCtx, requestCancel := context.WithDeadline(ctx, requestDeadline)
	defer requestCancel()

	// Keep the request intentionally small and capability-free. stream=false
	// is the ordinary Responses API mode; the decoder also accepts SSE because
	// some compatible gateways return an event stream regardless of this flag.
	body, err := json.Marshal(struct {
		Model  string `json:"model"`
		Input  string `json:"input"`
		Stream bool   `json:"stream"`
	}{Model: config.Model, Input: "hi", Stream: false})
	if err != nil {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, err := tester.httpClient.Do(request)
	if err != nil {
		return classifyAIHTTPDoError(requestCtx, err)
	}
	if response == nil {
		return newAIModelConnectionTestFailure("network", errors.New("model HTTP response is missing"))
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if response.Body != nil {
			response.Body.Close()
		}
		return newAIHTTPStatusFailure(response.StatusCode)
	}
	if response.Body == nil {
		return newAIModelConnectionTestFailure("invalid_response", errAIHTTPInvalidResponse)
	}
	defer response.Body.Close()
	limit := tester.maxResponseBytes
	if limit < int64(^uint64(0)>>1) {
		limit++
	}
	limited := io.LimitReader(response.Body, limit)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return classifyAIHTTPDoError(requestCtx, err)
	}
	if int64(len(raw)) > tester.maxResponseBytes {
		return newAIModelConnectionTestFailure("invalid_response", errAIHTTPResponseTooBig)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return newAIModelConnectionTestFailure("invalid_response", errAIHTTPInvalidResponse)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") || aiLooksLikeSSE(raw) {
		if err := parseAIResponsesSSE(raw); err != nil {
			return newAIModelConnectionTestFailure("invalid_response", err)
		}
		return nil
	}
	if err := parseAIResponsesJSON(raw); err != nil {
		return newAIModelConnectionTestFailure("invalid_response", err)
	}
	return nil
}

func aiResponsesEndpoint(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", errors.New("invalid model base URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("model base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(value, "#") {
		return "", errors.New("model base URL must not contain credentials, query, or fragment")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		path = "/responses"
	} else if path != "/responses" && !strings.HasSuffix(path, "/responses") {
		path += "/responses"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

func newAIHTTPStatusFailure(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return newAIModelConnectionTestFailure("authentication", errors.New("model service rejected authentication"))
	case http.StatusTooManyRequests:
		return newAIModelConnectionTestFailure("rate_limit", errors.New("model service rate limited the request"))
	case http.StatusNotFound:
		return newAIModelConnectionTestFailure("model_unavailable", errors.New("model service could not find the endpoint or model"))
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return newAIModelConnectionTestFailure("timeout", context.DeadlineExceeded)
	default:
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
}

func classifyAIHTTPContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newAIModelConnectionTestFailure("timeout", context.DeadlineExceeded)
	}
	return newAIModelConnectionTestFailure("failed", errors.New("model HTTP request was canceled"))
}

func classifyAIHTTPDoError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return classifyAIHTTPContextError(ctxErr)
		}
	}
	if errors.Is(err, errAIHTTPRedirect) {
		return newAIModelConnectionTestFailure("failed", errors.New("model redirect rejected"))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newAIModelConnectionTestFailure("timeout", context.DeadlineExceeded)
	}
	if strings.Contains(strings.ToLower(err.Error()), "client.timeout exceeded") {
		return newAIModelConnectionTestFailure("timeout", context.DeadlineExceeded)
	}
	return newAIModelConnectionTestFailure("network", errors.New("model HTTP request failed"))
}

func aiLooksLikeSSE(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:") {
			return true
		}
	}
	return false
}

func parseAIResponsesJSON(raw []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return errAIHTTPInvalidResponse
	}
	if aiJSONHasError(root) || aiJSONTypeIsFailure(root) {
		return errAIHTTPInvalidResponse
	}
	status := aiJSONStatus(root)
	if !strings.EqualFold(status, "completed") {
		return errAIHTTPInvalidResponse
	}
	if strings.TrimSpace(aiJSONText(root)) == "" {
		return errAIHTTPInvalidResponse
	}
	return nil
}

func parseAIResponsesSSE(raw []byte) error {
	var (
		eventName string
		data      strings.Builder
		text      strings.Builder
		sawDelta  bool
		completed bool
		failed    bool
	)
	dispatch := func() error {
		payload := strings.TrimSpace(data.String())
		name := strings.TrimSpace(eventName)
		eventName = ""
		data.Reset()
		if payload == "" {
			return nil
		}
		if payload == "[DONE]" {
			return nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &object); err != nil {
			return errAIHTTPInvalidResponse
		}
		if name == "" {
			name = aiJSONString(object, "type")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if aiJSONHasError(object) || aiJSONTypeIsFailure(object) || strings.Contains(name, "error") || strings.Contains(name, "failed") || strings.Contains(name, "incomplete") {
			failed = true
			return nil
		}
		switch name {
		case "response.output_text.delta":
			if delta := aiJSONString(object, "delta"); delta != "" {
				text.WriteString(delta)
				sawDelta = true
			}
		case "response.output_text.done":
			if !sawDelta {
				if value := aiJSONString(object, "text"); value != "" {
					text.WriteString(value)
				} else if value := aiJSONString(object, "output_text"); value != "" {
					text.WriteString(value)
				}
			}
		case "response.completed":
			if strings.EqualFold(aiJSONStatus(object), "completed") {
				completed = true
				text.WriteString(aiJSONText(object))
			}
		default:
			// A few compatible gateways omit the event field but include a
			// response object and type in data. Handle a completed payload
			// without treating [DONE] as completion.
			if strings.EqualFold(aiJSONStatus(object), "completed") && strings.EqualFold(aiJSONString(object, "type"), "response.completed") {
				completed = true
				text.WriteString(aiJSONText(object))
			}
		}
		return nil
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line, "data:"))
			continue
		}
		if strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		// SSE permits id/retry fields, but an unknown non-field line is
		// malformed enough to reject rather than silently report success.
		return errAIHTTPInvalidResponse
	}
	if data.Len() > 0 || eventName != "" {
		if err := dispatch(); err != nil {
			return err
		}
	}
	if failed || !completed || strings.TrimSpace(text.String()) == "" {
		return errAIHTTPInvalidResponse
	}
	return nil
}

func aiJSONHasError(value map[string]json.RawMessage) bool {
	if raw, ok := value["error"]; ok && strings.TrimSpace(string(raw)) != "" && strings.TrimSpace(string(raw)) != "null" {
		return true
	}
	if nested, ok := aiJSONObject(value, "response"); ok {
		if raw, exists := nested["error"]; exists && strings.TrimSpace(string(raw)) != "" && strings.TrimSpace(string(raw)) != "null" {
			return true
		}
	}
	return false
}

func aiJSONTypeIsFailure(value map[string]json.RawMessage) bool {
	typ := strings.ToLower(aiJSONString(value, "type"))
	return typ == "error" || strings.HasSuffix(typ, ".failed") || strings.HasSuffix(typ, ".incomplete")
}

func aiJSONStatus(value map[string]json.RawMessage) string {
	if status := aiJSONString(value, "status"); status != "" {
		return status
	}
	if nested, ok := aiJSONObject(value, "response"); ok {
		return aiJSONString(nested, "status")
	}
	return ""
}

func aiJSONText(value map[string]json.RawMessage) string {
	return aiJSONTextDepth(value, 0)
}

func aiJSONTextDepth(value map[string]json.RawMessage, depth int) string {
	if depth > 8 {
		return ""
	}
	var text strings.Builder
	if output := aiJSONString(value, "output_text"); output != "" {
		text.WriteString(output)
	}
	if output, ok := value["output"]; ok {
		var items []json.RawMessage
		if json.Unmarshal(output, &items) == nil {
			for _, item := range items {
				var object map[string]json.RawMessage
				if json.Unmarshal(item, &object) != nil {
					continue
				}
				if content, ok := object["content"]; ok {
					var parts []json.RawMessage
					if json.Unmarshal(content, &parts) == nil {
						for _, part := range parts {
							var contentObject map[string]json.RawMessage
							if json.Unmarshal(part, &contentObject) == nil {
								if value := aiJSONString(contentObject, "text"); value != "" {
									text.WriteString(value)
								}
							}
						}
					}
				}
			}
		}
	}
	if nested, ok := aiJSONObject(value, "response"); ok {
		text.WriteString(aiJSONTextDepth(nested, depth+1))
	}
	return text.String()
}

func aiJSONObject(value map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := value[key]
	if !ok {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, false
	}
	return object, true
}

func aiJSONString(value map[string]json.RawMessage, key string) string {
	raw, ok := value[key]
	if !ok {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}
