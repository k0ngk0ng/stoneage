// Package aiplanner compiles evidence-backed StoneAge task definitions into
// executable automation plans.
//
// The compiler is deliberately a trust boundary. A task marked unverified,
// a stale evidence fingerprint, an unverified execution assertion, an
// unknown cost, or a condition which the automation package cannot validate
// cannot produce a plan.
package aiplanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var (
	ErrTaskDependency        = errors.New("aiplanner: invalid task dependency")
	ErrPreparationUnreviewed = errors.New("aiplanner: task preparation has not been reviewed")
	// ErrTaskNotFound means that the requested task ID is absent from the
	// immutable knowledge snapshot.
	ErrTaskNotFound = errors.New("aiplanner: task not found")
	// ErrTaskUnverified means that a definition is not approved for execution.
	ErrTaskUnverified = errors.New("aiplanner: task is not verified")
	// ErrFingerprintMismatch means that evidence or the knowledge snapshot is
	// stale relative to the requested plan.
	ErrFingerprintMismatch = errors.New("aiplanner: fingerprint mismatch")
	// ErrExecutionUnverified means that static evidence has not been followed
	// by an independent runtime verification.
	ErrExecutionUnverified = errors.New("aiplanner: execution is not verified")
	// ErrInvalidCost means that a plan could not establish a finite, known
	// spending bound for every step. Unlimited funding changes runtime
	// affordability, but never waives this compile-time quote check.
	ErrInvalidCost = errors.New("aiplanner: invalid or unknown cost")
	// ErrInvalidCondition means that a task condition cannot be represented by
	// automation.Condition.
	ErrInvalidCondition = errors.New("aiplanner: invalid condition")
	// ErrInvalidTaskOptions means that the caller supplied unusable execution
	// limits or identity.
	ErrInvalidTaskOptions = errors.New("aiplanner: invalid task options")
)

// Planner uses one immutable knowledge snapshot. The field is exported so a
// service can construct a planner in dependency wiring; callers should treat
// the snapshot as read-only after construction.
type Planner struct {
	Knowledge *aiknowledge.Knowledge
}

// New returns a planner over the supplied knowledge snapshot.
func New(knowledge *aiknowledge.Knowledge) *Planner {
	return &Planner{Knowledge: knowledge}
}

// TaskOptions controls the execution-specific fields which are not part of a
// reusable task definition. A zero MaximumSeconds uses the sum of the
// verified step timeouts. A zero MaximumDeaths permits no deaths, matching
// automation's conservative default.
type TaskOptions struct {
	CharacterID string
	// SelectedPetID binds reusable pet-level conditions which use the
	// $selected_pet placeholder. The resolved stable identity is copied into
	// the executable plan, including dependency entry guards.
	SelectedPetID  string
	MaximumSeconds int
	MaximumDeaths  int
	// ReserveGold is enforced for ordinary accounts. An authenticated
	// server-side unlimited-funds capability is evaluated by the automation
	// engine at runtime and cannot be enabled through this option.
	ReserveGold     int64
	OfflineContinue bool
	// KnowledgeFingerprint optionally fences compilation to a caller's
	// expected complete snapshot digest. Empty uses the planner snapshot.
	KnowledgeFingerprint string
	// EvidenceFingerprint is useful for a caller which has already verified
	// evidence bytes while the snapshot has no DataDir. When DataDir is set it
	// is still checked against the bytes read from that directory.
	EvidenceFingerprint string
}

