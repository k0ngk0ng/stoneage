package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
)

// TaskDirectory returns a server-owned, display-oriented view of the loaded
// task definitions. The returned summaries deliberately do not expose the
// executable actions or source references; start/preview still compile the
// selected definition against the same immutable snapshot.
func (executor *AutomationExecutor) TaskDirectory(ctx context.Context) (AutomationTaskDirectory, error) {
	if executor == nil || executor.config.Knowledge == nil {
		return AutomationTaskDirectory{}, errors.New("automation task directory is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AutomationTaskDirectory{}, err
	}
	knowledge := executor.config.Knowledge
	revision := strings.TrimSpace(knowledge.Fingerprint())
	if revision == "" {
		return AutomationTaskDirectory{}, errors.New("automation knowledge revision is unavailable")
	}

	tasks := knowledge.Tasks()
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].ID != tasks[j].ID {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].Name < tasks[j].Name
	})
	result := AutomationTaskDirectory{
		KnowledgeRevision: revision,
		Supplies:          aiservice.ReviewedStockOffers(knowledge, executor.config.StockItems),
		Tasks:             make([]AutomationTaskSummary, 0, len(tasks)),
	}
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return AutomationTaskDirectory{}, err
		}
		// A definition without an ID cannot be selected by either the planner
		// or the Web request, so it is an internal malformed entry rather than
		// a useful directory item.
		if strings.TrimSpace(task.ID) == "" {
			continue
		}
		result.Tasks = append(result.Tasks, summarizeAutomationTask(knowledge, task))
	}
	return result, nil
}

func summarizeAutomationTask(knowledge *aiknowledge.Knowledge, task aiknowledge.TaskDefinition) AutomationTaskSummary {
	order, orderErr := knowledge.TaskOrder(task.ID)
	blockers := taskReviewBlockers(task)
	if orderErr != nil {
		// Do not return the underlying error: it can include source paths or
		// other details which are useful to an operator but not to a browser.
		blockers = appendUniqueCatalogString(blockers, "依赖关系无法解析")
	}
	if orderErr == nil {
		for _, dependency := range order[:len(order)-1] {
			label := strings.TrimSpace(dependency.Name)
			if label == "" {
				label = dependency.ID
			}
			for _, blocker := range taskReviewBlockers(dependency) {
				blockers = appendUniqueCatalogString(blockers, "依赖任务 "+label+"："+blocker)
			}
		}
	}

	dependencies := make([]AutomationTaskDependency, 0, len(task.Dependencies))
	seenDependencies := make(map[string]struct{}, len(task.Dependencies))
	for _, id := range task.Dependencies {
		if _, seen := seenDependencies[id]; seen {
			continue
		}
		seenDependencies[id] = struct{}{}
		dependency := AutomationTaskDependency{ID: id}
		if value, ok := knowledge.FindTask(id); ok {
			dependency.Name = value.Name
		}
		dependencies = append(dependencies, dependency)
	}

	// A selected pet is a chain-level binding. A prerequisite may contain the
	// placeholder even when the requested task itself does not, so inspect the
	// complete valid dependency order when available.
	requiresPet := taskRequiresSelectedPet(task)
	if orderErr == nil {
		for _, candidate := range order {
			requiresPet = requiresPet || taskRequiresSelectedPet(candidate)
		}
	}

	return AutomationTaskSummary{
		ID:               task.ID,
		Name:             task.Name,
		Description:      task.Description,
		PreparationNotes: task.PreparationNotes,
		ReviewBlockers:   blockers,
		Requirements:     taskRequirements(task),
		Dependencies:     dependencies,
		RequiresPet:      requiresPet,
	}
}

