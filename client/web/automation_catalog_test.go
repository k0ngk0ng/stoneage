package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func TestAutomationTaskDirectorySummarizesReviewAndRequirements(t *testing.T) {
	knowledge := catalogKnowledgeFixture()
	executor := &AutomationExecutor{config: AutomationExecutorConfig{Knowledge: knowledge}}
	directory, err := executor.TaskDirectory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if directory.KnowledgeRevision != knowledge.Fingerprint() || len(directory.Tasks) != 2 {
		t.Fatalf("unexpected task directory: %+v", directory)
	}
	if directory.Tasks[0].ID != "dep" || directory.Tasks[1].ID != "root" {
		t.Fatalf("task directory is not deterministic: %+v", directory.Tasks)
	}
	root := directory.Tasks[1]
	if root.Description != "A reviewed root task." || root.PreparationNotes != "Reviewed route and supplies." {
		t.Fatalf("task display metadata missing: %+v", root)
	}
	if !root.RequiresPet {
		t.Fatal("root dependency graph did not advertise its selected-pet requirement")
	}
	if len(root.Dependencies) != 1 || root.Dependencies[0] != (AutomationTaskDependency{ID: "dep", Name: "Dependency task"}) {
		t.Fatalf("dependency summary is incomplete: %+v", root.Dependencies)
	}
	if !containsCatalogString(root.Requirements, "人物等级至少 12") || !containsCatalogString(root.Requirements, "所选宠物等级至少 8") {
		t.Fatalf("level requirements are not human-readable: %+v", root.Requirements)
	}
	if !containsCatalogString(root.ReviewBlockers, "依赖任务 Dependency task：任务尚未审核通过") ||
		!containsCatalogString(root.ReviewBlockers, "依赖任务 Dependency task：静态证据尚未核验") ||
		!containsCatalogString(root.ReviewBlockers, "依赖任务 Dependency task：独立运行验证尚未完成") {
		t.Fatalf("dependency review blockers are incomplete: %+v", root.ReviewBlockers)
	}
	if len(root.ReviewBlockers) == 0 || root.Requirements == nil || root.Dependencies == nil {
		t.Fatal("directory used null arrays for UI collections")
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "facts.conf") || strings.Contains(string(raw), "observe") || strings.Contains(string(raw), "action") {
		t.Fatalf("task directory leaked internal source/action data: %s", raw)
	}
}

func TestAutomationCatalogDescribesItemAbsentRequirement(t *testing.T) {
	condition := aiknowledge.MachineCondition{Kind: "item_absent", ID: "item:2417"}
	if !catalogConditionValid(condition) {
		t.Fatal("item_absent condition was not accepted by the catalog validator")
	}
	if got := catalogRequirement(condition); got != "背包中不能有该任务物品" {
		t.Fatalf("item_absent requirement text=%q", got)
	}
}

func TestAutomationTaskDirectoryReportsInvalidGraphAndUnknownCost(t *testing.T) {
	knowledge := catalogKnowledgeFixture()
	root := knowledge.TaskDefinitions[1]
	root.Dependencies = []string{"missing"}
	root.Steps[0].CostKnown = false
	root.Budget.Evidence = nil
	knowledge.TaskDefinitions[1] = root
	executor := &AutomationExecutor{config: AutomationExecutorConfig{Knowledge: knowledge}}
	directory, err := executor.TaskDirectory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary AutomationTaskSummary
	for _, candidate := range directory.Tasks {
		if candidate.ID == "root" {
			summary = candidate
		}
	}
	if !containsCatalogString(summary.ReviewBlockers, "依赖关系无法解析") ||
		!containsCatalogString(summary.ReviewBlockers, "步骤费用尚未核验") ||
		!containsCatalogString(summary.ReviewBlockers, "任务费用范围尚未核验") {
		t.Fatalf("directory omitted obvious review blockers: %+v", summary.ReviewBlockers)
	}
	if len(summary.Dependencies) != 1 || summary.Dependencies[0].ID != "missing" || summary.Dependencies[0].Name != "" {
		t.Fatalf("missing dependency was not explained safely: %+v", summary.Dependencies)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.TaskDirectory(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled catalog request returned %v", err)
	}
}

func TestAutomationTaskDirectoryPropagatesDependencyPetRequirement(t *testing.T) {
	knowledge := catalogKnowledgeFixture()
	root, ok := knowledge.FindTask("root")
	if !ok {
		t.Fatal("root fixture task is missing")
	}
	dependency, ok := knowledge.FindTask("dep")
	if !ok {
		t.Fatal("dependency fixture task is missing")
	}
	root.Preconditions = filterCatalogPetPlaceholders(root.Preconditions)
	root.Steps[0].Preconditions = []aiknowledge.MachineCondition{
		{Kind: "item_count", ID: "item:quest", Value: 1},
		{Kind: "position", Value: 1000, X: 2, Y: 3},
		{Kind: "character_level", Value: 9},
	}
	dependency.Steps[0].SuccessConditions = append(dependency.Steps[0].SuccessConditions,
		aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8},
	)
	knowledge.TaskDefinitions[1] = root
	knowledge.TaskDefinitions[0] = dependency
	if taskRequiresSelectedPet(root) {
		t.Fatal("root fixture unexpectedly retained a selected-pet placeholder")
	}

	executor := &AutomationExecutor{config: AutomationExecutorConfig{Knowledge: knowledge}}
	directory, err := executor.TaskDirectory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary AutomationTaskSummary
	for _, candidate := range directory.Tasks {
		if candidate.ID == "root" {
			summary = candidate
		}
	}
	if !summary.RequiresPet {
		t.Fatal("dependency-only selected-pet requirement was not propagated")
	}
	if containsCatalogString(summary.Requirements, "所选宠物等级至少 8") {
		t.Fatalf("dependency completion was presented as a root departure requirement: %+v", summary.Requirements)
	}
	if !containsCatalogString(summary.Requirements, "第1步要求：人物等级至少 9") {
		t.Fatalf("step level gate was not labeled: %+v", summary.Requirements)
	}
	for _, requirement := range summary.Requirements {
		if strings.Contains(requirement, "item:quest") || strings.Contains(requirement, "地图 1000") {
			t.Fatalf("later step condition was presented as an opaque departure requirement: %+v", summary.Requirements)
		}
	}

	var dependencySummary AutomationTaskSummary
	for _, candidate := range directory.Tasks {
		if candidate.ID == "dep" {
			dependencySummary = candidate
		}
	}
	if !dependencySummary.RequiresPet {
		t.Fatal("dependency selected-pet requirement was not reported")
	}
}