// BuildTask compiles one verified knowledge task into an automation quest
// plan. It never downgrades an unverified task into a best-effort plan.
func (p *Planner) BuildTask(ctx context.Context, taskID string, options TaskOptions) (automation.Plan, error) {
	plan, err := p.buildSingleTask(ctx, taskID, options)
	if err != nil {
		return automation.Plan{}, err
	}
	order, err := p.Knowledge.TaskOrder(taskID)
	if err != nil {
		return automation.Plan{}, fmt.Errorf("%w: %v", ErrTaskDependency, err)
	}
	root := order[len(order)-1]
	direct := map[string]bool{}
	for _, id := range root.Dependencies {
		direct[id] = true
	}
	for _, task := range order[:len(order)-1] {
		dependencyOptions := options
		// A caller-provided root evidence hash cannot attest another task.
		dependencyOptions.EvidenceFingerprint = ""
		dependency, err := p.buildSingleTask(ctx, task.ID, dependencyOptions)
		if err != nil {
			return automation.Plan{}, fmt.Errorf("%w: %s: %w", ErrTaskDependency, task.ID, err)
		}
		if direct[task.ID] {
			// Only direct completion predicates are entry requirements. A
			// prerequisite can legitimately consume an ancestor's reward.
			plan.Preconditions = append(plan.Preconditions, dependency.Completion...)
		}
	}
	if err := plan.Validate(); err != nil {
		return automation.Plan{}, fmt.Errorf("%w: %v", ErrTaskDependency, err)
	}
	return plan, nil
}

// Each dependency executes through its own ordinary handle and budget. This
// compiler adds entry guards, never hidden paid steps to the requested task.
func (p *Planner) buildSingleTask(ctx context.Context, taskID string, options TaskOptions) (automation.Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextErr(ctx); err != nil {
		return automation.Plan{}, err
	}
	if p == nil || p.Knowledge == nil {
		return automation.Plan{}, fmt.Errorf("%w: knowledge snapshot is required", ErrInvalidTaskOptions)
	}
	if strings.TrimSpace(options.CharacterID) == "" {
		return automation.Plan{}, fmt.Errorf("%w: character ID is required", ErrInvalidTaskOptions)
	}
	if options.MaximumSeconds < 0 || options.MaximumDeaths < 0 || options.ReserveGold < 0 {
		return automation.Plan{}, fmt.Errorf("%w: execution limits and reserve must be non-negative", ErrInvalidTaskOptions)
	}
	task, ok := p.Knowledge.FindTask(taskID)
	if !ok {
		return automation.Plan{}, fmt.Errorf("%w: %q", ErrTaskNotFound, taskID)
	}
	if task.Status != aiknowledge.TaskVerified {
		return automation.Plan{}, fmt.Errorf("%w: %q has status %q", ErrTaskUnverified, task.ID, task.Status)
	}
	if !task.ExecutionVerified {
		return automation.Plan{}, fmt.Errorf("%w: %q", ErrExecutionUnverified, task.ID)
	}
	if !task.PreparationReviewed || strings.TrimSpace(task.PreparationNotes) == "" {
		return automation.Plan{}, fmt.Errorf("%w: %q; 核实攻略、人物/宠物等级和路线补给后再执行", ErrPreparationUnreviewed, task.ID)
	}
	if !task.EvidenceVerified {
		return automation.Plan{}, fmt.Errorf("%w: %q static evidence is not verified", ErrFingerprintMismatch, task.ID)
	}
	if err := verifyKnowledgeFingerprint(p.Knowledge, options.KnowledgeFingerprint); err != nil {
		return automation.Plan{}, err
	}
	if err := verifyTaskFingerprint(ctx, p.Knowledge, task, options.EvidenceFingerprint); err != nil {
		return automation.Plan{}, err
	}

	preconditions, err := convertPreconditions(task.Preconditions, options.SelectedPetID)
	if err != nil {
		return automation.Plan{}, err
	}
	completion, err := convertSuccessConditions(task.Success, options.SelectedPetID)
	if err != nil {
		return automation.Plan{}, err
	}
	steps, maximumCost, timeoutSeconds, err := convertSteps(task.Steps, options.SelectedPetID)
	if err != nil {
		return automation.Plan{}, err
	}
	budget, err := compileBudget(task, maximumCost)
	if err != nil {
		return automation.Plan{}, err
	}
	budget.Reserve = options.ReserveGold
	maximumSeconds := options.MaximumSeconds
	if maximumSeconds == 0 {
		maximumSeconds = timeoutSeconds
	}
	if maximumSeconds <= 0 {
		return automation.Plan{}, fmt.Errorf("%w: no positive execution limit", ErrInvalidTaskOptions)
	}

	plan := automation.Plan{
		ID:                task.ID,
		CharacterID:       strings.TrimSpace(options.CharacterID),
		Mode:              "quest",
		KnowledgeRevision: p.Knowledge.Fingerprint(),
		Title:             task.Name,
		Preconditions:     preconditions,
		Completion:        completion,
		Steps:             steps,
		Budget:            budget,
		MaximumSeconds:    maximumSeconds,
		MaximumDeaths:     options.MaximumDeaths,
		OfflineContinue:   options.OfflineContinue,
	}
	if err := plan.Validate(); err != nil {
		return automation.Plan{}, fmt.Errorf("%w: compiled plan: %v", ErrInvalidTaskOptions, err)
	}
	return plan, nil
}