func taskReviewBlockers(task aiknowledge.TaskDefinition) []string {
	blockers := make([]string, 0, 8)
	switch task.Status {
	case aiknowledge.TaskVerified:
	default:
		blockers = append(blockers, "任务尚未审核通过")
	}
	if !task.EvidenceVerified {
		blockers = append(blockers, "静态证据尚未核验")
	}
	if !task.ExecutionVerified {
		blockers = append(blockers, "独立运行验证尚未完成")
	}
	if !task.PreparationReviewed {
		blockers = append(blockers, "准备条件尚未审核")
	}
	if strings.TrimSpace(task.PreparationNotes) == "" {
		blockers = append(blockers, "缺少准备审核说明")
	}
	if !validCatalogDigest(task.DataFingerprint) {
		blockers = append(blockers, "任务证据指纹缺失或无效")
	}
	if len(task.Evidence) == 0 {
		blockers = append(blockers, "任务证据声明缺失")
	}
	if task.Budget.GoldMin < 0 || task.Budget.GoldExpected < task.Budget.GoldMin || task.Budget.GoldMax < task.Budget.GoldExpected || len(task.Budget.Evidence) == 0 {
		blockers = append(blockers, "任务费用范围尚未核验")
	}
	if len(task.Steps) == 0 || len(task.Success) == 0 {
		blockers = append(blockers, "任务执行定义不完整")
	}
	for _, step := range task.Steps {
		if !step.CostKnown || step.MaximumCost < 0 {
			blockers = appendUniqueCatalogString(blockers, "步骤费用尚未核验")
		}
		if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Action.Skill) == "" || len(step.SuccessConditions) == 0 {
			blockers = appendUniqueCatalogString(blockers, "步骤执行定义不完整")
		}
		for _, condition := range step.Preconditions {
			if !catalogConditionValid(condition) {
				blockers = appendUniqueCatalogString(blockers, "步骤机器条件无效")
			}
		}
		for _, condition := range step.SuccessConditions {
			if !catalogConditionValid(condition) {
				blockers = appendUniqueCatalogString(blockers, "步骤机器条件无效")
			}
		}
	}
	for _, precondition := range task.Preconditions {
		if !catalogConditionValid(precondition.Condition()) {
			blockers = appendUniqueCatalogString(blockers, "任务机器前置条件无效")
		}
	}
	for _, success := range task.Success {
		if !catalogConditionValid(success.MachineCondition) {
			blockers = appendUniqueCatalogString(blockers, "任务完成条件无效")
		}
	}
	return blockers
}

func catalogConditionValid(condition aiknowledge.MachineCondition) bool {
	if strings.HasPrefix(condition.ID, "$") && condition.ID != "$selected_pet" {
		return false
	}
	if condition.ID == "$selected_pet" && condition.Kind != "pet_level" {
		return false
	}
	return condition.Validate() == nil
}

func taskRequiresSelectedPet(task aiknowledge.TaskDefinition) bool {
	for _, precondition := range task.Preconditions {
		if precondition.Condition().Kind == "pet_level" && precondition.Condition().ID == "$selected_pet" {
			return true
		}
	}
	for _, condition := range task.Success {
		if condition.Kind == "pet_level" && condition.ID == "$selected_pet" {
			return true
		}
	}
	for _, step := range task.Steps {
		for _, condition := range append(append([]aiknowledge.MachineCondition{}, step.Preconditions...), step.SuccessConditions...) {
			if condition.Kind == "pet_level" && condition.ID == "$selected_pet" {
				return true
			}
		}
	}
	return false
}

func taskRequirements(task aiknowledge.TaskDefinition) []string {
	result := make([]string, 0)
	for _, precondition := range task.Preconditions {
		if requirement := catalogRequirement(precondition.Condition()); requirement != "" {
			result = appendUniqueCatalogString(result, requirement)
		}
	}
	for stepIndex, step := range task.Steps {
		for _, condition := range step.Preconditions {
			// A step condition describes a later checkpoint, so expose only
			// level gates here and label them as such. Position, inventory and
			// progress conditions may become true during the journey and are
			// not departure requirements.
			if condition.Kind != "character_level" && condition.Kind != "pet_level" {
				continue
			}
			if requirement := catalogRequirement(condition); requirement != "" {
				result = appendUniqueCatalogString(result, fmt.Sprintf("第%d步要求：%s", stepIndex+1, requirement))
			}
		}
	}
	return result
}

func catalogRequirement(condition aiknowledge.MachineCondition) string {
	switch condition.Kind {
	case "character_level":
		return fmt.Sprintf("人物等级至少 %d", condition.Value)
	case "pet_level":
		if condition.ID == "$selected_pet" {
			return fmt.Sprintf("所选宠物等级至少 %d", condition.Value)
		}
		return fmt.Sprintf("指定宠物等级至少 %d", condition.Value)
	case "backpack_free_slots":
		return fmt.Sprintf("至少保留 %d 个空背包格", condition.Value)
	case "gold_at_least":
		return fmt.Sprintf("至少准备 %d 石币", condition.Value)
	case "item_count":
		return "任务物品数量需满足服务端要求"
	case "item_absent":
		return "背包中不能有该任务物品"
	case "flag_set":
		if condition.ID == "party:solo" {
			return "需要单人状态"
		}
		return "任务进度需满足服务端要求"
	case "flag_clear":
		return "任务进度需满足服务端要求"
	case "character_skill_level":
		return "需要满足技能等级要求"
	}
	return ""
}

func appendUniqueCatalogString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func validCatalogDigest(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
