package admin

// The admin package owns the HTTP and authorization boundary for AI
// configuration.  The actual persistence and runtime live in internal/airuntime
// (or a test double), so the console never needs to know how an agent is
// scheduled or how a game connection is executed.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

const (
	defaultAITimeout     = 120 * time.Second
	defaultAIMaxTokens   = 4096
	defaultAITokenBudget = int64(100000)
	maxAINameLength      = 128
	maxAIModelLength     = 128
	maxAIURLLength       = 2048
	maxAISecretLength    = 4096
	maxAIPromptLength    = 16000
	maxAISkills          = 64
	maxAIEvents          = 200
	maxAITimeout         = 24 * time.Hour
	maxAIMaxOutputTokens = 1_000_000
	maxAITokenBudget     = int64(2_000_000_000)
	maxAIExternalSpend   = int64(2_000_000_000)
)

var aiIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

var (
	errAIProfileDeleteActive   = errors.New("AI profile is still active")
	errAIProfileDeleteRecovery = errors.New("AI profile has an unresolved execution")
	errAIProfileDeleteStatus   = errors.New("AI profile runtime status is unavailable")
)

// AIStore is deliberately made from the small set of operations needed by
// the console.  *airuntime.Store implements it.  Keeping this as an
// interface also lets the admin tests use an in-memory fake and prevents the
// handler from reaching into the runtime database.
type AIStore interface {
	CreateModelConfigAs(context.Context, airuntime.ModelConfig, string) (airuntime.ModelConfig, error)
	GetModelConfig(context.Context, string) (airuntime.ModelConfig, error)
	ListModelConfigs(context.Context) ([]airuntime.ModelConfig, error)
	UpdateModelConfigCAS(context.Context, string, int64, airuntime.ModelConfigPatch) (airuntime.ModelConfig, error)
	DeleteModelConfigCAS(context.Context, string, int64, string) error

	CreateProfileAs(context.Context, airuntime.Profile, string) (airuntime.Profile, error)
	GetProfile(context.Context, string) (airuntime.Profile, error)
	ListProfiles(context.Context) ([]airuntime.Profile, error)
	UpdateProfileCAS(context.Context, string, int64, airuntime.ProfilePatch) (airuntime.Profile, error)
	DeleteProfileCAS(context.Context, string, int64, string) error
	ListEvents(context.Context, string, int) ([]airuntime.AuditEvent, error)
}

// AISecretStore is the only secret operation surface exposed to HTTP code.
// Implementations must map IDs to a fixed private directory and must never
// return key material from HasKey.
type AISecretStore interface {
	WriteKey(string, string) error
	RemoveKey(string) error
	HasKey(string) (bool, error)
}

// AIDefaultModelStore makes the selected model durable when the runtime
// provides that capability.  Older runtimes may omit it; in that case the
// selection remains process-local and the UI says that no durable default is
// available rather than pretending it survived a restart.
type AIDefaultModelStore interface {
	GetDefaultModelConfigID(context.Context) (string, error)
	SetDefaultModelConfigID(context.Context, string, string) error
}

// AIProfileRuntime is optional.  The admin console cannot claim an agent is
// online from profile configuration alone.  A runtime adapter must provide
// this status and the fixed lifecycle actions before the start/stop controls
// become available.
type AIProfileRuntime interface {
	ProfileStatus(context.Context, string) (AIExecutionStatus, error)
	StartProfile(context.Context, string) error
	PauseProfile(context.Context, string) error
	StopProfile(context.Context, string) error
}

// AIModelConnectionTester is intentionally separate from AIStore.  A model
// test is a network operation and must only run after an explicit POST from
// an administrator; a missing tester is reported as unavailable.
type AIModelConnectionTester interface {
	TestModelConfig(context.Context, string) error
}

// AIPlayerProvisioner is the server-owned identity boundary for creating a
// new AI player.  The HTTP layer supplies only the non-secret profile and
// character preferences; the implementation generates the game account
// credentials, creates the character and stores those credentials privately.
// It must never return password material.
type AIPlayerProvisioner interface {
	Provision(context.Context, AIPlayerProvisionRequest) (airuntime.Profile, error)
}

// AIPlayerProvisionRequest contains only administrator-selected AI player
// preferences. Actor and ActorID are assigned by the authenticated HTTP
// handler and are never decoded from the browser request body.
type AIPlayerProvisionRequest struct {
	InitialState       *aiinitial.Request
	ProfileID          string
	CharacterName      string
	CharacterSlot      int
	ModelConfigID      string
	Personality        airuntime.Personality
	Goal               airuntime.Goal
	Skills             []airuntime.SkillVersion
	UnlimitedFunds     *bool
	DailyTokenBudget   int64
	ExternalSpendLimit int64
	Status             string
	Actor              string
	ActorID            *int64
}

type AIExecutionStatus struct {
	NextDecisionAt time.Time `json:"next_decision_at,omitempty"`
	Activity       string    `json:"activity,omitempty"`
	ActivityUntil  time.Time `json:"activity_until,omitempty"`
	ProfileID      string    `json:"profile_id"`
	State          string    `json:"state"`
	Message        string    `json:"message,omitempty"`
	Backend        string    `json:"backend,omitempty"`
	CLIVersion     string    `json:"cli_version,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type aiModelView struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	Backend          string    `json:"backend"`
	BaseURL          string    `json:"base_url"`
	Model            string    `json:"model"`
	WireAPI          string    `json:"wire_api"`
	ReasoningEffort  string    `json:"reasoning_effort"`
	TimeoutSeconds   int64     `json:"timeout_seconds"`
	MaxOutputTokens  int       `json:"max_output_tokens"`
	DailyTokenBudget int64     `json:"daily_token_budget"`
	HasKey           bool      `json:"has_key"`
	Default          bool      `json:"default"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// aiModelPresetView describes a safe starting point for a model form. Presets
// are convenience data only: every endpoint, model, provider and wire API is
// still validated by the server and remains editable by an administrator.
type aiModelPresetView struct {
	ID               string
	Label            string
	Provider         string
	BaseURL          string
	Model            string
	DisplayName      string
	Description      string
	DefaultReasoning string
	ReasoningLevels  []string
	WireAPI          string
}

// aiSkillView is the safe management representation of one native Codex
// skill. The catalog digest stays server-owned; the browser submits only the
// selected names and the provision boundary resolves the canonical metadata.
type aiSkillView struct {
	Name    string
	Version string
	Usage   string
}

const aiCoreSkillName = "stoneage-play"

var aiSkillUsage = map[string]string{
	"stoneage-play":     "核心游戏操作：观察世界、查询已验证知识并执行合法的游戏动作。",
	"stoneage-leveling": "练级任务：按服务端确认的目标、区域和恢复策略执行练级。",
	"stoneage-quest":    "任务流程：完成已验证的任务或任务链，并确认每个服务端结果。",
	"stoneage-social":   "社交互动：在可见性和同意校验下进行聊天、组队、交易和决斗。",
}

// reviewedAISkillCatalog derives the available names and versions from the
// fixed aimcp catalog. Usage text is product copy; identity and version are
// always taken from aimcp so the form cannot drift from the installer.
func reviewedAISkillCatalog() []aiSkillView {
	specs := aimcp.SkillCatalog()
	result := make([]aiSkillView, 0, len(specs))
	for _, spec := range specs {
		usage := aiSkillUsage[spec.Name]
		if usage == "" {
			usage = "StoneAge 原生技能：由服务端校验后安装到独立 agent 工作区。"
		}
		result = append(result, aiSkillView{Name: spec.Name, Version: spec.Version, Usage: usage})
	}
	return result
}

func canonicalAISkill(spec aimcp.SkillSpec) airuntime.SkillVersion {
	return airuntime.SkillVersion{
		Name:    spec.Name,
		Version: spec.Version,
		Kind:    airuntime.SkillKindNative,
		Path:    spec.RelativePath,
		Digest:  "sha256:" + spec.SHA256,
	}
}

// normalizeAIProvisionSkills accepts only names from the server-owned
// catalog. It adds no browser-provided version, path, digest, or parameters;
// the core play skill is required for every newly provisioned player.
func normalizeAIProvisionSkills(names []string) ([]airuntime.SkillVersion, error) {
	if len(names) == 0 {
		return nil, errors.New("至少选择 stoneage-play 原生技能")
	}
	if len(names) > maxAISkills {
		return nil, errors.New("skill 数量超出范围")
	}
	specs := aimcp.SkillCatalog()
	byName := make(map[string]aimcp.SkillSpec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	selected := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errors.New("skill 名称不能为空")
		}
		if _, duplicate := selected[name]; duplicate {
			return nil, fmt.Errorf("skill %q 重复选择", name)
		}
		_, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("skill %q 不在服务端原生技能目录中", name)
		}
		selected[name] = struct{}{}
	}
	if _, ok := selected[aiCoreSkillName]; !ok {
		return nil, errors.New("新建 AI 玩家必须包含 stoneage-play 原生技能")
	}

	// Catalog order is stable and keeps the persisted profile deterministic,
	// regardless of checkbox or request ordering.
	result := make([]airuntime.SkillVersion, 0, len(selected))
	for _, spec := range specs {
		if _, ok := selected[spec.Name]; ok {
			result = append(result, canonicalAISkill(spec))
		}
	}
	return result, nil
}