func compileBudget(task aiknowledge.TaskDefinition, maximumCost int64) (automation.Budget, error) {
	b := task.Budget
	if b.GoldMin < 0 || b.GoldExpected < b.GoldMin || b.GoldMax < b.GoldExpected {
		return automation.Budget{}, fmt.Errorf("%w: budget ordering is invalid", ErrInvalidCost)
	}
	if len(b.Evidence) == 0 {
		return automation.Budget{}, fmt.Errorf("%w: budget evidence is missing", ErrInvalidCost)
	}
	if maximumCost > b.GoldMax {
		return automation.Budget{}, fmt.Errorf("%w: step maximum %d exceeds budget maximum %d", ErrInvalidCost, maximumCost, b.GoldMax)
	}
	return automation.Budget{
		Minimum:      b.GoldMin,
		ExpectedLow:  b.GoldExpected,
		ExpectedHigh: b.GoldMax,
		Reserve:      0,
		MaximumSpend: b.GoldMax,
		Known:        true,
	}, nil
}

func convertPreconditions(input []aiknowledge.Precondition, selectedPetID string) ([]automation.Condition, error) {
	result := make([]automation.Condition, 0, len(input))
	for index, precondition := range input {
		if precondition.Operator != "" {
			return nil, fmt.Errorf("%w: precondition %d uses operator", ErrInvalidCondition, index+1)
		}
		condition, err := convertCondition(precondition.Condition(), selectedPetID)
		if err != nil {
			return nil, fmt.Errorf("%w: precondition %d: %v", ErrInvalidCondition, index+1, err)
		}
		result = append(result, condition)
	}
	return result, nil
}

func convertSuccessConditions(input []aiknowledge.SuccessCondition, selectedPetID string) ([]automation.Condition, error) {
	result := make([]automation.Condition, 0, len(input))
	for index, success := range input {
		condition, err := convertCondition(success.MachineCondition, selectedPetID)
		if err != nil {
			return nil, fmt.Errorf("%w: success condition %d: %v", ErrInvalidCondition, index+1, err)
		}
		result = append(result, condition)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: task has no completion condition", ErrInvalidCondition)
	}
	return result, nil
}

