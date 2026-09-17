package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestAIHTTPModelConnectionTesterPostsMinimalResponsesRequest(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		if request.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q, want /v1/responses", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body["model"] != "model with spaces" || body["input"] != "hi" || body["stream"] != false {
			t.Errorf("request body = %#v", body)
		}
		for _, field := range []string{"tools", "mcp", "game", "codex", "max_output_tokens"} {
			if _, ok := body[field]; ok {
				t.Errorf("request unexpectedly contains %q", field)
			}
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"completed","output_text":"hi"}`))
	}))
	defer server.Close()

	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models:  probeModelReader{config: httpTestModelConfig(server.URL+"/v1", "model with spaces")},
		Secrets: probeSecretReader{key: "private-http-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.TestModelConfig(context.Background(), "model-http"); err != nil {
		t.Fatalf("direct Responses request failed: %v", err)
	}
	if gotAuth != "Bearer private-http-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestAIHTTPModelConnectionTesterAcceptsCompletedSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
			"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"text\":\"hi\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models:  probeModelReader{config: httpTestModelConfig(server.URL+"/v1/", "sse-model")},
		Secrets: probeSecretReader{key: "sse-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.TestModelConfig(context.Background(), "model-sse"); err != nil {
		t.Fatalf("SSE response failed: %v", err)
	}
}

func TestAIHTTPModelConnectionTesterAcceptsNestedOutputText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"nested response"}]}]}`))
	}))
	defer server.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models:  probeModelReader{config: httpTestModelConfig(server.URL+"/v1/", "nested-model")},
		Secrets: probeSecretReader{key: "nested-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.TestModelConfig(context.Background(), "model-nested"); err != nil {
		t.Fatalf("nested Responses output failed: %v", err)
	}
}

func TestAIHTTPModelConnectionTesterDoesNotDuplicateResponsesPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			t.Errorf("request path = %q, want /v1/responses", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"completed","output_text":"ok"}`))
	}))
	defer server.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models:  probeModelReader{config: httpTestModelConfig(server.URL+"/v1/responses/", "path-model")},
		Secrets: probeSecretReader{key: "path-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.TestModelConfig(context.Background(), "model-path"); err != nil {
		t.Fatalf("Responses path request failed: %v", err)
	}
}

func TestAIHTTPModelConnectionTesterRejectsIncompleteResponses(t *testing.T) {
	for name, body := range map[string]string{
		"failed":     `{"status":"failed","error":{"message":"provider failure"}}`,
		"incomplete": `{"status":"incomplete","output_text":"partial"}`,
		"error":      `{"error":{"message":"provider failure"}}`,
		"no text":    `{"status":"completed","output":[]}`,
		"malformed":  `{"status":"completed"`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(body))
			}))
			defer server.Close()
			tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
				Models:  probeModelReader{config: httpTestModelConfig(server.URL, "test-model")},
				Secrets: probeSecretReader{key: "key"},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = tester.TestModelConfig(context.Background(), "model-invalid")
			var coded interface{ ConnectionTestCode() string }
			if !errors.As(err, &coded) || coded.ConnectionTestCode() != "invalid_response" {
				t.Fatalf("error = %v, code = %v", err, coded)
			}
		})
	}
}

func TestAIHTTPModelConnectionTesterRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"completed","output_text":"response larger than the configured limit"}`))
	}))
	defer server.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models: probeModelReader{config: httpTestModelConfig(server.URL, "large-model")}, Secrets: probeSecretReader{key: "key"}, MaxResponseBytes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = tester.TestModelConfig(context.Background(), "model-large")
	var coded interface{ ConnectionTestCode() string }
	if !errors.As(err, &coded) || coded.ConnectionTestCode() != "invalid_response" {
		t.Fatalf("oversized response error = %v, code = %v", err, coded)
	}
}