// aiModelCatalogView is the model form's preset catalog. DeepSeek's model
// metadata comes from the reviewed embedded catalog, while OpenAI and custom
// entries are generic OpenAI-compatible connection presets.
type aiModelCatalogView struct {
	Backend          string
	Provider         string
	BaseURL          string
	Slug             string
	DisplayName      string
	Description      string
	DefaultReasoning string
	ReasoningLevels  []string
	Presets          []aiModelPresetView
}

var genericAIReasoningLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

var aiProviderPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func reviewedAIModelCatalog() (aiModelCatalogView, error) {
	descriptor, err := aimodels.Flash()
	if err != nil {
		return aiModelCatalogView{}, err
	}
	deepSeekLevels := make([]string, 0, len(descriptor.ReasoningLevels))
	for _, level := range descriptor.ReasoningLevels {
		if strings.TrimSpace(level.Effort) != "" {
			deepSeekLevels = append(deepSeekLevels, level.Effort)
		}
	}
	deepSeek := aiModelPresetView{
		ID: "deepseek", Label: "DeepSeek", Provider: aimodels.DeepSeekProvider,
		BaseURL: strings.TrimRight(aimodels.DeepSeekBaseURL, "/"), Model: descriptor.Slug,
		DisplayName: descriptor.DisplayName, Description: descriptor.Description,
		DefaultReasoning: descriptor.DefaultReasoning, ReasoningLevels: deepSeekLevels,
		WireAPI: airuntime.ModelProviderResponses,
	}
	openAI := aiModelPresetView{
		ID: "openai", Label: "OpenAI", Provider: "openai", BaseURL: aimodels.OpenAIBaseURL,
		Model: "gpt-5", DisplayName: "OpenAI GPT-5", Description: "OpenAI 官方 OpenAI-compatible API",
		ReasoningLevels: append([]string(nil), genericAIReasoningLevels...), WireAPI: airuntime.ModelProviderResponses,
	}
	custom := aiModelPresetView{
		ID: "custom", Label: "自定义", Provider: "custom", DisplayName: "自定义 OpenAI-compatible",
		Description:     "填写任意兼容 OpenAI Responses 的 HTTP(S) API 地址和模型标识",
		ReasoningLevels: append([]string(nil), genericAIReasoningLevels...), WireAPI: airuntime.ModelProviderResponses,
	}
	return aiModelCatalogView{
		Backend:          airuntime.ModelBackendCodex,
		Provider:         deepSeek.Provider,
		BaseURL:          deepSeek.BaseURL,
		Slug:             descriptor.Slug,
		DisplayName:      descriptor.DisplayName,
		Description:      descriptor.Description,
		DefaultReasoning: descriptor.DefaultReasoning,
		ReasoningLevels:  append([]string(nil), genericAIReasoningLevels...),
		Presets:          []aiModelPresetView{deepSeek, openAI, custom},
	}, nil
}

// NewReviewedAIModelConfig returns the server's one currently supported
// model configuration.  The provider, endpoint, model slug, and default
// reasoning level all come from the reviewed aimodels catalog; operational
// limits remain the same defaults used by the admin form.
func NewReviewedAIModelConfig() (airuntime.ModelConfig, error) {
	descriptor, err := aimodels.Flash()
	if err != nil {
		return airuntime.ModelConfig{}, err
	}
	return airuntime.ModelConfig{
		Name:             descriptor.DisplayName,
		Backend:          airuntime.ModelBackendCodex,
		Provider:         aimodels.DeepSeekProvider,
		BaseURL:          strings.TrimRight(aimodels.DeepSeekBaseURL, "/"),
		Model:            descriptor.Slug,
		WireAPI:          airuntime.ModelProviderResponses,
		ReasoningEffort:  descriptor.DefaultReasoning,
		Timeout:          defaultAITimeout,
		MaxOutputTokens:  defaultAIMaxTokens,
		DailyTokenBudget: defaultAITokenBudget,
	}, nil
}

type aiProfileView struct {
	InitialState            *aiprovision.InitialState   `json:"initial_state,omitempty"`
	InitialStateUnavailable bool                        `json:"initial_state_unavailable,omitempty"`
	ID                      string                      `json:"id"`
	Account                 airuntime.AccountIdentity   `json:"account"`
	Character               airuntime.CharacterIdentity `json:"character"`
	ModelConfigID           string                      `json:"model_config_id,omitempty"`
	Personality             airuntime.Personality       `json:"personality"`
	Goal                    airuntime.Goal              `json:"goal"`
	Skills                  []airuntime.SkillVersion    `json:"skills"`
	UnlimitedFunds          bool                        `json:"unlimited_funds"`
	DailyTokenBudget        int64                       `json:"daily_token_budget"`
	ExternalSpendLimit      int64                       `json:"external_spend_limit"`
	ProfileStatus           string                      `json:"profile_status"`
	Runtime                 AIExecutionStatus           `json:"runtime"`
	Version                 int64                       `json:"version"`
	CreatedAt               time.Time                   `json:"created_at"`
	UpdatedAt               time.Time                   `json:"updated_at"`
}

type aiEventView struct {
	ID        int64           `json:"id"`
	ProfileID string          `json:"profile_id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type aiModelRequest struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name"`
	Provider         string `json:"provider,omitempty"`
	Backend          string `json:"backend,omitempty"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	WireAPI          string `json:"wire_api,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	TimeoutSeconds   int64  `json:"timeout_seconds"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	DailyTokenBudget int64  `json:"daily_token_budget"`
	APIKey           string `json:"api_key,omitempty"`
	ClearKey         bool   `json:"clear_key,omitempty"`
	Default          *bool  `json:"default,omitempty"`
	ExpectedVersion  int64  `json:"expected_version,omitempty"`
}

type aiProfileCreateRequest struct {
	ID                 string                      `json:"id,omitempty"`
	Account            airuntime.AccountIdentity   `json:"account"`
	Character          airuntime.CharacterIdentity `json:"character"`
	ModelConfigID      string                      `json:"model_config_id"`
	Personality        airuntime.Personality       `json:"personality"`
	Goal               airuntime.Goal              `json:"goal"`
	Skills             []airuntime.SkillVersion    `json:"skills"`
	UnlimitedFunds     *bool                       `json:"unlimited_funds,omitempty"`
	DailyTokenBudget   int64                       `json:"daily_token_budget"`
	ExternalSpendLimit int64                       `json:"external_spend_limit"`
	Status             string                      `json:"status,omitempty"`
}

// aiPlayerProvisionRequest is intentionally separate from the legacy profile
// CRUD request. A provision request cannot select an existing account or
// character identity; the server creates a dedicated game identity.
type aiPlayerProvisionRequest struct {
	InitialState       *aiinitial.Request    `json:"initial_state,omitempty"`
	ID                 string                `json:"id,omitempty"`
	CharacterName      string                `json:"character_name"`
	CharacterSlot      int                   `json:"character_slot,omitempty"`
	ModelConfigID      string                `json:"model_config_id,omitempty"`
	Personality        airuntime.Personality `json:"personality"`
	Goal               airuntime.Goal        `json:"goal"`
	SkillNames         []string              `json:"skill_names"`
	UnlimitedFunds     *bool                 `json:"unlimited_funds,omitempty"`
	DailyTokenBudget   int64                 `json:"daily_token_budget"`
	ExternalSpendLimit int64                 `json:"external_spend_limit"`
	Status             string                `json:"status,omitempty"`
}

