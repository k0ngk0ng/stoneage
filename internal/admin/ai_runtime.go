package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

var (
	// ErrAIModelConnectionTest is intentionally generic.  The underlying
	// Codex error may contain provider details, so the admin boundary must
	// never return it (or a credential read from the secret store).
	ErrAIModelConnectionTest = errors.New("AI model connection test failed")
	ErrAIRuntimeUnavailable  = errors.New("AI runtime is unavailable")
)

// AISupervisorControl is the lifecycle surface consumed by the admin
// adapter.  Keeping the interface here makes the HTTP layer testable while a
// real *aisupervisor.Supervisor remains the production implementation.
type AISupervisorControl interface {
	ProfileStatus(context.Context, string) (aisupervisor.Status, error)
	Start(context.Context, string) error
	Pause(context.Context, string) error
	Stop(context.Context, string) error
}

// AISupervisorRuntimeAdapter maps the supervisor's status and lifecycle
// methods to the admin package's deliberately smaller runtime interface.
// It reports the supervisor state only; profile configuration alone is never
// treated as an online session.
type AISupervisorRuntimeAdapter struct {
	supervisor AISupervisorControl
}

var _ AIProfileRuntime = (*AISupervisorRuntimeAdapter)(nil)

func NewAISupervisorRuntimeAdapter(supervisor AISupervisorControl) (*AISupervisorRuntimeAdapter, error) {
	if supervisor == nil {
		return nil, ErrAIRuntimeUnavailable
	}
	return &AISupervisorRuntimeAdapter{supervisor: supervisor}, nil
}

func (adapter *AISupervisorRuntimeAdapter) ProfileStatus(ctx context.Context, profileID string) (AIExecutionStatus, error) {
	if adapter == nil || adapter.supervisor == nil {
		return AIExecutionStatus{}, ErrAIRuntimeUnavailable
	}
	status, err := adapter.supervisor.ProfileStatus(ctx, profileID)
	if err != nil {
		return AIExecutionStatus{}, err
	}
	message := status.Message
	if message == "" {
		message = status.LastError
	}
	if status.ProfileID == "" {
		status.ProfileID = profileID
	}
	return AIExecutionStatus{
		NextDecisionAt: status.NextDecisionAt,
		Activity:       status.Activity.Label(), ActivityUntil: status.Activity.Until,
		ProfileID: status.ProfileID,
		State:     status.State,
		Message:   message,
		Backend:   aicodex.CodexBackend,
		SessionID: status.ThreadID,
		UpdatedAt: status.UpdatedAt,
	}, nil
}

func (adapter *AISupervisorRuntimeAdapter) StartProfile(ctx context.Context, profileID string) error {
	if adapter == nil || adapter.supervisor == nil {
		return ErrAIRuntimeUnavailable
	}
	return adapter.supervisor.Start(ctx, profileID)
}

func (adapter *AISupervisorRuntimeAdapter) PauseProfile(ctx context.Context, profileID string) error {
	if adapter == nil || adapter.supervisor == nil {
		return ErrAIRuntimeUnavailable
	}
	return adapter.supervisor.Pause(ctx, profileID)
}

func (adapter *AISupervisorRuntimeAdapter) StopProfile(ctx context.Context, profileID string) error {
	if adapter == nil || adapter.supervisor == nil {
		return ErrAIRuntimeUnavailable
	}
	return adapter.supervisor.Stop(ctx, profileID)
}

// AIModelConfigReader is the public model lookup needed by the connection
// tester.  *airuntime.Store implements it, while a smaller fake is sufficient
// for tests.
type AIModelConfigReader interface {
	GetModelConfig(context.Context, string) (airuntime.ModelConfig, error)
}

// AIModelSecretReader is intentionally read-only.  The admin HTTP handlers
// use AISecretStore for writes, while the connection tester obtains the key
// only from this server-side reader and never includes it in its result.
type AIModelSecretReader interface {
	ReadKey(string) (string, error)
}

// AIModelConnectionTesterOptions contains server-owned paths and handles.
// None of these values can be supplied by a profile or an HTTP request.
type AIModelConnectionTesterOptions struct {
	Models            AIModelConfigReader
	Secrets           AIModelSecretReader
	CodexBinary       string
	WorkRoot          string
	StateRoot         string
	ConnectionTimeout time.Duration
}