func convertSteps(input []aiknowledge.TaskStep, selectedPetID string) ([]automation.Step, int64, int, error) {
	if len(input) == 0 {
		return nil, 0, 0, fmt.Errorf("%w: task has no steps", ErrInvalidTaskOptions)
	}
	result := make([]automation.Step, 0, len(input))
	seen := make(map[string]bool, len(input))
	var maximumCost int64
	var timeoutSeconds int
	for index, source := range input {
		if strings.TrimSpace(source.ID) == "" || seen[source.ID] {
			return nil, 0, 0, fmt.Errorf("%w: step %d has a duplicate or empty ID", ErrInvalidTaskOptions, index+1)
		}
		seen[source.ID] = true
		if strings.TrimSpace(source.Action.Skill) == "" || !isJSONObject(source.Action.Arguments) {
			return nil, 0, 0, fmt.Errorf("%w: step %q has invalid action", ErrInvalidTaskOptions, source.ID)
		}
		if source.TimeoutSeconds <= 0 || source.TimeoutSeconds > 3600 {
			return nil, 0, 0, fmt.Errorf("%w: step %q timeout is invalid", ErrInvalidTaskOptions, source.ID)
		}
		if !source.CostKnown || source.MaximumCost < 0 {
			return nil, 0, 0, fmt.Errorf("%w: step %q cost is not known", ErrInvalidCost, source.ID)
		}
		if maximumCost > math.MaxInt64-source.MaximumCost {
			return nil, 0, 0, fmt.Errorf("%w: step costs overflow", ErrInvalidCost)
		}
		maximumCost += source.MaximumCost
		if timeoutSeconds > math.MaxInt-source.TimeoutSeconds {
			return nil, 0, 0, fmt.Errorf("%w: step timeouts overflow", ErrInvalidTaskOptions)
		}
		timeoutSeconds += source.TimeoutSeconds
		preconditions, err := convertMachineConditions(source.Preconditions, selectedPetID)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%w: step %q preconditions: %v", ErrInvalidCondition, source.ID, err)
		}
		success, err := convertMachineConditions(source.SuccessConditions, selectedPetID)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%w: step %q success conditions: %v", ErrInvalidCondition, source.ID, err)
		}
		if len(success) == 0 {
			return nil, 0, 0, fmt.Errorf("%w: step %q has no success condition", ErrInvalidCondition, source.ID)
		}
		result = append(result, automation.Step{
			ID:             source.ID,
			Description:    source.Description,
			Action:         automation.Action{Skill: source.Action.Skill, Arguments: append(json.RawMessage(nil), source.Action.Arguments...)},
			Preconditions:  preconditions,
			Success:        success,
			TimeoutSeconds: source.TimeoutSeconds,
			MaximumCost:    source.MaximumCost,
			CostKnown:      true,
		})
	}
	return result, maximumCost, timeoutSeconds, nil
}

func convertMachineConditions(input []aiknowledge.MachineCondition, selectedPetID string) ([]automation.Condition, error) {
	result := make([]automation.Condition, 0, len(input))
	for index, source := range input {
		condition, err := convertCondition(source, selectedPetID)
		if err != nil {
			return nil, fmt.Errorf("condition %d: %v", index+1, err)
		}
		result = append(result, condition)
	}
	return result, nil
}

func convertCondition(source aiknowledge.MachineCondition, selectedPetID string) (automation.Condition, error) {
	// IDs beginning with '$' are reserved for compiler references. Resolve the
	// one supported reference before copying the condition into the plan and
	// reject every other reference instead of persisting an unresolved ID.
	if strings.HasPrefix(source.ID, "$") {
		if source.ID != "$selected_pet" {
			return automation.Condition{}, fmt.Errorf("unknown condition reference %q", source.ID)
		}
		if source.Kind != "pet_level" {
			return automation.Condition{}, fmt.Errorf("$selected_pet is only valid for pet_level conditions")
		}
		resolvedID, err := resolveSelectedPetID(selectedPetID)
		if err != nil {
			return automation.Condition{}, err
		}
		source.ID = resolvedID
	}
	if err := source.Validate(); err != nil {
		return automation.Condition{}, err
	}
	condition := automation.Condition{Kind: source.Kind, ID: source.ID, Value: source.Value, X: source.X, Y: source.Y}
	if err := condition.Validate(); err != nil {
		return automation.Condition{}, err
	}
	return condition, nil
}

func resolveSelectedPetID(selectedPetID string) (string, error) {
	if strings.TrimSpace(selectedPetID) == "" {
		return "", errors.New("$selected_pet requires a selected pet binding")
	}
	// A stable identity is copied byte-for-byte. Silently trimming it could
	// bind a different identity than the one the caller authenticated, and a
	// leading '$' would leave another unresolved compiler reference in the
	// persisted plan.
	if selectedPetID != strings.TrimSpace(selectedPetID) {
		return "", errors.New("selected pet binding must not have surrounding whitespace")
	}
	if strings.HasPrefix(selectedPetID, "$") {
		return "", fmt.Errorf("selected pet binding %q is not a concrete stable identity", selectedPetID)
	}
	return selectedPetID, nil
}