type aiProfilePatchRequest struct {
	Account            *airuntime.AccountIdentity   `json:"account,omitempty"`
	Character          *airuntime.CharacterIdentity `json:"character,omitempty"`
	ModelConfigID      *string                      `json:"model_config_id,omitempty"`
	Personality        *airuntime.Personality       `json:"personality,omitempty"`
	Goal               *airuntime.Goal              `json:"goal,omitempty"`
	Skills             *[]airuntime.SkillVersion    `json:"skills,omitempty"`
	UnlimitedFunds     *bool                        `json:"unlimited_funds,omitempty"`
	DailyTokenBudget   *int64                       `json:"daily_token_budget,omitempty"`
	ExternalSpendLimit *int64                       `json:"external_spend_limit,omitempty"`
	Status             *string                      `json:"status,omitempty"`
	ExpectedVersion    int64                        `json:"expected_version"`
}

// aiDefaultMu protects the compatibility fallback described on
// AIDefaultModelStore.  A runtime implementation with durable methods takes
// precedence over this value.
var aiDefaultMu sync.RWMutex

func (server *Server) aiDefaultModelID(ctx context.Context) string {
	if defaults, ok := server.aiStore.(AIDefaultModelStore); ok {
		if id, err := defaults.GetDefaultModelConfigID(ctx); err == nil {
			return id
		}
	}
	aiDefaultMu.RLock()
	defer aiDefaultMu.RUnlock()
	return server.aiDefaultID
}

func (server *Server) setAIDefaultModelID(ctx context.Context, id, actor string) error {
	if defaults, ok := server.aiStore.(AIDefaultModelStore); ok {
		if err := defaults.SetDefaultModelConfigID(ctx, id, actor); err != nil {
			return err
		}
	}
	aiDefaultMu.Lock()
	server.aiDefaultID = id
	aiDefaultMu.Unlock()
	return nil
}

func (server *Server) aiConfigured() bool { return server.aiStore != nil }

func aiModelViewFrom(config airuntime.ModelConfig, defaultID string, keyOverride *bool) aiModelView {
	hasKey := config.HasKey
	if keyOverride != nil {
		hasKey = *keyOverride
	}
	backend := strings.TrimSpace(config.Backend)
	if backend == "" {
		backend = airuntime.ModelBackendCodex
	}
	wireAPI := strings.TrimSpace(config.WireAPI)
	if wireAPI == "" {
		wireAPI = airuntime.ModelProviderResponses
	}
	seconds := int64(config.Timeout / time.Second)
	if seconds <= 0 && config.Timeout > 0 {
		seconds = 1
	}
	return aiModelView{ID: config.ID, Name: config.Name, Provider: config.Provider,
		Backend: backend, BaseURL: config.BaseURL, Model: config.Model,
		WireAPI: wireAPI, ReasoningEffort: config.ReasoningEffort,
		TimeoutSeconds: seconds, MaxOutputTokens: config.MaxOutputTokens,
		DailyTokenBudget: config.DailyTokenBudget, HasKey: hasKey,
		Default: config.ID != "" && config.ID == defaultID, Version: config.Version,
		CreatedAt: config.CreatedAt, UpdatedAt: config.UpdatedAt}
}

func (server *Server) aiProfileView(ctx context.Context, profile airuntime.Profile) aiProfileView {
	runtime := AIExecutionStatus{ProfileID: profile.ID, State: "stopped", Message: "AI runtime 未接入，当前仅已配置"}
	if executor, ok := server.aiRuntime.(AIProfileRuntime); ok {
		if status, err := executor.ProfileStatus(ctx, profile.ID); err == nil {
			runtime = status
			if runtime.ProfileID == "" {
				runtime.ProfileID = profile.ID
			}
		} else {
			runtime.State = aisupervisor.StateError
			runtime.Message = "无法读取 AI runtime 状态"
		}
	}
	var initial *aiprovision.InitialState
	initialUnavailable := false
	if reader, ok := server.aiProvisioner.(interface {
		InitialState(context.Context, string) (*aiprovision.InitialState, error)
	}); ok {
		var err error
		initial, err = reader.InitialState(ctx, profile.ID)
		initialUnavailable = err != nil
	}
	return aiProfileView{InitialState: initial, InitialStateUnavailable: initialUnavailable, ID: profile.ID, Account: profile.Account, Character: profile.Character,
		ModelConfigID: profile.ModelConfigID, Personality: profile.Personality, Goal: profile.Goal,
		Skills: profile.Skills, UnlimitedFunds: profile.UnlimitedFunds,
		DailyTokenBudget: profile.DailyTokenBudget, ExternalSpendLimit: profile.ExternalSpendLimit,
		ProfileStatus: profile.Status, Runtime: runtime, Version: profile.Version,
		CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt}
}

func (server *Server) aiModelViews(ctx context.Context) ([]aiModelView, string, error) {
	if server.aiStore == nil {
		return []aiModelView{}, "", errors.New("AI runtime 未配置")
	}
	configs, err := server.aiStore.ListModelConfigs(ctx)
	if err != nil {
		return nil, "", err
	}
	defaultID := server.aiDefaultModelID(ctx)
	views := make([]aiModelView, 0, len(configs))
	for _, config := range configs {
		var override *bool
		if server.aiSecrets != nil {
			if hasKey, keyErr := server.aiSecrets.HasKey(config.ID); keyErr == nil {
				override = &hasKey
			}
		}
		views = append(views, aiModelViewFrom(config, defaultID, override))
	}
	return views, defaultID, nil
}

