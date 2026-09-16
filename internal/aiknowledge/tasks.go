package aiknowledge

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

// embeddedTasks is intentionally the only built-in task source. A task is
// executable only when its JSON contains machine conditions and complete
// action arguments; prose in a description is never compiled as a predicate.
//
//go:embed tasks/*.json
var embeddedTasks embed.FS

type taskInput struct {
	path string
	raw  []byte
}

// loadTasks reads the embedded definitions or an operator-supplied task
// directory. The returned taskBytes are kept separate from core data because
// they participate in the knowledge fingerprint under their task path.
func loadTasks(taskDir, dataDir string, _ []EnemyBase, npc NPCKnowledge, strict bool) ([]TaskDefinition, []FileDigest, map[string][]byte, []Issue, error) {
	inputs, err := collectTaskInputs(taskDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	tasks := make([]TaskDefinition, 0, len(inputs))
	files := make([]FileDigest, 0, len(inputs))
	contents := make(map[string][]byte, len(inputs))
	issues := make([]Issue, 0)
	seenIDs := make(map[string]int)
	for index, input := range inputs {
		contents[input.path] = append([]byte(nil), input.raw...)
		digest := FileDigest{
			Path:      input.path,
			SHA256:    SHA256Hex(input.raw),
			Bytes:     int64(len(input.raw)),
			Records:   1,
			Encoding:  detectEncoding(input.raw),
			Supported: true,
		}
		files = append(files, digest)

		task, decodeErr := decodeTask(input.raw, input.path)
		if decodeErr != nil {
			issue := taskIssue(input.path, "task_json", decodeErr.Error(), SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %w", input.path, decodeErr)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			tasks = append(tasks, TaskDefinition{Status: TaskUnsupported, Source: SourceRef{Path: input.path, SHA256: digest.SHA256, Extractor: "task-json"}})
			continue
		}
		if task.Source.Path == "" {
			task.Source.Path = input.path
		}
		// The source of a loaded definition is always the file that supplied it;
		// accepting a different path would make audit output ambiguous.
		if task.Source.Path != input.path {
			message := fmt.Sprintf("source.path must be %q", input.path)
			issue := taskIssue(input.path, "task_source", message, SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %s", input.path, message)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			task.Status = TaskUnsupported
		}
		task.Source.SHA256 = digest.SHA256
		task.Source.Extractor = "task-json"

		shapeErr := validateTaskShape(task, npc)
		if shapeErr != nil {
			issue := taskIssue(input.path, "task_schema", shapeErr.Error(), SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %w", input.path, shapeErr)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			task.Status = TaskUnsupported
		}

		dataOK, evidenceFingerprint, evidenceErr := verifyTaskEvidence(&task, dataDir)
		task.EvidenceVerified = dataOK
		if evidenceErr != nil {
			issue := taskIssue(input.path, "task_evidence", evidenceErr.Error(), SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %w", input.path, evidenceErr)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			task.Status = TaskUnsupported
		}
		if task.DataFingerprint != "" && !strings.EqualFold(task.DataFingerprint, evidenceFingerprint) {
			message := fmt.Sprintf("data_fingerprint %q does not match current evidence %q", task.DataFingerprint, evidenceFingerprint)
			issue := taskIssue(input.path, "task_fingerprint", message, SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %s", input.path, message)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			task.Status = TaskUnsupported
			task.EvidenceVerified = false
		}

		if task.Status == TaskVerified && (!task.EvidenceVerified || !task.ExecutionVerified || task.DataFingerprint == "" || !task.PreparationReviewed || strings.TrimSpace(task.PreparationNotes) == "") {
			message := "verified task requires matching data evidence, data_fingerprint, preparation_reviewed with notes, and independent execution_verified=true"
			issue := taskIssue(input.path, "task_not_verified", message, SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %s", input.path, message)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			// Current source facts may still be useful for planning, but the
			// definition cannot be presented as runtime-verified.
			task.Status = TaskUnverified
		}

		if previous, duplicate := seenIDs[task.ID]; duplicate && task.ID != "" {
			message := fmt.Sprintf("duplicate task id %q in definitions %d and %d", task.ID, previous+1, index+1)
			issue := taskIssue(input.path, "task_duplicate_id", message, SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), fmt.Errorf("%s: %s", input.path, message)
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			task.Status = TaskUnsupported
		} else if task.ID != "" {
			seenIDs[task.ID] = index
		}
		tasks = append(tasks, task)
	}

	graph := &Knowledge{TaskDefinitions: tasks}
	for i := range tasks {
		if tasks[i].ID == "" {
			continue
		}
		order, dependencyErr := graph.TaskOrder(tasks[i].ID)
		if dependencyErr != nil {
			issue := taskIssue(tasks[i].Source.Path, "task_dependencies", dependencyErr.Error(), SeverityError)
			if strict {
				return tasks, files, contents, append(issues, issue), dependencyErr
			}
			issue.Severity = SeverityWarning
			issues = append(issues, issue)
			tasks[i].Status = TaskUnsupported
			continue
		}
		if tasks[i].Status == TaskVerified {
			for _, dependency := range order[:len(order)-1] {
				if dependency.Status == TaskVerified {
					continue
				}
				message := fmt.Sprintf("verified task %q depends on non-executable task %q", tasks[i].ID, dependency.ID)
				issue := taskIssue(tasks[i].Source.Path, "task_dependency_unverified", message, SeverityError)
				if strict {
					return tasks, files, contents, append(issues, issue), fmt.Errorf("%s", message)
				}
				issue.Severity = SeverityWarning
				issues = append(issues, issue)
				tasks[i].Status = TaskUnverified
				break
			}
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return tasks, files, contents, issues, nil
}

func collectTaskInputs(taskDir string) ([]taskInput, error) {
	if strings.TrimSpace(taskDir) == "" {
		var paths []string
		err := fs.WalkDir(embeddedTasks, ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".json" {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("aiknowledge: walk embedded tasks: %w", err)
		}
		sort.Strings(paths)
		result := make([]taskInput, 0, len(paths))
		for _, path := range paths {
			raw, err := fs.ReadFile(embeddedTasks, path)
			if err != nil {
				return nil, fmt.Errorf("aiknowledge: read embedded task %s: %w", path, err)
			}
			result = append(result, taskInput{path: path, raw: raw})
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("aiknowledge: no embedded task definitions")
		}
		return result, nil
	}

	root := filepath.Clean(taskDir)
	fileInfo, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("aiknowledge: stat task directory %s: %w", root, err)
	}
	if !fileInfo.IsDir() {
		return nil, fmt.Errorf("aiknowledge: task path %s is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aiknowledge: walk task directory %s: %w", root, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("aiknowledge: task directory %s has no JSON definitions", root)
	}
	result := make([]taskInput, 0, len(paths))
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("aiknowledge: read task %s: %w", rel, err)
		}
		result = append(result, taskInput{path: "tasks/" + rel, raw: raw})
	}
	return result, nil
}

func decodeTask(raw []byte, path string) (TaskDefinition, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var task TaskDefinition
	if err := decoder.Decode(&task); err != nil {
		return TaskDefinition{}, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return TaskDefinition{}, fmt.Errorf("multiple JSON values")
		}
		return TaskDefinition{}, fmt.Errorf("trailing JSON: %w", err)
	}
	if task.Source.Path == "" {
		task.Source.Path = path
	}
	return task, nil
}

func validateTaskShape(task TaskDefinition, npc NPCKnowledge) error {
	if err := validateTaskDependencies(task); err != nil {
		return err
	}
	if !validTaskID(task.ID) {
		return fmt.Errorf("id must contain lowercase letters, digits, '.', '_' or '-' and start with a letter or digit")
	}
	if strings.TrimSpace(task.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch task.Status {
	case TaskVerified, TaskUnverified, TaskUnsupported:
	default:
		return fmt.Errorf("unknown status %q", task.Status)
	}
	if len(task.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	if len(task.Success) == 0 {
		return fmt.Errorf("at least one task success condition is required")
	}
	if task.Budget.GoldMin < 0 || task.Budget.GoldExpected < task.Budget.GoldMin || task.Budget.GoldMax < task.Budget.GoldExpected {
		return fmt.Errorf("budget must satisfy 0 <= gold_min <= gold_expected <= gold_max")
	}
	if len(task.Evidence) == 0 {
		return fmt.Errorf("at least one top-level evidence claim is required")
	}
	for index, precondition := range task.Preconditions {
		condition := precondition.Condition()
		if precondition.Operator != "" {
			return fmt.Errorf("precondition %d uses operator; use a machine condition with executor semantics", index+1)
		}
		if err := condition.Validate(); err != nil {
			return fmt.Errorf("precondition %d: %w", index+1, err)
		}
		if len(precondition.Evidence) == 0 {
			return fmt.Errorf("precondition %d needs evidence", index+1)
		}
	}
	for index, success := range task.Success {
		if err := success.MachineCondition.Validate(); err != nil {
			return fmt.Errorf("task success %d: %w", index+1, err)
		}
		if strings.TrimSpace(success.Expression) == "" && strings.TrimSpace(success.Description) == "" {
			return fmt.Errorf("task success %d needs a description or expression", index+1)
		}
		if len(success.Evidence) == 0 {
			return fmt.Errorf("task success %d needs evidence", index+1)
		}
	}
	stepIDs := make(map[string]bool, len(task.Steps))
	for index, step := range task.Steps {
		if !validTaskID(step.ID) {
			return fmt.Errorf("step %d has invalid id", index+1)
		}
		if stepIDs[step.ID] {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		stepIDs[step.ID] = true
		if !allowedStepKind(step.Kind) {
			return fmt.Errorf("step %q has unsupported kind %q", step.ID, step.Kind)
		}
		if step.Action.Skill == "" {
			return fmt.Errorf("step %q has no action skill", step.ID)
		}
		if err := validateAction(step); err != nil {
			return fmt.Errorf("step %q: %w", step.ID, err)
		}
		if step.TimeoutSeconds <= 0 || step.TimeoutSeconds > 3600 {
			return fmt.Errorf("step %q timeout_seconds must be between 1 and 3600", step.ID)
		}
		if step.MaximumCost < 0 {
			return fmt.Errorf("step %q maximum_cost cannot be negative", step.ID)
		}
		if len(step.SuccessConditions) == 0 {
			return fmt.Errorf("step %q needs success_conditions; success prose is not executable", step.ID)
		}
		for conditionIndex, condition := range step.Preconditions {
			if err := condition.Validate(); err != nil {
				return fmt.Errorf("step %q precondition %d: %w", step.ID, conditionIndex+1, err)
			}
		}
		for conditionIndex, condition := range step.SuccessConditions {
			if err := condition.Validate(); err != nil {
				return fmt.Errorf("step %q success condition %d: %w", step.ID, conditionIndex+1, err)
			}
		}
		if len(step.Evidence) == 0 {
			return fmt.Errorf("step %q needs evidence", step.ID)
		}
		if step.NPC != "" && !npcNameKnown(step.NPC, npc) {
			return fmt.Errorf("step %q references unknown NPC %q", step.ID, step.NPC)
		}
		if step.Floor < 0 {
			return fmt.Errorf("step %q floor cannot be negative", step.ID)
		}
		if step.Coordinates != nil && (step.Coordinates.Floor < 0 || step.Coordinates.X < 0 || step.Coordinates.Y < 0) {
			return fmt.Errorf("step %q coordinates must be non-negative", step.ID)
		}
	}
	return nil
}

func validTaskID(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			if index == 0 && (character == '.' || character == '_' || character == '-') {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func allowedStepKind(kind string) bool {
	switch kind {
	case "move", "talk", "select", "confirm", "heal", "heal_item", "stock", "recover", "battle", "give_item", "observe":
		return true
	default:
		return false
	}
}

func validateAction(step TaskStep) error {
	var arguments map[string]json.RawMessage
	if len(bytes.TrimSpace(step.Action.Arguments)) == 0 {
		return fmt.Errorf("action arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(step.Action.Arguments))
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		if err == nil {
			return fmt.Errorf("action arguments must be a JSON object")
		}
		return fmt.Errorf("action arguments must be a JSON object: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("action arguments contain trailing JSON")
	}
	require := func(names ...string) error {
		for _, name := range names {
			if _, ok := arguments[name]; !ok {
				return fmt.Errorf("action argument %q is required", name)
			}
		}
		return nil
	}
	rejectUnknown := func(names ...string) error {
		allowed := make(map[string]struct{}, len(names))
		for _, name := range names {
			allowed[name] = struct{}{}
		}
		for name := range arguments {
			if _, ok := allowed[name]; !ok {
				return fmt.Errorf("action argument %q is not supported", name)
			}
		}
		return nil
	}
	integerArgument := func(name string) error {
		raw, ok := arguments[name]
		if !ok {
			return fmt.Errorf("action argument %q is required", name)
		}
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
			return fmt.Errorf("action argument %q must be a non-negative integer", name)
		}
		return nil
	}
	textArgument := func(name string) error {
		raw, ok := arguments[name]
		if !ok {
			return fmt.Errorf("action argument %q is required", name)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("action argument %q must be a non-empty string", name)
		}
		return nil
	}
	skill := strings.ToLower(strings.TrimSpace(step.Action.Skill))
	switch step.Kind {
	case "stock":
		if skill != "item.stock" {
			return fmt.Errorf("stock step must use item.stock skill")
		}
		if err := rejectUnknown("item", "target_count", "reserve_slots"); err != nil {
			return err
		}
		if err := textArgument("item"); err != nil {
			return err
		}
		var target int
		if err := json.Unmarshal(arguments["target_count"], &target); err != nil || target < 1 || target > 15 {
			return fmt.Errorf("stock target_count must be an integer from 1 to 15")
		}
		if raw, exists := arguments["reserve_slots"]; exists {
			var reserve int
			if err := json.Unmarshal(raw, &reserve); err != nil || string(raw) == "null" || reserve < 0 || reserve > 15-target {
				return fmt.Errorf("stock reserve_slots must be an integer from 0 to 15 minus target_count")
			}
		}
		return nil
	case "heal_item":
		if skill != "item.heal" {
			return fmt.Errorf("heal_item step must use item.heal skill")
		}
		if err := rejectUnknown("item"); err != nil {
			return err
		}
		return textArgument("item")
	case "recover":
		if skill != "npc.recover" {
			return fmt.Errorf("recover step must use npc.recover skill")
		}
		return rejectUnknown()
	case "heal":
		if skill != "npc.heal" {
			return fmt.Errorf("heal step must use npc.heal skill")
		}
		if err := rejectUnknown("npc"); err != nil {
			return err
		}
		return textArgument("npc")
	case "move":
		if skill != "move" {
			return fmt.Errorf("move step must use move skill")
		}
		for _, name := range []string{"floor", "x", "y"} {
			if err := integerArgument(name); err != nil {
				return err
			}
		}
		return nil
	case "talk":
		if skill != "talk" && skill != "npc.talk" {
			return fmt.Errorf("talk step must use talk or npc.talk skill")
		}
		if err := textArgument("npc"); err != nil {
			return err
		}
		for _, name := range []string{"floor", "x", "y"} {
			if err := integerArgument(name); err != nil {
				return err
			}
		}
		return nil
	case "select", "confirm":
		if skill != "window" && skill != "npc.window" {
			return fmt.Errorf("%s step must use window or npc.window skill", step.Kind)
		}
		if err := rejectUnknown("npc", "window_sequence", "choice", "window_type", "window_object_id"); err != nil {
			return err
		}
		if err := textArgument("npc"); err != nil {
			return err
		}
		if err := integerArgument("window_sequence"); err != nil {
			return err
		}
		return require("choice")
	case "battle", "give_item", "observe":
		// These skills still need an explicit JSON object, but their schemas are
		// installed with the executor and are intentionally not guessed here.
		return nil
	}
	return fmt.Errorf("unsupported action kind %q", step.Kind)
}

func npcNameKnown(name string, npc NPCKnowledge) bool {
	for _, template := range npc.Templates {
		if template.Name == name {
			return true
		}
	}
	for _, create := range npc.Creates {
		for _, enemy := range create.Enemies {
			if enemy.Template == name {
				return true
			}
		}
	}
	return false
}

func verifyTaskEvidence(task *TaskDefinition, dataDir string) (bool, string, error) {
	refs := make([]SourceRef, 0)
	for _, evidence := range task.Evidence {
		if strings.TrimSpace(evidence.Claim) == "" {
			return false, "", fmt.Errorf("top-level evidence claim is empty")
		}
		if !evidence.Verified {
			// This is a deliberate author assertion, not a parse failure. The
			// source is still checked below so stale evidence cannot pass.
		}
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

	allHashes := true
	allClaimsVerified := true
	fileBytes := make(map[string][]byte)
	for _, evidence := range task.Evidence {
		if !evidence.Verified {
			allClaimsVerified = false
		}
	}
	for _, ref := range refs {
		clean, err := validateSourcePath(ref.Path)
		if err != nil {
			return false, "", err
		}
		raw, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(clean)))
		if err != nil {
			return false, "", fmt.Errorf("source %s: %w", clean, err)
		}
		fileBytes[clean] = raw
		if ref.Line < 0 || ref.LineEnd < 0 || (ref.LineEnd > 0 && ref.Line > ref.LineEnd) {
			return false, "", fmt.Errorf("source %s has invalid line range", clean)
		}
		lineCount := bytes.Count(raw, []byte{'\n'}) + 1
		if ref.Line > lineCount || ref.LineEnd > lineCount {
			return false, "", fmt.Errorf("source %s line range exceeds file (%d lines)", clean, lineCount)
		}
		if ref.SHA256 == "" {
			allHashes = false
			continue
		}
		if len(ref.SHA256) != hex.EncodedLen(sha256Size) {
			return false, "", fmt.Errorf("source %s has invalid sha256", clean)
		}
		if _, err := hex.DecodeString(ref.SHA256); err != nil || !strings.EqualFold(ref.SHA256, SHA256Hex(raw)) {
			return false, "", fmt.Errorf("source %s sha256 does not match loaded data", clean)
		}
	}
	fingerprint := evidenceFingerprint(fileBytes)
	return allHashes && allClaimsVerified, fingerprint, nil
}

const sha256Size = 32

func validateSourcePath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("source path %q must be a relative slash path", value)
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source path %q escapes data directory", value)
	}
	return clean, nil
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
	return SHA256Hex(input)
}

func taskIssue(path, code, message string, severity IssueSeverity) Issue {
	return Issue{Severity: severity, Code: code, Message: message, Source: &SourceRef{Path: path, Extractor: "task-json"}}
}