// AICodexModelConnectionTester performs one explicit, short Codex request
// against a configured model.  It materializes the reviewed model catalog in
// a disposable private home, then deletes that home after the request.
type AICodexModelConnectionTester struct {
	models            AIModelConfigReader
	secrets           AIModelSecretReader
	codexBinary       string
	workRoot          string
	stateRoot         string
	connectionTimeout time.Duration
}

var _ AIModelConnectionTester = (*AICodexModelConnectionTester)(nil)

func NewAICodexModelConnectionTester(options AIModelConnectionTesterOptions) (*AICodexModelConnectionTester, error) {
	if options.Models == nil {
		return nil, errors.New("AI model store is required")
	}
	if options.Secrets == nil {
		return nil, errors.New("AI secret store is required")
	}
	options.CodexBinary = strings.TrimSpace(options.CodexBinary)
	options.WorkRoot = strings.TrimSpace(options.WorkRoot)
	options.StateRoot = strings.TrimSpace(options.StateRoot)
	if options.CodexBinary == "" || !filepath.IsAbs(options.CodexBinary) {
		return nil, errors.New("Codex binary must be an absolute server path")
	}
	if options.WorkRoot == "" || !filepath.IsAbs(options.WorkRoot) || options.WorkRoot == string(filepath.Separator) {
		return nil, errors.New("connection test work root must be a dedicated absolute directory")
	}
	if err := ensureAIConnectionWorkRoot(options.WorkRoot); err != nil {
		return nil, err
	}
	// Git reports its canonical repository path on platforms where the
	// temporary/state root is reached through a symlink (for example /var on
	// macOS). Keep the runner's workspace path canonical so its isolation check
	// compares the same spelling.
	if resolved, err := filepath.EvalSymlinks(options.WorkRoot); err != nil {
		return nil, errors.New("resolve connection test work root")
	} else {
		options.WorkRoot = resolved
	}
	if options.StateRoot == "" {
		options.StateRoot = options.WorkRoot
	}
	if options.StateRoot == string(filepath.Separator) || !filepath.IsAbs(options.StateRoot) {
		return nil, errors.New("connection test state root must be a dedicated absolute directory")
	}
	if err := ensureAIConnectionWorkRoot(options.StateRoot); err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(options.StateRoot); err != nil {
		return nil, errors.New("resolve connection test state root")
	} else {
		options.StateRoot = resolved
	}
	if options.ConnectionTimeout <= 0 {
		options.ConnectionTimeout = 30 * time.Second
	}
	if options.ConnectionTimeout > 10*time.Minute {
		return nil, errors.New("connection test timeout is too long")
	}
	return &AICodexModelConnectionTester{
		models:            options.Models,
		secrets:           options.Secrets,
		codexBinary:       options.CodexBinary,
		workRoot:          options.WorkRoot,
		stateRoot:         options.StateRoot,
		connectionTimeout: options.ConnectionTimeout,
	}, nil
}

// NewAIModelConnectionTester is a concise constructor alias for callers that
// prefer the interface's name.
func NewAIModelConnectionTester(options AIModelConnectionTesterOptions) (*AICodexModelConnectionTester, error) {
	return NewAICodexModelConnectionTester(options)
}