func (server *Server) aiModelsPage(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodGet {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	data.Title = "AI 模型"
	data.AIConnectionTestAvailable = server.aiConnectionTester != nil
	if catalog, err := reviewedAIModelCatalog(); err != nil {
		data.AIError = "内置 AI 模型目录读取失败"
	} else {
		data.AIModelCatalog = catalog
	}
	if server.aiStore == nil {
		if data.AIError == "" {
			data.AIError = "AI runtime 尚未配置；模型配置和 AI 玩家目前不可用。"
		}
	} else if models, defaultID, err := server.aiModelViews(request.Context()); err != nil {
		data.AIError = "模型配置读取失败"
	} else {
		data.AIModels = models
		data.AIDefaultModelID = defaultID
	}
	server.render(response, "ai_models", data)
}

func (server *Server) aiProfilesPage(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodGet {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	data.Title = "AI 玩家"
	data.AISkills = reviewedAISkillCatalog()
	if server.aiStore == nil {
		data.AIError = "AI runtime 尚未配置；AI 玩家不会被伪装成在线。"
		server.render(response, "ai_profiles", data)
		return
	}
	models, defaultID, modelErr := server.aiModelViews(request.Context())
	if modelErr != nil {
		data.AIError = "模型配置读取失败"
	} else {
		data.AIModels = models
		data.AIDefaultModelID = defaultID
	}
	profiles, err := server.aiStore.ListProfiles(request.Context())
	if err != nil {
		data.AIError = "AI 玩家读取失败"
	} else {
		data.AIProfiles = make([]aiProfileView, 0, len(profiles))
		for _, profile := range profiles {
			data.AIProfiles = append(data.AIProfiles, server.aiProfileView(request.Context(), profile))
		}
	}
	server.render(response, "ai_profiles", data)
}

func (server *Server) aiAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	path := strings.TrimSuffix(request.URL.Path, "/")
	if server.aiStore == nil {
		playerJSONError(response, http.StatusServiceUnavailable, "AI runtime 尚未配置")
		return
	}
	if path == "/api/ai/models" || path == "/api/ai/profiles" {
		if path == "/api/ai/models" {
			server.aiModelsAPI(response, request, data)
		} else {
			server.aiProfilesAPI(response, request, data)
		}
		return
	}
	if path == "/api/ai/initializations" {
		server.aiInitializationsAPI(response, request, data)
		return
	}
	if strings.HasPrefix(path, "/api/ai/initializations/") {
		server.aiInitialRecoveryAPI(response, request, data, strings.TrimPrefix(path, "/api/ai/initializations/"))
		return
	}
	if path == "/api/ai/profiles/provision" {
		server.aiProfileProvisionAPI(response, request, data)
		return
	}
	if strings.HasPrefix(path, "/api/ai/models/") {
		server.aiModelAPI(response, request, data, strings.TrimPrefix(path, "/api/ai/models/"))
		return
	}
	if strings.HasPrefix(path, "/api/ai/profiles/") {
		server.aiProfileAPI(response, request, data, strings.TrimPrefix(path, "/api/ai/profiles/"))
		return
	}
	playerJSONError(response, http.StatusNotFound, "页面不存在")
}

func (server *Server) aiModelsAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	switch request.Method {
	case http.MethodGet:
		models, defaultID, err := server.aiModelViews(request.Context())
		if err != nil {
			aiStoreError(response, err)
			return
		}
		playerJSON(response, http.StatusOK, map[string]any{"models": models, "default_model_id": defaultID})
	case http.MethodPost:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		var input aiModelRequest
		if err := decodeAIJSON(request, &input); err != nil {
			aiDecodeError(response, err)
			return
		}
		config, err := normalizeAIModelInput(input, false)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		if input.APIKey != "" && server.aiSecrets == nil {
			playerJSONError(response, http.StatusServiceUnavailable, "AI 密钥存储尚未配置")
			return
		}
		if input.APIKey != "" {
			config.HasKey = true
		}
		actor := data.Session.Username
		created, err := server.aiStore.CreateModelConfigAs(request.Context(), config, actor)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		if input.APIKey != "" {
			if err := server.aiSecrets.WriteKey(created.ID, input.APIKey); err != nil {
				_ = server.aiStore.DeleteModelConfigCAS(context.Background(), created.ID, created.Version, actor)
				playerJSONError(response, http.StatusInternalServerError, "AI 密钥保存失败")
				return
			}
		}
		if input.Default != nil && *input.Default {
			if err := server.setAIDefaultModelID(request.Context(), created.ID, actor); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "默认模型保存失败")
				return
			}
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_model_created", created.ID, map[string]any{"version": created.Version, "default": input.Default != nil && *input.Default})
		view := aiModelViewFrom(created, server.aiDefaultModelID(request.Context()), boolPtr(input.APIKey != ""))
		playerJSON(response, http.StatusCreated, map[string]any{"model": view})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func (server *Server) aiModelAPI(response http.ResponseWriter, request *http.Request, data *pageData, id string) {
	parts := strings.Split(id, "/")
	if len(parts) == 2 && parts[1] == "test" {
		if request.Method != http.MethodPost {
			playerJSONError(response, http.StatusMethodNotAllowed, "连接测试必须使用 POST")
			return
		}
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		if !aiIDPattern.MatchString(parts[0]) {
			playerJSONError(response, http.StatusNotFound, "模型配置不存在")
			return
		}
		if server.aiConnectionTester == nil {
			playerJSONError(response, http.StatusServiceUnavailable, "AI runtime 尚未接入连接测试适配器")
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 120*time.Second)
		defer cancel()
		started := time.Now()
		if err := server.aiConnectionTester.TestModelConfig(ctx, parts[0]); err != nil {
			// Never forward an HTTP response body, a provider error, or a key.
			status, code, message := aiModelConnectionFailure(err)
			_ = server.recordAIAudit(request.Context(), data, "ai_model_test_failed", parts[0], map[string]any{"result": "failed", "code": code})
			playerJSON(response, status, map[string]any{"error": message, "code": code, "duration_ms": time.Since(started).Milliseconds()})
			return
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_model_test_succeeded", parts[0], map[string]any{"result": "succeeded"})
		playerJSON(response, http.StatusOK, map[string]any{"ok": true, "duration_ms": time.Since(started).Milliseconds()})
		return
	}
	if len(parts) != 1 || !aiIDPattern.MatchString(parts[0]) {
		playerJSONError(response, http.StatusNotFound, "模型配置不存在")
		return
	}
	id = parts[0]
	switch request.Method {
	case http.MethodGet:
		config, err := server.aiStore.GetModelConfig(request.Context(), id)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		var keyOverride *bool
		if server.aiSecrets != nil {
			if hasKey, keyErr := server.aiSecrets.HasKey(id); keyErr == nil {
				keyOverride = &hasKey
			}
		}
		playerJSON(response, http.StatusOK, map[string]any{"model": aiModelViewFrom(config, server.aiDefaultModelID(request.Context()), keyOverride)})
	case http.MethodPut, http.MethodPatch:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		var input aiModelRequest
		if err := decodeAIJSON(request, &input); err != nil {
			aiDecodeError(response, err)
			return
		}
		if input.ExpectedVersion < 1 {
			playerJSONError(response, http.StatusBadRequest, "缺少有效的 expected_version")
			return
		}
		patch, keyAction, err := normalizeAIModelPatch(input)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		if keyAction != "none" && server.aiSecrets == nil {
			playerJSONError(response, http.StatusServiceUnavailable, "AI 密钥存储尚未配置")
			return
		}
		if keyAction == "write" {
			if err := server.aiSecrets.WriteKey(id, input.APIKey); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "AI 密钥保存失败")
				return
			}
			value := true
			patch.HasKey = &value
		} else if keyAction == "remove" {
			if err := server.aiSecrets.RemoveKey(id); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "AI 密钥删除失败")
				return
			}
			value := false
			patch.HasKey = &value
		}
		updated, err := server.aiStore.UpdateModelConfigCAS(request.Context(), id, input.ExpectedVersion, patchWithActor(patch, data.Session.Username))
		if err != nil {
			aiStoreError(response, err)
			return
		}
		if input.Default != nil && *input.Default {
			if err := server.setAIDefaultModelID(request.Context(), id, data.Session.Username); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "默认模型保存失败")
				return
			}
		} else if input.Default != nil && server.aiDefaultModelID(request.Context()) == id {
			if err := server.setAIDefaultModelID(request.Context(), "", data.Session.Username); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "默认模型清除失败")
				return
			}
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_model_updated", id, map[string]any{"version": updated.Version, "default": input.Default != nil && *input.Default, "key_changed": keyAction != "none"})
		var keyOverride *bool
		if server.aiSecrets != nil {
			if hasKey, keyErr := server.aiSecrets.HasKey(id); keyErr == nil {
				keyOverride = &hasKey
			}
		}
		playerJSON(response, http.StatusOK, map[string]any{"model": aiModelViewFrom(updated, server.aiDefaultModelID(request.Context()), keyOverride)})
	case http.MethodDelete:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		version, err := parseExpectedVersion(request)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, "缺少有效的 expected_version")
			return
		}
		profiles, err := server.aiStore.ListProfiles(request.Context())
		if err != nil {
			aiStoreError(response, err)
			return
		}
		for _, profile := range profiles {
			if profile.ModelConfigID == id {
				playerJSONError(response, http.StatusConflict, "模型仍被 AI 玩家使用，请先解除绑定")
				return
			}
		}
		if err := server.aiStore.DeleteModelConfigCAS(request.Context(), id, version, data.Session.Username); err != nil {
			aiStoreError(response, err)
			return
		}
		if server.aiSecrets != nil {
			if err := server.aiSecrets.RemoveKey(id); err != nil {
				playerJSONError(response, http.StatusInternalServerError, "AI 密钥清理失败")
				return
			}
		}
		if server.aiDefaultModelID(request.Context()) == id {
			_ = server.setAIDefaultModelID(request.Context(), "", data.Session.Username)
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_model_deleted", id, map[string]any{"version": version})
		playerJSON(response, http.StatusOK, map[string]any{"ok": true})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func (server *Server) aiProfilesAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	switch request.Method {
	case http.MethodGet:
		profiles, err := server.aiStore.ListProfiles(request.Context())
		if err != nil {
			aiStoreError(response, err)
			return
		}
		views := make([]aiProfileView, 0, len(profiles))
		for _, profile := range profiles {
			views = append(views, server.aiProfileView(request.Context(), profile))
		}
		playerJSON(response, http.StatusOK, map[string]any{"profiles": views})
	case http.MethodPost:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		var input aiProfileCreateRequest
		if err := decodeAIJSON(request, &input); err != nil {
			aiDecodeError(response, err)
			return
		}
		profile, err := normalizeAIProfileCreate(input)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		if profile.ModelConfigID != "" {
			if _, err := server.aiStore.GetModelConfig(request.Context(), profile.ModelConfigID); err != nil {
				aiStoreError(response, err)
				return
			}
		}
		created, err := server.aiStore.CreateProfileAs(request.Context(), profile, data.Session.Username)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_created", created.ID, map[string]any{"version": created.Version, "unlimited_funds": created.UnlimitedFunds})
		playerJSON(response, http.StatusCreated, map[string]any{"profile": server.aiProfileView(request.Context(), created)})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func (server *Server) aiProfileProvisionAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodPost {
		playerJSONError(response, http.StatusMethodNotAllowed, "创建游戏 AI 角色必须使用 POST")
		return
	}
	if !server.aiWriteRequest(response, request, data) {
		return
	}
	provisioner, ok := server.aiProvisioner.(AIPlayerProvisioner)
	if !ok || provisioner == nil {
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_provision_unavailable", "", map[string]any{"result": "unavailable"})
		playerJSONError(response, http.StatusServiceUnavailable, "AI 游戏角色创建服务尚未配置")
		return
	}
	var input aiPlayerProvisionRequest
	if err := decodeAIJSON(request, &input); err != nil {
		aiDecodeError(response, err)
		return
	}
	requestValue, err := normalizeAIPlayerProvision(input)
	if err != nil {
		playerJSONError(response, http.StatusBadRequest, err.Error())
		return
	}
	if requestValue.ModelConfigID != "" {
		if _, err := server.aiStore.GetModelConfig(request.Context(), requestValue.ModelConfigID); err != nil {
			aiStoreError(response, err)
			return
		}
	}
	profileID := requestValue.ProfileID
	if profileID == "" {
		profileID, err = newAIProvisionProfileID()
		if err != nil {
			_ = server.recordAIAudit(request.Context(), data, "ai_profile_provision_failed", "", map[string]any{"result": "failed"})
			playerJSONError(response, http.StatusInternalServerError, "AI 游戏角色创建失败")
			return
		}
		requestValue.ProfileID = profileID
	}
	requestValue.Actor = data.Session.Username
	requestValue.ActorID = adminID(data)
	profile, err := provisioner.Provision(request.Context(), requestValue)
	if err != nil {
		// Provisioner errors may wrap game or account details. Keep those
		// details server-side and return only a generic result to the browser.
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_provision_failed", profileID, map[string]any{"result": "failed"})
		if errors.Is(err, aiinitial.ErrInvalidMount) {
			playerJSONError(response, http.StatusBadRequest, aiinitial.ErrInvalidMount.Error())
			return
		}
		playerJSONError(response, http.StatusBadGateway, "AI 游戏角色创建失败")
		return
	}
	if profile.ID != profileID || strings.TrimSpace(profile.Account.ID) == "" || strings.TrimSpace(profile.Account.Username) == "" ||
		strings.TrimSpace(profile.Character.ID) == "" || strings.TrimSpace(profile.Character.Name) == "" {
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_provision_failed", profileID, map[string]any{"result": "invalid_result"})
		playerJSONError(response, http.StatusInternalServerError, "AI 游戏角色创建失败")
		return
	}
	_ = server.recordAIAudit(request.Context(), data, "ai_profile_provisioned", profile.ID, map[string]any{
		"version": profile.Version, "unlimited_funds": profile.UnlimitedFunds,
	})
	playerJSON(response, http.StatusCreated, map[string]any{"profile": server.aiProfileView(request.Context(), profile)})
}

func (server *Server) aiProfileAPI(response http.ResponseWriter, request *http.Request, data *pageData, suffix string) {
	parts := strings.Split(suffix, "/")
	if len(parts) == 2 && (parts[1] == "local-command" || parts[1] == "executor") {
		server.aiLocalExecutorAPI(response, request, data, parts[0], parts[1])
		return
	}
	if len(parts) == 2 && parts[1] == "life-state" {
		server.aiLifeStateAPI(response, request, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "recovery" {
		server.aiUnknownRecoveryAPI(response, request, data, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		if request.Method != http.MethodGet {
			playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
			return
		}
		if !aiIDPattern.MatchString(parts[0]) {
			playerJSONError(response, http.StatusNotFound, "AI 玩家不存在")
			return
		}
		limit := maxAIEvents
		if raw := request.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > maxAIEvents {
				playerJSONError(response, http.StatusBadRequest, "limit 参数无效")
				return
			}
			limit = parsed
		}
		events, err := server.aiStore.ListEvents(request.Context(), parts[0], limit)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		views := make([]aiEventView, 0, len(events))
		for _, event := range events {
			views = append(views, aiEventView{ID: event.ID, ProfileID: event.ProfileID, Kind: event.Kind, Actor: event.Actor, Detail: redactAIJSON(event.Detail), CreatedAt: event.CreatedAt})
		}
		playerJSON(response, http.StatusOK, map[string]any{"events": views})
		return
	}
	if len(parts) == 2 && (parts[1] == "start" || parts[1] == "pause" || parts[1] == "stop") {
		if request.Method != http.MethodPost {
			playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
			return
		}
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		if !aiIDPattern.MatchString(parts[0]) {
			playerJSONError(response, http.StatusNotFound, "AI 玩家不存在")
			return
		}
		executor, ok := server.aiRuntime.(AIProfileRuntime)
		if !ok {
			playerJSONError(response, http.StatusServiceUnavailable, "AI runtime 尚未接入，当前状态为 stopped")
			return
		}
		var err error
		switch parts[1] {
		case "start":
			err = executor.StartProfile(request.Context(), parts[0])
		case "pause":
			err = executor.PauseProfile(request.Context(), parts[0])
		case "stop":
			err = executor.StopProfile(request.Context(), parts[0])
		}
		if err != nil {
			_ = server.recordAIAudit(request.Context(), data, "ai_profile_"+parts[1]+"_failed", parts[0], map[string]any{"result": "failed"})
			if errors.Is(err, aisupervisor.ErrAttemptRecovery) || errors.Is(err, airuntime.ErrAttemptPending) {
				playerJSONError(response, http.StatusConflict, "上一轮执行尚未结束，请稍后再次启动")
				return
			}
			if parts[1] == "start" {
				if message, ok := aiStartFailureMessage(err); ok {
					playerJSONError(response, http.StatusBadGateway, message)
					return
				}
			}
			playerJSONError(response, http.StatusBadGateway, "AI runtime 操作失败")
			return
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_"+parts[1], parts[0], map[string]any{"result": "accepted"})
		playerJSON(response, http.StatusAccepted, map[string]any{"ok": true})
		return
	}
	if len(parts) != 1 || !aiIDPattern.MatchString(parts[0]) {
		playerJSONError(response, http.StatusNotFound, "AI 玩家不存在")
		return
	}
	id := parts[0]
	switch request.Method {
	case http.MethodGet:
		profile, err := server.aiStore.GetProfile(request.Context(), id)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		playerJSON(response, http.StatusOK, map[string]any{"profile": server.aiProfileView(request.Context(), profile)})
	case http.MethodPut, http.MethodPatch:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		var input aiProfilePatchRequest
		if err := decodeAIJSON(request, &input); err != nil {
			aiDecodeError(response, err)
			return
		}
		if input.ExpectedVersion < 1 {
			playerJSONError(response, http.StatusBadRequest, "缺少有效的 expected_version")
			return
		}
		patch := airuntime.ProfilePatch{Account: input.Account, Character: input.Character, ModelConfigID: input.ModelConfigID,
			Personality: input.Personality, Goal: input.Goal, Skills: input.Skills,
			UnlimitedFunds: input.UnlimitedFunds, DailyTokenBudget: input.DailyTokenBudget,
			ExternalSpendLimit: input.ExternalSpendLimit, Status: input.Status, Actor: data.Session.Username}
		if err := validateAIProfilePatch(patch); err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		if patch.ModelConfigID != nil && *patch.ModelConfigID != "" {
			if _, err := server.aiStore.GetModelConfig(request.Context(), *patch.ModelConfigID); err != nil {
				aiStoreError(response, err)
				return
			}
		}
		updated, err := server.aiStore.UpdateProfileCAS(request.Context(), id, input.ExpectedVersion, patch)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_updated", id, map[string]any{"version": updated.Version})
		playerJSON(response, http.StatusOK, map[string]any{"profile": server.aiProfileView(request.Context(), updated)})
	case http.MethodDelete:
		if !server.aiWriteRequest(response, request, data) {
			return
		}
		version, err := parseExpectedVersion(request)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, "缺少有效的 expected_version")
			return
		}
		deleteVersion, err := server.aiProfileDeleteCheck(request.Context(), id, version)
		if err != nil {
			aiStoreError(response, err)
			return
		}
		if err := server.aiStore.DeleteProfileCAS(request.Context(), id, deleteVersion, data.Session.Username); err != nil {
			aiStoreError(response, err)
			return
		}
		_ = server.recordAIAudit(request.Context(), data, "ai_profile_deleted", id, map[string]any{"version": version})
		playerJSON(response, http.StatusOK, map[string]any{"ok": true})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

// aiProfileDeleteCheck is deliberately kept at the admin boundary. Deleting
// a profile is a destructive operation, so the handler must observe both the
// durable profile state and the live runtime before it delegates the actual
// compare-and-swap delete to the store.
func (server *Server) aiProfileDeleteCheck(ctx context.Context, id string, expectedVersion int64) (int64, error) {
	profile, err := server.aiStore.GetProfile(ctx, id)
	if err != nil {
		return 0, err
	}
	if profile.Version != expectedVersion {
		return 0, airuntime.ErrConflict
	}
	if profile.Status != airuntime.ProfileStatusStopped {
		return 0, errAIProfileDeleteActive
	}
	if server.aiRuntime == nil {
		return 0, errAIProfileDeleteStatus
	}
	executor, ok := server.aiRuntime.(AIProfileRuntime)
	if !ok {
		return 0, errAIProfileDeleteStatus
	}
	runtimeStatus, err := executor.ProfileStatus(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errAIProfileDeleteStatus, err)
	}
	if runtimeStatus.State != aisupervisor.StateStopped {
		return 0, errAIProfileDeleteActive
	}
	if recovery, ok := server.aiRuntime.(AIUnknownRecovery); ok {
		status, err := recovery.UnknownRecovery(ctx, id)
		if errors.Is(err, airuntime.ErrNotFound) {
			return profile.Version, nil
		}
		if err != nil {
			// A stopped profile may be deleted even when its remote worker is
			// offline. The profile fence prevents future dispatch; the unresolved
			// attempt remains in audit history instead of blocking administration.
			return profile.Version, nil
		}
		if status.ProfileID != "" || status.AttemptID != "" {
			if !status.Ready || !status.Execution.ContainerStopped {
				return 0, errAIProfileDeleteRecovery
			}
			preparer, ok := server.aiRuntime.(AIProfileDeletionPreparer)
			if !ok {
				return 0, errAIProfileDeleteStatus
			}
			preparedVersion, err := preparer.PrepareProfileDeletion(ctx, id, profile.Version)
			if err != nil {
				if errors.Is(err, airuntime.ErrConflict) || errors.Is(err, aisupervisor.ErrProfileChanged) {
					return 0, airuntime.ErrConflict
				}
				if errors.Is(err, aisupervisor.ErrAttemptRecovery) || errors.Is(err, aisupervisor.ErrAttemptStillRunning) || errors.Is(err, airuntime.ErrAttemptPending) {
					return 0, fmt.Errorf("%w: %v", errAIProfileDeleteRecovery, err)
				}
				return 0, fmt.Errorf("%w: %v", errAIProfileDeleteStatus, err)
			}
			if preparedVersion <= profile.Version {
				return 0, fmt.Errorf("%w: deletion fence did not advance profile", errAIProfileDeleteStatus)
			}
			return preparedVersion, nil
		}
	}
	return profile.Version, nil
}

func (server *Server) aiWriteRequest(response http.ResponseWriter, request *http.Request, data *pageData) bool {
	if data == nil || data.Session == nil || data.Session.Role != "admin" {
		playerJSONError(response, http.StatusForbidden, "仅管理员可以修改 AI 配置")
		return false
	}
	provided := request.Header.Get("X-CSRF-Token")
	if provided == "" || !hmac.Equal([]byte(provided), []byte(data.CSRF)) {
		playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
		return false
	}
	return true
}

func decodeAIJSON(request *http.Request, value any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("请使用 JSON 请求")
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("请求包含多个 JSON 对象")
		}
		return err
	}
	return nil
}

func aiDecodeError(response http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "请使用 JSON") {
		playerJSONError(response, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	if _, ok := err.(*http.MaxBytesError); ok {
		playerJSONError(response, http.StatusRequestEntityTooLarge, "请求内容过大")
		return
	}
	playerJSONError(response, http.StatusBadRequest, "请求参数无效")
}

func normalizeAIModelInput(input aiModelRequest, patch bool) (airuntime.ModelConfig, error) {
	backend := strings.TrimSpace(input.Backend)
	if backend == "" {
		backend = airuntime.ModelBackendCodex
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = aimodels.DeepSeekProvider
	}
	if !validAIBackend(backend) {
		return airuntime.ModelConfig{}, errors.New("不支持的 AI backend")
	}
	if !aiProviderPattern.MatchString(provider) {
		return airuntime.ModelConfig{}, errors.New("provider 标识无效")
	}
	name := strings.TrimSpace(input.Name)
	if len([]rune(name)) == 0 || len([]rune(name)) > maxAINameLength {
		return airuntime.ModelConfig{}, errors.New("模型名称长度无效")
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		switch provider {
		case aimodels.DeepSeekProvider:
			model = aimodels.DeepSeekFlash
		case "openai":
			model = "gpt-5"
		default:
			return airuntime.ModelConfig{}, errors.New("model 必填")
		}
	}
	if len([]rune(model)) == 0 || len([]rune(model)) > maxAIModelLength || strings.ContainsAny(model, "\x00\r\n") {
		return airuntime.ModelConfig{}, errors.New("模型标识长度无效")
	}
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		switch provider {
		case aimodels.DeepSeekProvider:
			baseURL = aimodels.DeepSeekBaseURL
		case "openai":
			baseURL = aimodels.OpenAIBaseURL
		default:
			return airuntime.ModelConfig{}, errors.New("base_url 必填")
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := validateAIModelBaseURL(baseURL); err != nil {
		return airuntime.ModelConfig{}, err
	}
	wireAPI := strings.TrimSpace(input.WireAPI)
	if wireAPI == "" {
		wireAPI = airuntime.ModelProviderResponses
	}
	if !validAIWireAPI(wireAPI) {
		return airuntime.ModelConfig{}, errors.New("wire_api 必须为 responses")
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = int64(defaultAITimeout / time.Second)
	}
	if input.TimeoutSeconds <= 0 || input.TimeoutSeconds > int64(maxAITimeout/time.Second) {
		return airuntime.ModelConfig{}, errors.New("timeout_seconds 必须为 1–86400")
	}
	if input.MaxOutputTokens == 0 {
		input.MaxOutputTokens = defaultAIMaxTokens
	}
	if input.MaxOutputTokens <= 0 || input.MaxOutputTokens > maxAIMaxOutputTokens {
		return airuntime.ModelConfig{}, errors.New("max_output_tokens 超出范围")
	}
	if input.DailyTokenBudget < 0 || input.DailyTokenBudget > maxAITokenBudget {
		return airuntime.ModelConfig{}, errors.New("daily_token_budget 超出范围")
	}
	if input.APIKey != "" && (len(input.APIKey) > maxAISecretLength || strings.ContainsAny(input.APIKey, "\r\n")) {
		return airuntime.ModelConfig{}, errors.New("API key 无效")
	}
	effort := strings.TrimSpace(input.ReasoningEffort)
	if model == aimodels.DeepSeekFlash {
		if effort == "" {
			effort = "high"
		}
		if !deepSeekReasoningEffort(effort) {
			return airuntime.ModelConfig{}, errors.New("DeepSeek-Flash 的 reasoning_effort 必须为 low、high 或 max")
		}
	} else if !genericReasoningEffort(effort) {
		return airuntime.ModelConfig{}, errors.New("reasoning_effort 无效，可留空或使用 none、minimal、low、medium、high、xhigh、max")
	}
	if patch {
		return airuntime.ModelConfig{}, nil
	}
	return airuntime.ModelConfig{ID: strings.TrimSpace(input.ID), Name: name, Backend: backend, Provider: provider,
		BaseURL: baseURL, Model: model, WireAPI: wireAPI, ReasoningEffort: effort,
		Timeout: time.Duration(input.TimeoutSeconds) * time.Second, MaxOutputTokens: input.MaxOutputTokens,
		DailyTokenBudget: input.DailyTokenBudget}, nil
}

func normalizeAIModelPatch(input aiModelRequest) (airuntime.ModelConfigPatch, string, error) {
	if input.APIKey != "" && input.ClearKey {
		return airuntime.ModelConfigPatch{}, "none", errors.New("不能同时设置 API key 和 clear_key")
	}
	if input.APIKey != "" && (len(input.APIKey) > maxAISecretLength || strings.ContainsAny(input.APIKey, "\r\n")) {
		return airuntime.ModelConfigPatch{}, "none", errors.New("API key 无效")
	}
	patch := airuntime.ModelConfigPatch{}
	if input.Name != "" {
		if len([]rune(input.Name)) > maxAINameLength {
			return patch, "none", errors.New("模型名称长度无效")
		}
		patch.Name = stringPtr(strings.TrimSpace(input.Name))
	}
	backend := strings.TrimSpace(input.Backend)
	provider := strings.TrimSpace(input.Provider)
	if backend != "" || provider != "" {
		if backend != "" && !validAIBackend(backend) {
			return patch, "none", errors.New("不支持的 AI backend")
		}
		if provider != "" && !aiProviderPattern.MatchString(provider) {
			return patch, "none", errors.New("provider 标识无效")
		}
		if backend != "" {
			patch.Backend = stringPtr(backend)
		}
		if provider != "" {
			patch.Provider = stringPtr(provider)
		}
	}
	if input.BaseURL != "" {
		baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
		if err := validateAIModelBaseURL(baseURL); err != nil {
			return patch, "none", err
		}
		patch.BaseURL = stringPtr(baseURL)
	}
	if input.Model != "" {
		model := strings.TrimSpace(input.Model)
		if len([]rune(model)) > maxAIModelLength || strings.ContainsAny(model, "\x00\r\n") {
			return patch, "none", errors.New("模型标识长度无效")
		}
		patch.Model = stringPtr(model)
	}
	if input.WireAPI != "" {
		wireAPI := strings.TrimSpace(input.WireAPI)
		if !validAIWireAPI(wireAPI) {
			return patch, "none", errors.New("wire_api 必须为 responses")
		}
		patch.WireAPI = &wireAPI
	}
	if input.ReasoningEffort != "" {
		effort := strings.TrimSpace(input.ReasoningEffort)
		if !genericReasoningEffort(effort) {
			return patch, "none", errors.New("reasoning_effort 无效，可留空或使用 none、minimal、low、medium、high、xhigh、max")
		}
		patch.ReasoningEffort = &effort
	}
	if input.TimeoutSeconds != 0 {
		if input.TimeoutSeconds < 1 || time.Duration(input.TimeoutSeconds)*time.Second > maxAITimeout {
			return patch, "none", errors.New("timeout_seconds 必须为 1–86400")
		}
		value := time.Duration(input.TimeoutSeconds) * time.Second
		patch.Timeout = &value
	}
	if input.MaxOutputTokens != 0 {
		if input.MaxOutputTokens < 1 || input.MaxOutputTokens > maxAIMaxOutputTokens {
			return patch, "none", errors.New("max_output_tokens 超出范围")
		}
		patch.MaxOutputTokens = &input.MaxOutputTokens
	}
	if input.DailyTokenBudget != 0 {
		if input.DailyTokenBudget < 0 || input.DailyTokenBudget > maxAITokenBudget {
			return patch, "none", errors.New("daily_token_budget 超出范围")
		}
		patch.DailyTokenBudget = &input.DailyTokenBudget
	}
	keyAction := "none"
	if input.APIKey != "" {
		keyAction = "write"
	} else if input.ClearKey {
		keyAction = "remove"
	}
	return patch, keyAction, nil
}

func validateAIModelBaseURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxAIURLLength || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("Base URL 长度或格式无效")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("Base URL 必须为不含凭据、query 或 fragment 的 HTTP(S) 地址")
	}
	return nil
}

func validAIWireAPI(value string) bool {
	return value == airuntime.ModelProviderResponses
}

func genericReasoningEffort(value string) bool {
	if value == "" {
		return true
	}
	for _, level := range genericAIReasoningLevels {
		if value == level {
			return true
		}
	}
	return false
}

func deepSeekReasoningEffort(value string) bool {
	return value == "low" || value == "high" || value == "max"
}

func normalizeAIProfileCreate(input aiProfileCreateRequest) (airuntime.Profile, error) {
	if strings.TrimSpace(input.Account.ID) == "" || len(input.Account.ID) > 128 {
		return airuntime.Profile{}, errors.New("account.id 必填且长度无效")
	}
	if strings.TrimSpace(input.Character.ID) == "" || len(input.Character.ID) > 128 {
		return airuntime.Profile{}, errors.New("character.id 必填且长度无效")
	}
	if input.ModelConfigID != "" && !aiIDPattern.MatchString(input.ModelConfigID) {
		return airuntime.Profile{}, errors.New("model_config_id 无效")
	}
	if err := validateAIPersonality(input.Personality); err != nil {
		return airuntime.Profile{}, err
	}
	if err := validateAIGoal(input.Goal); err != nil {
		return airuntime.Profile{}, err
	}
	skills, err := normalizeAIProfileSkills(input.Skills)
	if err != nil {
		return airuntime.Profile{}, err
	}
	unlimited := true
	if input.UnlimitedFunds != nil {
		unlimited = *input.UnlimitedFunds
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = airuntime.ProfileStatusStopped
	}
	if status != airuntime.ProfileStatusActive && status != airuntime.ProfileStatusPaused && status != airuntime.ProfileStatusStopped {
		return airuntime.Profile{}, errors.New("status 无效")
	}
	if input.DailyTokenBudget < 0 || input.DailyTokenBudget > maxAITokenBudget || input.ExternalSpendLimit < 0 || input.ExternalSpendLimit > maxAIExternalSpend {
		return airuntime.Profile{}, errors.New("预算超出范围")
	}
	return airuntime.Profile{ID: strings.TrimSpace(input.ID), Account: input.Account, Character: input.Character,
		ModelConfigID: strings.TrimSpace(input.ModelConfigID), Personality: input.Personality, Goal: input.Goal,
		Skills: skills, UnlimitedFunds: unlimited, DailyTokenBudget: input.DailyTokenBudget,
		ExternalSpendLimit: input.ExternalSpendLimit, Status: status}, nil
}

func validateAIProfilePatch(patch airuntime.ProfilePatch) error {
	if patch.Account != nil && (strings.TrimSpace(patch.Account.ID) == "" || len(patch.Account.ID) > 128) {
		return errors.New("account.id 必填且长度无效")
	}
	if patch.Character != nil && (strings.TrimSpace(patch.Character.ID) == "" || len(patch.Character.ID) > 128) {
		return errors.New("character.id 必填且长度无效")
	}
	if patch.ModelConfigID != nil && *patch.ModelConfigID != "" && !aiIDPattern.MatchString(*patch.ModelConfigID) {
		return errors.New("model_config_id 无效")
	}
	if patch.Personality != nil {
		if err := validateAIPersonality(*patch.Personality); err != nil {
			return err
		}
	}
	if patch.Goal != nil {
		if err := validateAIGoal(*patch.Goal); err != nil {
			return err
		}
	}
	if patch.Skills != nil {
		skills, err := normalizeAIProfileSkills(*patch.Skills)
		if err != nil {
			return err
		}
		*patch.Skills = skills
	}
	if patch.Status != nil && *patch.Status != airuntime.ProfileStatusActive && *patch.Status != airuntime.ProfileStatusPaused && *patch.Status != airuntime.ProfileStatusStopped {
		return errors.New("status 无效")
	}
	if patch.DailyTokenBudget != nil && (*patch.DailyTokenBudget < 0 || *patch.DailyTokenBudget > maxAITokenBudget) {
		return errors.New("daily_token_budget 超出范围")
	}
	if patch.ExternalSpendLimit != nil && (*patch.ExternalSpendLimit < 0 || *patch.ExternalSpendLimit > maxAIExternalSpend) {
		return errors.New("external_spend_limit 超出范围")
	}
	return nil
}

func validateAIPersonality(value airuntime.Personality) error {
	if len([]rune(value.Name)) > maxAINameLength || len([]rune(value.Prompt)) > maxAIPromptLength {
		return errors.New("人格名称或提示词过长")
	}
	if len(value.Traits) > 64 || len(value.Values) > 64 {
		return errors.New("人格字段过多")
	}
	return nil
}

func validateAIGoal(value airuntime.Goal) error {
	if err := value.ValidateLife(); err != nil {
		return err
	}
	if err := value.CharacterBuild.Validate(); err != nil {
		return err
	}
	if len([]rune(value.Kind)) > 64 || len([]rune(value.Description)) > maxAIPromptLength || value.TargetLevel < 0 || value.TargetLevel > 1000 {
		return errors.New("目标参数无效")
	}
	if value.TargetCharacterID != "" && !aiIDPattern.MatchString(value.TargetCharacterID) {
		return errors.New("goal.target_character_id 无效")
	}
	if len(value.Metadata) > 64 {
		return errors.New("目标 metadata 过多")
	}
	return nil
}

func normalizeAIPlayerProvision(input aiPlayerProvisionRequest) (AIPlayerProvisionRequest, error) {
	if err := input.InitialState.Validate(); err != nil {
		return AIPlayerProvisionRequest{}, err
	}
	profileID := strings.TrimSpace(input.ID)
	if profileID != "" && !aiIDPattern.MatchString(profileID) {
		return AIPlayerProvisionRequest{}, errors.New("id 无效")
	}
	characterName := strings.TrimSpace(input.CharacterName)
	if characterName == "" || len([]byte(characterName)) > 64 || strings.ContainsAny(characterName, "\x00\r\n") {
		return AIPlayerProvisionRequest{}, errors.New("角色名称长度或格式无效")
	}
	if input.CharacterSlot < 0 || input.CharacterSlot > 1 {
		return AIPlayerProvisionRequest{}, errors.New("角色槽位无效")
	}
	modelConfigID := strings.TrimSpace(input.ModelConfigID)
	if modelConfigID != "" && !aiIDPattern.MatchString(modelConfigID) {
		return AIPlayerProvisionRequest{}, errors.New("model_config_id 无效")
	}
	if err := validateAIPersonality(input.Personality); err != nil {
		return AIPlayerProvisionRequest{}, err
	}
	if err := validateAIGoal(input.Goal); err != nil {
		return AIPlayerProvisionRequest{}, err
	}
	skills, err := normalizeAIProvisionSkills(input.SkillNames)
	if err != nil {
		return AIPlayerProvisionRequest{}, err
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = airuntime.ProfileStatusStopped
	}
	if status != airuntime.ProfileStatusActive && status != airuntime.ProfileStatusPaused && status != airuntime.ProfileStatusStopped {
		return AIPlayerProvisionRequest{}, errors.New("status 无效")
	}
	if input.DailyTokenBudget < 0 || input.DailyTokenBudget > maxAITokenBudget || input.ExternalSpendLimit < 0 || input.ExternalSpendLimit > maxAIExternalSpend {
		return AIPlayerProvisionRequest{}, errors.New("预算超出范围")
	}
	return AIPlayerProvisionRequest{
		ProfileID:          profileID,
		InitialState:       input.InitialState,
		CharacterName:      characterName,
		CharacterSlot:      input.CharacterSlot,
		ModelConfigID:      modelConfigID,
		Personality:        input.Personality,
		Goal:               input.Goal,
		Skills:             skills,
		UnlimitedFunds:     input.UnlimitedFunds,
		DailyTokenBudget:   input.DailyTokenBudget,
		ExternalSpendLimit: input.ExternalSpendLimit,
		Status:             status,
	}, nil
}

func newAIProvisionProfileID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "ai-player-" + hex.EncodeToString(random[:]), nil
}

