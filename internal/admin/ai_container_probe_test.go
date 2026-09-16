package admin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type diagnosticProbeBroker struct {
	diagnostic string
	calls      atomic.Int32
}

func (broker *diagnosticProbeBroker) Run(_ context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
	broker.calls.Add(1)
	return aibroker.RunResult{Response: airunner.Response{
		ProfileID: request.ProfileID,
		RequestID: request.RequestID,
		Result:    &airunner.Result{Turn: airunner.Turn{Error: broker.diagnostic}},
	}}, errors.New("provider request failed")
}

func TestAIContainerProbeClassifiesExplicitProviderDiagnostics(t *testing.T) {
	cases := []struct {
		name       string
		diagnostic string
		want       string
	}{
		{name: "authentication", diagnostic: "HTTP status: 401 Unauthorized", want: "authentication"},
		{name: "network", diagnostic: "dial tcp 10.0.0.4:443: connect: connection refused", want: "network"},
		{name: "model", diagnostic: "response code=404: model_not_found", want: "model_unavailable"},
		{name: "rate", diagnostic: "HTTP 429 Too Many Requests", want: "rate_limit"},
		{name: "unrelated number", diagnostic: "model parameter index 401 was ignored", want: "failed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &diagnosticProbeBroker{diagnostic: testCase.diagnostic}
			tester, err := NewAIContainerModelConnectionTester(AIContainerModelConnectionTesterOptions{
				Models:  probeModelReader{config: airuntime.ModelConfig{ID: "model-diagnostic", Backend: airuntime.ModelBackendCodex, Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", WireAPI: airuntime.ModelProviderResponses}},
				Secrets: probeSecretReader{key: "diagnostic-key"}, Broker: broker, ConnectionTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = tester.TestModelConfig(context.Background(), "model-diagnostic")
			var coded interface{ ConnectionTestCode() string }
			if !errors.As(err, &coded) || coded.ConnectionTestCode() != testCase.want {
				t.Fatalf("error=%v code=%v, want %s", err, coded, testCase.want)
			}
		})
	}
}

type blockingProbeBroker struct {
	started atomic.Bool
	calls   atomic.Int32
	release chan struct{}
}

func (broker *blockingProbeBroker) Run(ctx context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
	broker.calls.Add(1)
	broker.started.Store(true)
	select {
	case <-broker.release:
		return aibroker.RunResult{Response: airunner.Response{OK: true, ProfileID: request.ProfileID, RequestID: request.RequestID, Result: &airunner.Result{ThreadID: "probe-thread", LastMessage: "STONEAGE_CONNECTION_TEST_OK"}}}, nil
	case <-ctx.Done():
		return aibroker.RunResult{}, ctx.Err()
	}
}

func TestAIContainerModelConnectionTesterIsSingleFlight(t *testing.T) {
	broker := &blockingProbeBroker{release: make(chan struct{})}
	tester, err := NewAIContainerModelConnectionTester(AIContainerModelConnectionTesterOptions{
		Models:  probeModelReader{config: airuntime.ModelConfig{ID: "model-single-flight", Backend: airuntime.ModelBackendCodex, Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", WireAPI: airuntime.ModelProviderResponses}},
		Secrets: probeSecretReader{key: "single-flight-key"}, Broker: broker, ConnectionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- tester.TestModelConfig(context.Background(), "model-single-flight") }()
	deadline := time.Now().Add(time.Second)
	for !broker.started.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !broker.started.Load() {
		t.Fatal("first probe did not start")
	}
	secondErr := tester.TestModelConfig(context.Background(), "model-single-flight")
	var coded interface{ ConnectionTestCode() string }
	if !errors.As(secondErr, &coded) || coded.ConnectionTestCode() != "busy" {
		t.Fatalf("second probe error=%v code=%v, want busy", secondErr, coded)
	}
	close(broker.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first probe error=%v", err)
	}
	if broker.calls.Load() != 1 {
		t.Fatalf("probe calls=%d, want one", broker.calls.Load())
	}
}

func TestAIContainerModelConnectionTesterUsesTighterTimeoutAndAllowsLongModelValue(t *testing.T) {
	broker := &probeBroker{block: true}
	tester, err := NewAIContainerModelConnectionTester(AIContainerModelConnectionTesterOptions{
		Models:  probeModelReader{config: airuntime.ModelConfig{ID: "model-long-timeout", Backend: airuntime.ModelBackendCodex, Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", WireAPI: airuntime.ModelProviderResponses, Timeout: 20 * time.Minute}},
		Secrets: probeSecretReader{key: "timeout-key"}, Broker: broker, ConnectionTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = tester.TestModelConfig(context.Background(), "model-long-timeout")
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("probe ignored server timeout: elapsed=%s err=%v", elapsed, err)
	}
	var coded interface{ ConnectionTestCode() string }
	if !errors.As(err, &coded) || coded.ConnectionTestCode() != "timeout" {
		t.Fatalf("error=%v code=%v, want timeout", err, coded)
	}
}