func isJSONObject(raw json.RawMessage) bool {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func verifyKnowledgeFingerprint(knowledge *aiknowledge.Knowledge, expected string) error {
	actual := strings.TrimSpace(knowledge.Fingerprint())
	if !validDigest(actual) {
		return fmt.Errorf("%w: knowledge fingerprint is invalid", ErrFingerprintMismatch)
	}
	if expected != "" && !strings.EqualFold(strings.TrimSpace(expected), actual) {
		return fmt.Errorf("%w: knowledge %q does not match expected %q", ErrFingerprintMismatch, actual, expected)
	}
	return nil
}

func verifyTaskFingerprint(ctx context.Context, knowledge *aiknowledge.Knowledge, task aiknowledge.TaskDefinition, expected string) error {
	if !validDigest(task.DataFingerprint) {
		return fmt.Errorf("%w: task data fingerprint is missing or malformed", ErrFingerprintMismatch)
	}
	if expected != "" && !strings.EqualFold(strings.TrimSpace(expected), task.DataFingerprint) {
		return fmt.Errorf("%w: task evidence does not match expected %q", ErrFingerprintMismatch, expected)
	}
	refs := taskEvidenceRefs(task)
	if len(refs) == 0 {
		return fmt.Errorf("%w: task evidence is empty", ErrFingerprintMismatch)
	}
	if strings.TrimSpace(knowledge.DataDir) == "" {
		if expected == "" {
			return fmt.Errorf("%w: evidence directory is unavailable", ErrFingerprintMismatch)
		}
		return nil
	}
	bytesByPath := make(map[string][]byte)
	for index, ref := range refs {
		if err := contextErr(ctx); err != nil {
			return err
		}
		clean, err := safeSourcePath(ref.Path)
		if err != nil {
			return fmt.Errorf("%w: evidence %d: %v", ErrFingerprintMismatch, index+1, err)
		}
		raw, err := os.ReadFile(filepathJoin(knowledge.DataDir, clean))
		if err != nil {
			return fmt.Errorf("%w: evidence %s: %v", ErrFingerprintMismatch, clean, err)
		}
		if !validDigest(ref.SHA256) || !strings.EqualFold(ref.SHA256, aiknowledge.SHA256Hex(raw)) {
			return fmt.Errorf("%w: evidence %s hash does not match", ErrFingerprintMismatch, clean)
		}
		bytesByPath[clean] = raw
	}
	actual := evidenceFingerprint(bytesByPath)
	if !strings.EqualFold(actual, task.DataFingerprint) {
		return fmt.Errorf("%w: task evidence %q does not match current %q", ErrFingerprintMismatch, task.DataFingerprint, actual)
	}
	return nil
}

func taskEvidenceRefs(task aiknowledge.TaskDefinition) []aiknowledge.SourceRef {
	refs := make([]aiknowledge.SourceRef, 0)
	for _, evidence := range task.Evidence {
		refs = append(refs, evidence.Source)
	}
	for _, precondition := range task.Preconditions {
		refs = append(refs, precondition.Evidence...)
	}
	for _, step := range task.Steps {
		refs = append(refs, step.Evidence...)
	}
	for _, success := range task.Success {
		refs = append(refs, success.Evidence...)
	}
	refs = append(refs, task.Budget.Evidence...)
	return refs
}

func evidenceFingerprint(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	input := make([]byte, 0)
	for _, path := range paths {
		input = append(input, path...)
		input = append(input, 0)
		input = append(input, files[path]...)
		input = append(input, 0)
	}
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeSourcePath(value string) (string, error) {
	value = filepathToSlash(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("source path %q must be relative", value)
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source path %q escapes data directory", value)
	}
	return clean, nil
}

// Small wrappers keep path handling in this package explicit and make it
// harder to accidentally use a platform-specific path as an evidence key.
func filepathToSlash(value string) string {
	return strings.ReplaceAll(value, string(os.PathSeparator), "/")
}
func filepathJoin(root, clean string) string {
	parts := strings.Split(clean, "/")
	result := root
	for _, part := range parts {
		if part != "" {
			result += string(os.PathSeparator) + part
		}
	}
	return result
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