func validateAISkills(skills []airuntime.SkillVersion) error {
	if len(skills) > maxAISkills {
		return errors.New("skill 数量超出范围")
	}
	seen := map[string]bool{}
	for _, skill := range skills {
		if !aiIDPattern.MatchString(skill.Name) || seen[skill.Name] || len(skill.Version) == 0 || len(skill.Version) > 64 {
			return errors.New("skill 名称或版本无效")
		}
		if len(skill.Description) > maxAIPromptLength {
			return errors.New("skill 描述过长")
		}
		if strings.TrimSpace(string(skill.Parameters)) != "" && !json.Valid(skill.Parameters) {
			return errors.New("skill 参数必须是有效 JSON")
		}
		seen[skill.Name] = true
	}
	return nil
}

// normalizeAIProfileSkills keeps the historical /api/ai/profiles CRUD
// surface usable for old non-native metadata while making every recognized
// native skill canonical. Known catalog entries cannot carry a forged
// version, kind, path, or digest. New provisioning is stricter and uses
// normalizeAIProvisionSkills, which accepts names only and requires the core
// skill.
func normalizeAIProfileSkills(skills []airuntime.SkillVersion) ([]airuntime.SkillVersion, error) {
	if err := validateAISkills(skills); err != nil {
		return nil, err
	}
	specs := aimcp.SkillCatalog()
	byName := make(map[string]aimcp.SkillSpec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	result := make([]airuntime.SkillVersion, 0, len(skills))
	for _, skill := range skills {
		spec, known := byName[skill.Name]
		if !known {
			result = append(result, skill)
			continue
		}
		if skill.Version != spec.Version {
			return nil, fmt.Errorf("skill %q 版本不在服务端原生技能目录中", skill.Name)
		}
		if skill.Kind != "" && skill.Kind != airuntime.SkillKindNative {
			return nil, fmt.Errorf("skill %q kind 无效", skill.Name)
		}
		if skill.Path != "" && skill.Path != spec.RelativePath {
			return nil, fmt.Errorf("skill %q path 不在服务端原生技能目录中", skill.Name)
		}
		if !aiSkillDigestMatches(skill.Digest, spec.SHA256) {
			return nil, fmt.Errorf("skill %q digest 不在服务端原生技能目录中", skill.Name)
		}
		result = append(result, canonicalAISkill(spec))
	}
	return result, nil
}

func aiSkillDigestMatches(value, expected string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return true
	}
	value = strings.TrimPrefix(value, "sha256:")
	return value == strings.ToLower(strings.TrimSpace(expected))
}