func TestAutomationTaskDirectoryHTTPRouteIsSessionScopedAndGETOnly(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	left, right := net.Pipe()
	session := newTCPSession("catalog-session", left, 64*1024)
	if err := handler.sessions.add(session); err != nil {
		_ = right.Close()
		t.Fatal(err)
	}
	defer right.Close()
	knowledge := catalogKnowledgeFixture()
	directory := AutomationTaskDirectory{KnowledgeRevision: knowledge.Fingerprint(), Tasks: []AutomationTaskSummary{{ID: "root", Name: "Root", ReviewBlockers: []string{}, Requirements: []string{}, Dependencies: []AutomationTaskDependency{}}}}
	handler.SetAutomation(catalogAutomationAdapter{directory: directory})

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/catalog-session/automation/tasks", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog GET status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var got AutomationTaskDirectory
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.KnowledgeRevision != directory.KnowledgeRevision || len(got.Tasks) != 1 || got.Tasks[0].ID != "root" {
		t.Fatalf("unexpected catalog response: %+v", got)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/sessions/catalog-session/automation/tasks", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
		t.Fatalf("catalog POST status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions/unknown-session/automation/tasks", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown session catalog status=%d", response.Code)
	}

	handler.SetAutomation(nil)
	request = httptest.NewRequest(http.MethodGet, "/api/sessions/catalog-session/automation/tasks", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured catalog status=%d", response.Code)
	}
}

type catalogAutomationAdapter struct {
	directory AutomationTaskDirectory
}

func (catalogAutomationAdapter) Start(context.Context, *AutomationSession, AutomationStartRequest) (AutomationHandle, error) {
	return nil, errors.New("catalog test adapter does not start runs")
}

func (adapter catalogAutomationAdapter) TaskDirectory(context.Context) (AutomationTaskDirectory, error) {
	return adapter.directory, nil
}

func catalogKnowledgeFixture() *aiknowledge.Knowledge {
	ref := aiknowledge.SourceRef{Path: "facts.conf", SHA256: strings.Repeat("a", 64)}
	condition := aiknowledge.MachineCondition{Kind: "alive"}
	base := aiknowledge.TaskDefinition{
		Name:                "Dependency task",
		Status:              aiknowledge.TaskVerified,
		EvidenceVerified:    true,
		ExecutionVerified:   true,
		PreparationReviewed: true,
		PreparationNotes:    "Reviewed dependency.",
		DataFingerprint:     strings.Repeat("a", 64),
		Preconditions:       []aiknowledge.Precondition{{MachineCondition: condition, Evidence: []aiknowledge.SourceRef{ref}}},
		Steps: []aiknowledge.TaskStep{{
			ID: "step", Action: aiknowledge.TaskAction{Skill: "observe", Arguments: json.RawMessage(`{}`)},
			SuccessConditions: []aiknowledge.MachineCondition{condition}, TimeoutSeconds: 10, CostKnown: true,
			MaximumCost: 0, Evidence: []aiknowledge.SourceRef{ref},
		}},
		Success:  []aiknowledge.SuccessCondition{{MachineCondition: condition, Description: "done", Evidence: []aiknowledge.SourceRef{ref}}},
		Budget:   aiknowledge.Budget{Evidence: []aiknowledge.SourceRef{ref}},
		Evidence: []aiknowledge.Evidence{{Source: ref, Claim: "fixture", Verified: true}},
	}
	dependency := base
	dependency.ID = "dep"
	root := base
	root.ID = "root"
	root.Name = "Root task"
	root.Description = "A reviewed root task."
	root.PreparationNotes = "Reviewed route and supplies."
	root.Dependencies = []string{"dep"}
	root.Preconditions = append(root.Preconditions,
		aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "character_level", Value: 12}, Evidence: []aiknowledge.SourceRef{ref}},
		aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 8}, Evidence: []aiknowledge.SourceRef{ref}},
	)
	dependency.Status = aiknowledge.TaskUnverified
	dependency.EvidenceVerified = false
	dependency.ExecutionVerified = false
	dependency.PreparationReviewed = false
	dependency.PreparationNotes = ""
	dependency.DataFingerprint = ""
	dependency.Evidence = nil
	dependency.Budget.Evidence = nil
	return &aiknowledge.Knowledge{Digest: strings.Repeat("b", 64), TaskDefinitions: []aiknowledge.TaskDefinition{dependency, root}}
}

func filterCatalogPetPlaceholders(input []aiknowledge.Precondition) []aiknowledge.Precondition {
	result := make([]aiknowledge.Precondition, 0, len(input))
	for _, precondition := range input {
		condition := precondition.Condition()
		if condition.Kind == "pet_level" && condition.ID == "$selected_pet" {
			continue
		}
		result = append(result, precondition)
	}
	return result
}

func containsCatalogString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var _ Automation = catalogAutomationAdapter{}
var _ automationTaskDirectoryProvider = catalogAutomationAdapter{}