func (tester *AICodexModelConnectionTester) TestModelConfig(ctx context.Context, modelID string) error {
	if tester == nil || tester.models == nil || tester.secrets == nil {
		return ErrAIRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, tester.connectionTimeout)
	defer cancel()
	config, err := tester.models.GetModelConfig(testCtx, modelID)
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	wireAPI := strings.TrimSpace(config.WireAPI)
	if wireAPI == "" {
		wireAPI = airuntime.ModelProviderResponses
	}
	if wireAPI != airuntime.ModelProviderResponses {
		return ErrAIModelConnectionTest
	}
	key, err := tester.secrets.ReadKey(modelID)
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	if strings.TrimSpace(key) == "" {
		return ErrAIModelConnectionTest
	}

	guard, err := runtimepath.NewGuard()
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	if err := guard.CheckAll(tester.workRoot, tester.stateRoot); err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	root, err := os.MkdirTemp(tester.workRoot, ".model-connection-test-")
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	defer os.RemoveAll(root)
	stateRoot, err := os.MkdirTemp(tester.stateRoot, ".model-connection-state-")
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	defer os.RemoveAll(stateRoot)
	home := filepath.Join(root, "codex-home")
	files, err := aimodels.Materialize(home, aimodels.RuntimeSettings{
		Model:           config.Model,
		Provider:        config.Provider,
		BaseURL:         config.BaseURL,
		ReasoningEffort: config.ReasoningEffort,
		APIKey:          key,
	})
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	if err := testCtx.Err(); err != nil {
		return err
	}
	runner, err := aicodex.New(aicodex.Config{
		Binary:          tester.codexBinary,
		CWD:             filepath.Join(root, "work"),
		StateRoot:       stateRoot,
		ConfigFile:      files.ConfigPath,
		Model:           config.Model,
		Provider:        aicodex.ProviderConfig{Name: files.ProviderID, BaseURL: config.BaseURL, WireAPI: wireAPI},
		ModelCatalog:    files.CatalogPath,
		ReasoningEffort: config.ReasoningEffort,
		WebSearch:       "disabled",
		ApprovalPolicy:  "never",
		SandboxMode:     "danger-full-access",
	})
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	result, err := runner.Run(testCtx, aicodex.RunRequest{
		ProfileID: "connection-test",
		Prompt:    "Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools.",
	})
	if err != nil {
		return genericAIModelConnectionError(testCtx, err)
	}
	if result.Turn.Status != aicodex.TurnCompleted || result.Process.Status != aicodex.ProcessExited || result.Process.ExitCode != 0 || result.LastMessage != "STONEAGE_CONNECTION_TEST_OK" {
		return ErrAIModelConnectionTest
	}
	return nil
}

const (
	defaultAIContainerConnectionTimeout = 60 * time.Second
	modelConnectionProbePrompt          = "Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools."
)

// AIContainerModelProbeBroker is the narrow broker surface needed by the
// model-only connection tester. A *aibroker.Broker implements it; tests can
// provide a fake without constructing Docker or a game service.
type AIContainerModelProbeBroker interface {
	Run(context.Context, airunner.ExecuteRequest) (aibroker.RunResult, error)
}

// AIContainerModelConnectionTesterOptions contains only server-owned handles.
// The broker owns the image, network, runtime UID and profile volume policy;
// no HTTP or model profile value can replace them.
type AIContainerModelConnectionTesterOptions struct {
	Models            AIModelConfigReader
	Secrets           AIModelSecretReader
	Broker            AIContainerModelProbeBroker
	ConnectionTimeout time.Duration
}

// AIContainerModelConnectionTester runs one pure Responses request in a
// disposable broker profile. Probe requests carry no MCP capability or
// skills, and the broker's container is independent from every game profile.
type AIContainerModelConnectionTester struct {
	models            AIModelConfigReader
	secrets           AIModelSecretReader
	broker            AIContainerModelProbeBroker
	connectionTimeout time.Duration
	probeMu           sync.Mutex
}

var _ AIModelConnectionTester = (*AIContainerModelConnectionTester)(nil)

func NewAIContainerModelConnectionTester(options AIContainerModelConnectionTesterOptions) (*AIContainerModelConnectionTester, error) {
	if options.Models == nil {
		return nil, errors.New("AI model store is required")
	}
	if options.Secrets == nil {
		return nil, errors.New("AI secret store is required")
	}
	if options.Broker == nil {
		return nil, errors.New("AI container broker is required")
	}
	if options.ConnectionTimeout <= 0 {
		options.ConnectionTimeout = defaultAIContainerConnectionTimeout
	}
	if options.ConnectionTimeout > 10*time.Minute {
		return nil, errors.New("container connection test timeout is too long")
	}
	return &AIContainerModelConnectionTester{
		models: options.Models, secrets: options.Secrets, broker: options.Broker,
		connectionTimeout: options.ConnectionTimeout,
	}, nil
}