func TestAIHTTPModelConnectionTesterClassifiesStatusAndRedirects(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized:    "authentication",
		http.StatusForbidden:       "authentication",
		http.StatusTooManyRequests: "rate_limit",
		http.StatusNotFound:        "model_unavailable",
		http.StatusGatewayTimeout:  "timeout",
		http.StatusBadGateway:      "failed",
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(status)
			}))
			defer server.Close()
			tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
				Models:  probeModelReader{config: httpTestModelConfig(server.URL, "test-model")},
				Secrets: probeSecretReader{key: "key"},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = tester.TestModelConfig(context.Background(), "model-status")
			var coded interface{ ConnectionTestCode() string }
			if !errors.As(err, &coded) || coded.ConnectionTestCode() != want {
				t.Fatalf("error = %v, code = %v, want %s", err, coded, want)
			}
		})
	}

	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		redirected.Store(true)
		if request.Header.Get("Authorization") != "" {
			t.Errorf("redirect target received Authorization")
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models:  probeModelReader{config: httpTestModelConfig(redirector.URL, "redirect-model")},
		Secrets: probeSecretReader{key: "secret-must-not-leak"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = tester.TestModelConfig(context.Background(), "model-redirect")
	var coded interface{ ConnectionTestCode() string }
	if !errors.As(err, &coded) || coded.ConnectionTestCode() != "failed" || redirected.Load() {
		t.Fatalf("redirect error = %v, code = %v, target contacted = %v", err, coded, redirected.Load())
	}
	if strings.Contains(err.Error(), "secret-must-not-leak") {
		t.Fatal("redirect error leaked API key")
	}
}

func TestAIHTTPModelConnectionTesterUsesTimeoutAndSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(started)
		_, _ = io.Copy(io.Discard, request.Body)
		<-release
		_, _ = response.Write([]byte(`{"status":"completed","output_text":"hi"}`))
	}))
	defer server.Close()
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models: probeModelReader{config: httpTestModelConfig(server.URL, "slow-model")}, Secrets: probeSecretReader{key: "key"}, ConnectionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- tester.TestModelConfig(context.Background(), "model-slow") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach server")
	}
	secondErr := tester.TestModelConfig(context.Background(), "model-slow")
	var coded interface{ ConnectionTestCode() string }
	if !errors.As(secondErr, &coded) || coded.ConnectionTestCode() != "busy" {
		t.Fatalf("second request error = %v, code = %v", secondErr, coded)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first request failed: %v", err)
	}
}

func TestAIHTTPModelConnectionTesterUsesTheShorterTimeout(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		modelTimeout    time.Duration
		serverTimeout   time.Duration
		wantMaximumWait time.Duration
	}{
		{name: "model timeout", modelTimeout: 20 * time.Millisecond, serverTimeout: 500 * time.Millisecond, wantMaximumWait: 250 * time.Millisecond},
		{name: "server timeout", modelTimeout: 500 * time.Millisecond, serverTimeout: 20 * time.Millisecond, wantMaximumWait: 250 * time.Millisecond},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				close(started)
				_, _ = io.Copy(io.Discard, request.Body)
				select {
				case <-release:
				case <-time.After(2 * time.Second):
				}
				_, _ = response.Write([]byte(`{"status":"completed","output_text":"late"}`))
			}))
			defer server.Close()
			defer close(release)
			config := httpTestModelConfig(server.URL, "timeout-model")
			config.Timeout = testCase.modelTimeout
			tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
				Models: probeModelReader{config: config}, Secrets: probeSecretReader{key: "key"}, ConnectionTimeout: testCase.serverTimeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			startedAt := time.Now()
			result := make(chan error, 1)
			go func() { result <- tester.TestModelConfig(context.Background(), "model-timeout") }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not reach server")
			}
			err = <-result
			if elapsed := time.Since(startedAt); elapsed > testCase.wantMaximumWait {
				t.Fatalf("request took %s, want under %s", elapsed, testCase.wantMaximumWait)
			}
			var coded interface{ ConnectionTestCode() string }
			if !errors.As(err, &coded) || coded.ConnectionTestCode() != "timeout" {
				t.Fatalf("timeout error = %v, code = %v", err, coded)
			}
		})
	}
}

func TestAIHTTPModelConnectionTesterHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(started)
		_, _ = io.Copy(io.Discard, request.Body)
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		_, _ = response.Write([]byte(`{"status":"completed","output_text":"late"}`))
	}))
	defer server.Close()
	defer close(release)
	tester, err := NewAIHTTPModelConnectionTester(AIHTTPModelConnectionTesterOptions{
		Models: probeModelReader{config: httpTestModelConfig(server.URL, "cancel-model")}, Secrets: probeSecretReader{key: "key"}, ConnectionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- tester.TestModelConfig(ctx, "model-cancel") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-result:
		var coded interface{ ConnectionTestCode() string }
		if !errors.As(err, &coded) || coded.ConnectionTestCode() != "failed" {
			t.Fatalf("canceled request error = %v, code = %v", err, coded)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request did not return")
	}
}

func httpTestModelConfig(baseURL, model string) airuntime.ModelConfig {
	return airuntime.ModelConfig{ID: "model", Backend: airuntime.ModelBackendCodex, Provider: "test", BaseURL: baseURL, Model: model, WireAPI: airuntime.ModelProviderResponses, Timeout: time.Second}
}
