package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

func TestNormalizeAIModelInputUsesReviewedDeepSeekDefaults(t *testing.T) {
	config, err := normalizeAIModelInput(aiModelRequest{Name: "DeepSeek Flash"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if config.Backend != "codex" || config.Provider != "deepseek" || config.BaseURL != "https://api.deepseek.com" || config.Model != "deepseek-flash" || config.WireAPI != airuntime.ModelProviderResponses || config.ReasoningEffort != "high" {
		t.Fatalf("defaults = %#v", config)
	}
	if config.Timeout <= 0 || config.MaxOutputTokens <= 0 {
		t.Fatalf("runtime limits were not defaulted: %#v", config)
	}
}

func TestNormalizeAIModelInputRejectsUnsupportedCodexSettings(t *testing.T) {
	for name, input := range map[string]aiModelRequest{
		"backend":   {Name: "x", Backend: "responses"},
		"reasoning": {Name: "x", Provider: "openai", Model: "gpt-4o", ReasoningEffort: "invalid"},
		"wire_api":  {Name: "x", Provider: "openai", Model: "gpt-4o", WireAPI: "legacy_completions"},
		"url":       {Name: "x", Provider: "openai", Model: "gpt-4o", BaseURL: "https://user:password@example.invalid/v1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeAIModelInput(input, false); err == nil {
				t.Fatal("invalid model configuration accepted")
			}
		})
	}
}