// TestModelConfig performs a bounded model-only request. It returns a stable,
// secret-free error while retaining a private category for the admin HTTP
// layer. The shorter of the configured model timeout and the server-owned
// connection-test timeout bounds the probe.
func (tester *AIContainerModelConnectionTester) TestModelConfig(ctx context.Context, modelID string) error {
	if tester == nil || tester.models == nil || tester.secrets == nil || tester.broker == nil {
		return newAIModelConnectionTestFailure("runtime_unavailable", ErrAIRuntimeUnavailable)
	}
	// The broker serializes requests per profile, while every probe gets a
	// fresh random profile. Keep the administrator endpoint process-wide
	// single-flight so multiple tabs cannot start several bounded containers at
	// once and exhaust the host before the broker can apply its per-profile
	// lock.
	if !tester.probeMu.TryLock() {
		return newAIModelConnectionTestFailure("busy", aibroker.ErrProfileBusy)
	}
	defer tester.probeMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newAIModelConnectionTestFailure("timeout", err)
	}
	timeout := tester.connectionTimeout
	if timeout <= 0 {
		timeout = defaultAIContainerConnectionTimeout
	}
	lookupCtx, lookupCancel := context.WithTimeout(ctx, timeout)
	defer lookupCancel()
	config, err := tester.models.GetModelConfig(lookupCtx, modelID)
	if err != nil {
		if lookupErr := lookupCtx.Err(); lookupErr != nil {
			return newAIModelConnectionTestFailure("timeout", lookupErr)
		}
		return newAIModelConnectionTestFailure("runtime_unavailable", err)
	}
	if config.Backend != airuntime.ModelBackendCodex || strings.TrimSpace(config.WireAPI) != airuntime.ModelProviderResponses {
		return newAIModelConnectionTestFailure("failed", ErrAIModelConnectionTest)
	}
	key, err := tester.secrets.ReadKey(modelID)
	if err != nil || strings.TrimSpace(key) == "" {
		return newAIModelConnectionTestFailure("missing_key", err)
	}
	if config.Timeout > 0 && config.Timeout < timeout {
		timeout = config.Timeout
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	profileID, requestID, err := newAIModelProbeIDs()
	if err != nil {
		return newAIModelConnectionTestFailure("runtime_unavailable", err)
	}
	request := airunner.ExecuteRequest{
		ProfileID: profileID, RequestID: requestID, Probe: true,
		Run: airunner.RunRequest{Prompt: modelConnectionProbePrompt},
		Model: airunner.Model{Provider: config.Provider, BaseURL: config.BaseURL, Model: config.Model,
			ReasoningEffort: config.ReasoningEffort, APIKey: key},
	}
	outcome, runErr := tester.broker.Run(testCtx, request)
	// Cleanup is deliberately optional so the tester remains easy to exercise
	// with a fake broker. The concrete broker verifies the exact probe payload,
	// checks the container lifecycle, and removes the container before its
	// volume; a normal game request never reaches this cleanup path.
	if cleaner, ok := tester.broker.(interface {
		CleanupProbe(context.Context, airunner.ExecuteRequest) error
	}); ok {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = cleaner.CleanupProbe(cleanupCtx, request)
		cleanupCancel()
	}
	if err := testCtx.Err(); err != nil {
		return newAIModelConnectionTestFailure("timeout", err)
	}
	if runErr != nil {
		return classifyAIContainerProbeFailure(outcome, runErr)
	}
	if !outcome.Response.OK || outcome.Response.Result == nil || outcome.Response.Result.LastMessage != "STONEAGE_CONNECTION_TEST_OK" {
		return newAIModelConnectionTestFailure("invalid_response", ErrAIModelConnectionTest)
	}
	return nil
}

type aiModelConnectionTestFailure struct {
	code   string
	causes error
}

func (failure *aiModelConnectionTestFailure) Error() string {
	return ErrAIModelConnectionTest.Error()
}

func (failure *aiModelConnectionTestFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.causes
}

func (failure *aiModelConnectionTestFailure) ConnectionTestCode() string {
	if failure == nil || failure.code == "" {
		return "failed"
	}
	return failure.code
}

func newAIModelConnectionTestFailure(code string, cause error) error {
	switch code {
	case "missing_key", "timeout", "authentication", "rate_limit", "model_unavailable", "network", "invalid_response", "runtime_unavailable", "busy", "failed":
	default:
		code = "failed"
	}
	if cause == nil {
		cause = ErrAIModelConnectionTest
	} else {
		cause = errors.Join(ErrAIModelConnectionTest, cause)
	}
	return &aiModelConnectionTestFailure{code: code, causes: cause}
}