func validAIBackend(value string) bool {
	return value == airuntime.ModelBackendCodex
}

func parseExpectedVersion(request *http.Request) (int64, error) {
	raw := request.URL.Query().Get("expected_version")
	if raw == "" {
		raw = request.Header.Get("X-Expected-Version")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New("invalid expected version")
	}
	return value, nil
}

func patchWithActor(patch airuntime.ModelConfigPatch, actor string) airuntime.ModelConfigPatch {
	patch.Actor = actor
	return patch
}

func boolPtr(value bool) *bool       { return &value }
func stringPtr(value string) *string { return &value }

func aiStoreError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, airuntime.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, airuntime.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, errAIProfileDeleteActive), errors.Is(err, errAIProfileDeleteRecovery), errors.Is(err, errAIProfileDeleteStatus):
		status = http.StatusConflict
	case errors.Is(err, airuntime.ErrInvalidProfile), errors.Is(err, airuntime.ErrInvalidProvider), errors.Is(err, airuntime.ErrInvalidSkill), errors.Is(err, airuntime.ErrInvalidArguments):
		status = http.StatusBadRequest
	}
	message := "AI runtime 操作失败"
	if status == http.StatusNotFound {
		message = "目标不存在"
	} else if status == http.StatusConflict {
		if errors.Is(err, errAIProfileDeleteActive) {
			message = "AI 玩家仍在运行或尚未停止，不能删除"
		} else if errors.Is(err, errAIProfileDeleteRecovery) {
			message = "异常执行仍在处理中，不能删除"
		} else if errors.Is(err, errAIProfileDeleteStatus) {
			message = "无法确认 AI 玩家已停止，请稍后重试"
		} else {
			message = "数据已被其他操作修改，请刷新后重试"
		}
	} else if status == http.StatusBadRequest {
		message = "AI 配置参数无效"
	}
	playerJSONError(response, status, message)
}