func TestNormalizeAIModelInputAcceptsGenericOpenAICompatibleModel(t *testing.T) {
	config, err := normalizeAIModelInput(aiModelRequest{
		Name: "local GPT", Provider: "custom", BaseURL: "http://llm:8000/v1", Model: "gpt-4o-mini",
		WireAPI: airuntime.ModelProviderResponses,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if config.Backend != airuntime.ModelBackendCodex || config.Provider != "custom" || config.BaseURL != "http://llm:8000/v1" || config.Model != "gpt-4o-mini" || config.WireAPI != airuntime.ModelProviderResponses || config.ReasoningEffort != "" {
		t.Fatalf("generic config = %#v", config)
	}
}

func TestReviewedAIModelCatalogIncludesEditablePresets(t *testing.T) {
	catalog, err := reviewedAIModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Presets) != 3 {
		t.Fatalf("presets = %#v", catalog.Presets)
	}
	for _, want := range []string{"deepseek", "openai", "custom"} {
		found := false
		for _, preset := range catalog.Presets {
			if preset.ID == want {
				found = true
				if preset.WireAPI != airuntime.ModelProviderResponses {
					t.Fatalf("preset %s wire API = %q", want, preset.WireAPI)
				}
			}
		}
		if !found {
			t.Fatalf("preset %s missing", want)
		}
	}
}

func TestValidateAISkillsAllowsNativeMetadataWithoutParameters(t *testing.T) {
	skills := []airuntime.SkillVersion{{Name: "stoneage-chat", Version: "1", Kind: airuntime.SkillKindNative, Digest: "sha256:metadata"}}
	if err := validateAISkills(skills); err != nil {
		t.Fatalf("native skill metadata was rejected without a function schema: %v", err)
	}
}

func TestNormalizeAIProvisionSkillsUsesFixedCatalog(t *testing.T) {
	skills, err := normalizeAIProvisionSkills([]string{"stoneage-quest", "stoneage-play"})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "stoneage-play" || skills[1].Name != "stoneage-quest" {
		t.Fatalf("catalog order = %#v", skills)
	}
	catalog := map[string]aimcp.SkillSpec{}
	for _, spec := range aimcp.SkillCatalog() {
		catalog[spec.Name] = spec
	}
	for _, skill := range skills {
		spec, ok := catalog[skill.Name]
		if !ok || skill.Kind != airuntime.SkillKindNative || skill.Version != spec.Version || skill.Path != spec.RelativePath || skill.Digest != "sha256:"+spec.SHA256 || skill.Parameters != nil {
			t.Fatalf("non-canonical skill = %#v", skill)
		}
	}
	for _, names := range [][]string{nil, {}, {"stoneage-leveling"}, {"stoneage-play", "unknown"}, {"stoneage-play", "stoneage-play"}} {
		if _, err := normalizeAIProvisionSkills(names); err == nil {
			t.Fatalf("invalid provision skill names accepted: %#v", names)
		}
	}

	specs := aimcp.SkillCatalog()
	if len(specs) == 0 {
		t.Fatal("fixed skill catalog is empty")
	}
	for _, spec := range specs {
		names := []string{aiCoreSkillName}
		if spec.Name != aiCoreSkillName {
			names = append(names, spec.Name)
		}
		canonical, err := normalizeAIProvisionSkills(names)
		if err != nil {
			t.Fatalf("catalog skill %s was rejected: %v", spec.Name, err)
		}
		found := false
		for _, skill := range canonical {
			if skill.Name == spec.Name {
				found = true
				if skill.Version != spec.Version || skill.Digest != "sha256:"+spec.SHA256 || skill.Path != spec.RelativePath {
					t.Fatalf("catalog skill %s was not canonicalized: %#v", spec.Name, skill)
				}
			}
		}
		if !found {
			t.Fatalf("catalog skill %s was dropped", spec.Name)
		}
	}
}

func TestNormalizeAIProfileSkillsCanonicalizesKnownEntriesAndPreservesLegacy(t *testing.T) {
	legacy := airuntime.SkillVersion{Name: "legacy-skill", Version: "1", Kind: airuntime.SkillKindNative, Digest: "sha256:legacy"}
	var playSpec aimcp.SkillSpec
	for _, spec := range aimcp.SkillCatalog() {
		if spec.Name == aiCoreSkillName {
			playSpec = spec
		}
	}
	if playSpec.Name == "" {
		t.Fatal("stoneage-play is missing from the fixed catalog")
	}
	known := airuntime.SkillVersion{Name: playSpec.Name, Version: playSpec.Version, Kind: airuntime.SkillKindNative, Digest: "sha256:" + playSpec.SHA256}
	got, err := normalizeAIProfileSkills([]airuntime.SkillVersion{known, legacy})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != aiCoreSkillName || got[0].Version != playSpec.Version || got[0].Digest != known.Digest || got[0].Path != playSpec.RelativePath || got[1].Name != legacy.Name {
		t.Fatalf("normalized profile skills = %#v", got)
	}

	for _, forged := range []airuntime.SkillVersion{
		{Name: aiCoreSkillName, Version: "9.9.9", Kind: airuntime.SkillKindNative},
		{Name: aiCoreSkillName, Version: "1.0.0", Kind: "function"},
		{Name: aiCoreSkillName, Version: "1.0.0", Kind: airuntime.SkillKindNative, Digest: "sha256:forged"},
		{Name: aiCoreSkillName, Version: "1.0.0", Kind: airuntime.SkillKindNative, Path: "../../outside"},
	} {
		if _, err := normalizeAIProfileSkills([]airuntime.SkillVersion{forged}); err == nil {
			t.Fatalf("forged native skill accepted: %#v", forged)
		}
	}
}

func TestAIModelViewNeverContainsSecret(t *testing.T) {
	config := airuntime.ModelConfig{ID: "model-1", Name: "test", Backend: "codex", Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", ReasoningEffort: "high"}
	value, err := json.Marshal(aiModelViewFrom(config, "", boolPtr(true)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(value), "api_key") || strings.Contains(string(value), "sk-secret") {
		t.Fatalf("model view contains secret material: %s", value)
	}
}

func TestRedactAIJSONRemovesSecretFields(t *testing.T) {
	redacted := redactAIJSON(json.RawMessage(`{"api_key":"sk-secret","nested":{"secret_key":"value","result":"ok"}}`))
	if strings.Contains(string(redacted), "sk-secret") || strings.Contains(string(redacted), `"value"`) {
		t.Fatalf("secret was not redacted: %s", redacted)
	}
	if !strings.Contains(string(redacted), "[redacted]") || !strings.Contains(string(redacted), "ok") {
		t.Fatalf("redacted detail lost safe data: %s", redacted)
	}
}

func TestAIModelAPIStoresOnlyMaskedKeyAndDefault(t *testing.T) {
	ctx := context.Background()
	authStore, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer authStore.Close()
	if err := authStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := authStore.CreateAdmin(ctx, "admin", []byte("secret123")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("build/ai", 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("build/ai", "admin-http-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	aiStore, err := airuntime.OpenWithSecrets(filepath.Join(root, "state.db"), filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer aiStore.Close()
	aiSecrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewServer(authStore, Options{AIStore: aiStore, AISecrets: aiSecrets, CSRFSecret: []byte("ai-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(control.Handler())
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := client.PostForm(server.URL+"/login", map[string][]string{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	page, err := client.Get(server.URL + "/ai/models")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	csrfMatch := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(pageBody)
	if page.StatusCode != http.StatusOK || len(csrfMatch) != 2 {
		t.Fatalf("AI page = %d %s", page.StatusCode, pageBody)
	}
	key := "sk-admin-secret"
	payload, _ := json.Marshal(map[string]any{"name": "DeepSeek Flash", "backend": "codex", "provider": "deepseek", "base_url": "https://api.deepseek.com/", "model": "deepseek-flash", "reasoning_effort": "high", "timeout_seconds": 120, "max_output_tokens": 4096, "daily_token_budget": 100000, "api_key": key, "default": true})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/models", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", string(csrfMatch[1]))
	createdResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(createdResponse.Body)
	createdResponse.Body.Close()
	if createdResponse.StatusCode != http.StatusCreated || strings.Contains(string(body), key) || strings.Contains(string(body), "api_key") {
		t.Fatalf("create response = %d %s", createdResponse.StatusCode, body)
	}
	var created struct {
		Model aiModelView `json:"model"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if !created.Model.HasKey || !created.Model.Default || created.Model.ReasoningEffort != "high" {
		t.Fatalf("created model view = %#v", created.Model)
	}
	stored, err := aiSecrets.ReadKey(created.Model.ID)
	if err != nil || stored != key {
		t.Fatalf("stored key = %q, err=%v", stored, err)
	}
	defaultID, err := aiStore.GetDefaultModelConfigID(ctx)
	if err != nil || defaultID != created.Model.ID {
		t.Fatalf("default model = %q, err=%v", defaultID, err)
	}

	operatorPage, err := client.Get(server.URL + "/api/ai/models")
	if err != nil {
		t.Fatal(err)
	}
	operatorPage.Body.Close()
	if operatorPage.StatusCode != http.StatusOK {
		t.Fatalf("model list status = %d", operatorPage.StatusCode)
	}
}

type fakeAIPlayerProvisioner struct {
	request AIPlayerProvisionRequest
	profile airuntime.Profile
	err     error
	calls   int
}

func (fake *fakeAIPlayerProvisioner) Provision(_ context.Context, request AIPlayerProvisionRequest) (airuntime.Profile, error) {
	fake.request = request
	fake.calls++
	if fake.err != nil {
		return airuntime.Profile{}, fake.err
	}
	profile := fake.profile
	if profile.ID == "" {
		profile.ID = request.ProfileID
	}
	if profile.Account.ID == "" {
		profile.Account = airuntime.AccountIdentity{ID: "ai-generated", Username: "ai-generated"}
	}
	if profile.Character.ID == "" {
		profile.Character = airuntime.CharacterIdentity{ID: "ai-generated:0", Name: request.CharacterName}
	}
	return profile, nil
}

func TestAIProfileProvisionUsesServerOwnedIdentityAndNeverReturnsCredentials(t *testing.T) {
	ctx := context.Background()
	fake := &fakeAIPlayerProvisioner{}
	authStore, aiStore, server, client, csrf := newAIProvisionHTTPFixture(t, fake)
	defer authStore.Close()
	defer aiStore.Close()

	payload := map[string]any{
		"character_name": "远征者", "character_slot": 1,
		"initial_state":   map[string]any{"mode": "custom", "character_level": 60, "hometown": 3, "weights": map[string]int{"vital": 1, "strength": 3}, "pets": []map[string]any{{"template_id": 42, "level": 30}}, "mount": true},
		"model_config_id": "", "personality": map[string]any{"name": "稳健", "prompt": "先观察再行动"},
		"goal":            map[string]any{"kind": "leveling", "description": "达到 80 级", "target_level": 80, "stop_when_completed": true},
		"skill_names":     []string{"stoneage-play", "stoneage-leveling"},
		"unlimited_funds": true, "daily_token_budget": 100000, "external_spend_limit": 0, "status": "stopped",
	}
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("provision status = %d body=%s", response.StatusCode, responseBody)
	}
	if strings.Contains(string(responseBody), "password") || strings.Contains(string(responseBody), "secret") {
		t.Fatalf("provision response contains credential material: %s", responseBody)
	}
	if fake.calls != 1 || fake.request.ProfileID == "" || !aiIDPattern.MatchString(fake.request.ProfileID) || fake.request.CharacterName != "远征者" || fake.request.CharacterSlot != 1 {
		t.Fatalf("provision request = %#v calls=%d", fake.request, fake.calls)
	}
	if fake.request.InitialState == nil || fake.request.InitialState.CharacterLevel != 60 || fake.request.InitialState.Hometown != 3 || fake.request.InitialState.Weights.Strength != 3 || fake.request.InitialState.Mount == nil || !*fake.request.InitialState.Mount || len(fake.request.InitialState.Pets) != 1 || fake.request.InitialState.Pets[0].TemplateID != 42 || fake.request.InitialState.Pets[0].Level != 30 {
		t.Fatal("initial state not forwarded")
	}
	if fake.request.Actor != "admin" || fake.request.ActorID == nil || *fake.request.ActorID <= 0 {
		t.Fatalf("server actor was not attached: %#v", fake.request)
	}
	if fake.request.Personality.Name != "稳健" || fake.request.Goal.TargetLevel != 80 || len(fake.request.Skills) != 2 || fake.request.Skills[0].Name != "stoneage-leveling" || fake.request.Skills[1].Name != "stoneage-play" {
		t.Fatalf("AI preferences were not forwarded: %#v", fake.request)
	}
	catalog := map[string]aimcp.SkillSpec{}
	for _, spec := range aimcp.SkillCatalog() {
		catalog[spec.Name] = spec
	}
	for _, skill := range fake.request.Skills {
		spec, ok := catalog[skill.Name]
		if !ok || skill.Version != spec.Version || skill.Kind != airuntime.SkillKindNative || skill.Digest != "sha256:"+spec.SHA256 || skill.Path != spec.RelativePath || skill.Parameters != nil {
			t.Fatalf("provision skill was not canonicalized: %#v", skill)
		}
	}
	var result struct {
		Profile aiProfileView `json:"profile"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		t.Fatal(err)
	}
	if result.Profile.ID != fake.request.ProfileID || result.Profile.Account.Username != "ai-generated" || result.Profile.Character.Name != "远征者" {
		t.Fatalf("provision result = %#v", result.Profile)
	}

	unauthenticated, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse, err := client.Do(unauthenticated)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticatedResponse.Body.Close()
	if unauthenticatedResponse.StatusCode != http.StatusForbidden || fake.calls != 1 {
		t.Fatalf("missing CSRF status=%d calls=%d", unauthenticatedResponse.StatusCode, fake.calls)
	}

	operatorID := fake.request.ActorID
	if operatorID == nil {
		t.Fatal("missing admin actor ID")
	}
	if _, err := authStore.DB().ExecContext(ctx, "UPDATE admin_users SET role='operator' WHERE id=?", *operatorID); err != nil {
		t.Fatal(err)
	}
	operatorRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
	operatorRequest.Header.Set("Content-Type", "application/json")
	operatorRequest.Header.Set("X-CSRF-Token", csrf)
	operatorResponse, err := client.Do(operatorRequest)
	if err != nil {
		t.Fatal(err)
	}
	operatorResponse.Body.Close()
	if operatorResponse.StatusCode != http.StatusForbidden || fake.calls != 1 {
		t.Fatalf("operator provision status=%d calls=%d", operatorResponse.StatusCode, fake.calls)
	}
}

func TestAIProfileProvisionRejectsLegacyOrForgedSkillPayloads(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{name: "invalid initial state", payload: map[string]any{"character_name": "错误等级", "skill_names": []string{"stoneage-play"}, "initial_state": map[string]any{"mode": "custom", "character_level": 141}}},
		{name: "legacy skill objects", payload: map[string]any{
			"character_name": "旧格式", "skills": []any{map[string]any{"name": "stoneage-play", "version": "9", "digest": "sha256:forged"}},
		}},
		{name: "unknown skill", payload: map[string]any{
			"character_name": "未知技能", "skill_names": []string{"stoneage-play", "skill-from-browser"},
		}},
		{name: "missing core", payload: map[string]any{
			"character_name": "缺少核心", "skill_names": []string{"stoneage-leveling"},
		}},
		{name: "empty selection", payload: map[string]any{
			"character_name": "空选择", "skill_names": []string{},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeAIPlayerProvisioner{}
			_, _, server, client, csrf := newAIProvisionHTTPFixture(t, fake)
			body, _ := json.Marshal(test.payload)
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || fake.calls != 0 {
				t.Fatalf("invalid provision status=%d calls=%d", response.StatusCode, fake.calls)
			}
		})
	}
}

func TestAIProfileProvisionFailureIsGenericAndAudited(t *testing.T) {
	authStore, aiStore, server, client, csrf := newAIProvisionHTTPFixture(t, &fakeAIPlayerProvisioner{err: errors.New("password=should-not-leak")})
	defer authStore.Close()
	defer aiStore.Close()
	body, _ := json.Marshal(map[string]any{"character_name": "失败角色", "character_slot": 0, "personality": map[string]any{}, "goal": map[string]any{}, "skill_names": []string{"stoneage-play"}})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || strings.Contains(string(responseBody), "should-not-leak") || strings.Contains(string(responseBody), "password") {
		t.Fatalf("provision error response = %d %s", response.StatusCode, responseBody)
	}
	events, err := authStore.RecentAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event == "ai_profile_provision_failed" {
			found = true
			if strings.Contains(event.Detail, "should-not-leak") || strings.Contains(event.Detail, "password") {
				t.Fatalf("provision audit leaked credential material: %#v", event)
			}
		}
	}
	if !found {
		t.Fatalf("provision failure was not audited: %#v", events)
	}
}

func TestAIProfileProvisionUnavailableDoesNotEnableCreation(t *testing.T) {
	coreVersion := ""
	for _, skill := range aimcp.SkillCatalog() {
		if skill.Name == aiCoreSkillName {
			coreVersion = skill.Version
		}
	}
	if coreVersion == "" {
		t.Fatal("core skill missing from catalog")
	}
	authStore, aiStore, server, client, csrf := newAIProvisionHTTPFixture(t, nil)
	defer authStore.Close()
	defer aiStore.Close()
	page, err := client.Get(server.URL + "/ai/profiles")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(pageBody, []byte(`data-provisioning="false"`)) || !bytes.Contains(pageBody, []byte("创建服务尚未配置")) ||
		!bytes.Contains(pageBody, []byte(`name="skill_names"`)) || !bytes.Contains(pageBody, []byte(`value="stoneage-play"`)) ||
		!bytes.Contains(pageBody, []byte("核心游戏操作")) || !bytes.Contains(pageBody, []byte("v"+coreVersion)) ||
		!bytes.Contains(pageBody, []byte(`id="ai-account-id-field" hidden`)) || !bytes.Contains(pageBody, []byte(`name="account_username" maxlength="128" readonly`)) ||
		bytes.Contains(pageBody, []byte("skills_json")) || bytes.Contains(pageBody, []byte("parameters")) {
		t.Fatalf("unavailable provisioning page = %d %s", page.StatusCode, pageBody)
	}
	body, _ := json.Marshal(map[string]any{"character_name": "不可用", "character_slot": 0})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/profiles/provision", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || bytes.Contains(responseBody, []byte("password")) {
		t.Fatalf("unavailable provision status=%d body=%s", response.StatusCode, responseBody)
	}
}

func newAIProvisionHTTPFixture(t *testing.T, provisioner AIPlayerProvisioner, runtimes ...AIProfileRuntime) (*auth.Store, *airuntime.Store, *httptest.Server, *http.Client, string) {
	t.Helper()
	ctx := context.Background()
	authStore, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := authStore.Migrate(ctx); err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	if _, err := authStore.CreateAdmin(ctx, "admin", []byte("secret123")); err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	root := t.TempDir()
	aiStore, err := airuntime.OpenWithSecrets(filepath.Join(root, "state.db"), filepath.Join(root, "secrets"))
	if err != nil {
		_ = authStore.Close()
		t.Fatal(err)
	}
	aiSecrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatal(err)
	}
	var runtime AIProfileRuntime
	if len(runtimes) > 0 {
		runtime = runtimes[0]
	}
	control, err := NewServer(authStore, Options{AIStore: aiStore, AISecrets: aiSecrets, AIProvisioner: provisioner, AIRuntime: runtime, CSRFSecret: []byte("ai-provision-secret")})
	if err != nil {
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(control.Handler())
	clientJar, err := cookiejar.New(nil)
	if err != nil {
		server.Close()
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatal(err)
	}
	client := &http.Client{Jar: clientJar}
	login, err := client.PostForm(server.URL+"/login", map[string][]string{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		server.Close()
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatal(err)
	}
	login.Body.Close()
	page, err := client.Get(server.URL + "/ai/profiles")
	if err != nil {
		server.Close()
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	csrfMatch := regexp.MustCompile(`data-csrf="([^"]+)"`).FindSubmatch(pageBody)
	if page.StatusCode != http.StatusOK || len(csrfMatch) != 2 {
		server.Close()
		_ = authStore.Close()
		_ = aiStore.Close()
		t.Fatalf("AI profile page = %d %s", page.StatusCode, pageBody)
	}
	t.Cleanup(func() {
		server.Close()
		_ = authStore.Close()
		_ = aiStore.Close()
	})
	return authStore, aiStore, server, client, string(csrfMatch[1])
}