func classifyAIContainerProbeFailure(outcome aibroker.RunResult, runErr error) error {
	if errors.Is(runErr, context.DeadlineExceeded) {
		return newAIModelConnectionTestFailure("timeout", runErr)
	}
	if errors.Is(runErr, aibroker.ErrProfileBusy) || errors.Is(runErr, aicodex.ErrProfileBusy) {
		return newAIModelConnectionTestFailure("busy", runErr)
	}
	if errors.Is(runErr, aibroker.ErrResponse) || errors.Is(runErr, airunner.ErrOutput) || errors.Is(runErr, airunner.ErrInvalidRequest) {
		return newAIModelConnectionTestFailure("invalid_response", runErr)
	}
	if errors.Is(runErr, aibroker.ErrDocker) || errors.Is(runErr, aibroker.ErrInvalidConfig) || errors.Is(runErr, aibroker.ErrClosed) {
		return newAIModelConnectionTestFailure("runtime_unavailable", runErr)
	}
	// Codex/provider diagnostics are redacted by airunner/aicodex before they
	// reach this process. Only broad, reviewed words are used for UI category;
	// the original text is never returned or logged. Status codes are accepted
	// only when they appear with an explicit HTTP/status label, so an unrelated
	// number in a model diagnostic cannot select an error category.
	diagnostic := strings.ToLower("")
	if outcome.Response.Result != nil {
		diagnostic = strings.ToLower(outcome.Response.Result.Turn.Error + " " + outcome.Response.Result.Stderr)
	}
	switch {
	case hasAIProbeNetworkDiagnostic(diagnostic):
		return newAIModelConnectionTestFailure("network", runErr)
	case hasAIProbeHTTPStatus(diagnostic, "401"), hasAIProbeHTTPStatus(diagnostic, "403"), strings.Contains(diagnostic, "unauthorized"), strings.Contains(diagnostic, "forbidden"), strings.Contains(diagnostic, "invalid api key"), strings.Contains(diagnostic, "invalid_api_key"), strings.Contains(diagnostic, "authentication failed"), strings.Contains(diagnostic, "authentication error"):
		return newAIModelConnectionTestFailure("authentication", runErr)
	case hasAIProbeHTTPStatus(diagnostic, "429"), strings.Contains(diagnostic, "too many requests"), strings.Contains(diagnostic, "rate limit"), strings.Contains(diagnostic, "rate_limit"), strings.Contains(diagnostic, "quota exceeded"), strings.Contains(diagnostic, "insufficient quota"):
		return newAIModelConnectionTestFailure("rate_limit", runErr)
	case hasAIProbeHTTPStatus(diagnostic, "404"), strings.Contains(diagnostic, "model_not_found"), strings.Contains(diagnostic, "model not found"), strings.Contains(diagnostic, "unknown model"), strings.Contains(diagnostic, "no such model"), strings.Contains(diagnostic, "model does not exist"), strings.Contains(diagnostic, "the model") && strings.Contains(diagnostic, "does not exist"):
		return newAIModelConnectionTestFailure("model_unavailable", runErr)
	default:
		return newAIModelConnectionTestFailure("failed", runErr)
	}
}

var aiProbeHTTPStatusPattern = regexp.MustCompile(`\b(?:http(?:/\d(?:\.\d)?)?(?:\s+status)?|status(?:\s+code)?|response(?:\s+code)?|code)\s*[:=]?\s*(\d{3})\b`)

func hasAIProbeHTTPStatus(diagnostic, expected string) bool {
	for _, match := range aiProbeHTTPStatusPattern.FindAllStringSubmatch(diagnostic, -1) {
		if len(match) == 2 && match[1] == expected {
			return true
		}
	}
	return false
}

func hasAIProbeNetworkDiagnostic(diagnostic string) bool {
	for _, phrase := range []string{
		"connection refused", "connection reset", "connection timed out", "dial tcp",
		"no such host", "temporary failure in name resolution", "network is unreachable",
		"dns", "tls", "ssl", "certificate verify failed", "i/o timeout",
	} {
		if strings.Contains(diagnostic, phrase) {
			return true
		}
	}
	return false
}

func newAIModelProbeIDs() (string, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	encoded := hex.EncodeToString(random[:])
	return "model-probe-" + encoded, "probe-" + encoded, nil
}

func genericAIModelConnectionError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if err == nil {
		return ErrAIModelConnectionTest
	}
	return ErrAIModelConnectionTest
}

func ensureAIConnectionWorkRoot(path string) error {
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return err
	}
	if err := guard.Check(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("create connection test work root")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("connection test work root must be a real directory")
	}
	if err := guard.Check(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return errors.New("protect connection test work root")
	}
	return nil
}
