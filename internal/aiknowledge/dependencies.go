package aiknowledge

import "fmt"

// TaskOrder returns each prerequisite once, before its dependants, ending in
// the requested task. Definitions remain immutable; callers receive copies.
// Completion is never inferred from IDs or old execution receipts.
func (k *Knowledge) TaskOrder(id string) ([]TaskDefinition, error) {
	if k == nil {
		return nil, fmt.Errorf("task knowledge is unavailable")
	}
	index := make(map[string]TaskDefinition, len(k.TaskDefinitions))
	duplicates := map[string]bool{}
	for _, task := range k.TaskDefinitions {
		if _, exists := index[task.ID]; exists {
			duplicates[task.ID] = true
		}
		index[task.ID] = task
	}
	state := map[string]uint8{}
	var ordered []TaskDefinition
	var visit func(string) error
	visit = func(current string) error {
		if duplicates[current] {
			return fmt.Errorf("ambiguous task dependency %q", current)
		}
		if state[current] == 1 {
			return fmt.Errorf("cyclic task dependency at %q", current)
		}
		if state[current] == 2 {
			return nil
		}
		if len(state) >= 256 {
			return fmt.Errorf("task dependency graph exceeds 256 tasks")
		}
		task, ok := index[current]
		if !ok {
			return fmt.Errorf("missing task dependency %q", current)
		}
		if current != id {
			for _, condition := range task.Success {
				if condition.Kind == "window_submitted" {
					return fmt.Errorf("task dependency %q relies on a connection-local window submission; declare server-observable completion", current)
				}
			}
		}
		if err := validateTaskDependencies(task); err != nil {
			return err
		}
		state[current] = 1
		for _, dependency := range task.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[current] = 2
		ordered = append(ordered, cloneTask(task))
		return nil
	}
	if err := visit(id); err != nil {
		return nil, err
	}
	return ordered, nil
}

func validateTaskDependencies(task TaskDefinition) error {
	if len(task.Dependencies) > 64 {
		return fmt.Errorf("task %q has too many dependencies", task.ID)
	}
	seen := map[string]bool{}
	for _, id := range task.Dependencies {
		if !validTaskID(id) || id == task.ID || seen[id] {
			return fmt.Errorf("task %q has invalid or repeated dependency %q", task.ID, id)
		}
		seen[id] = true
	}
	return nil
}