func aiStartFailureMessage(err error) (string, bool) {
	var failure *aisupervisor.StartFailure
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	stage := AIStartStageLabel(failure.Stage)
	code := AIStartCodeLabel(failure.Code)
	if stage == "" {
		stage = "未知阶段"
	}
	if code == "" {
		code = "未知错误"
	}
	message := "AI 玩家启动失败（阶段：" + stage + "，类别：" + code
	if failure.Duration > 0 {
		message += ", 耗时：" + strconv.FormatInt(failure.Duration.Milliseconds(), 10) + "ms"
	}
	return message + "）", true
}

func redactAIJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage("null")
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return json.RawMessage("null")
	}
	redactAIValue(decoded)
	result, err := json.Marshal(decoded)
	if err != nil {
		return json.RawMessage("null")
	}
	return result
}

func redactAIValue(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if lower == "api_key" || lower == "apikey" || lower == "secret_key" || lower == "secret" {
				current[key] = "[redacted]"
				continue
			}
			redactAIValue(child)
		}
	case []any:
		for _, child := range current {
			redactAIValue(child)
		}
	}
}

func (server *Server) recordAIAudit(ctx context.Context, data *pageData, event, subject string, details map[string]any) error {
	if server.store == nil {
		return nil
	}
	if details == nil {
		details = map[string]any{}
	}
	detail, err := json.Marshal(details)
	if err != nil {
		return err
	}
	sourceIP := ""
	if data != nil {
		sourceIP = data.SourceIP
	}
	return server.store.RecordAudit(ctx, adminID(data), event, subject, sourceIP, string(redactAIJSON(detail)))
}
