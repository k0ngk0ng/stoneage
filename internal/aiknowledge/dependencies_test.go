package aiknowledge

import "testing"

func TestTaskOrderDAGAndIsolation(t *testing.T) {
	k := &Knowledge{TaskDefinitions: []TaskDefinition{
		{ID: "root", Dependencies: []string{"left", "right"}},
		{ID: "left", Dependencies: []string{"shared"}},
		{ID: "right", Dependencies: []string{"shared"}}, {ID: "shared"},
	}}
	order, err := k.TaskOrder("root")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"shared", "left", "right", "root"}
	if len(order) != len(want) {
		t.Fatalf("order=%+v", order)
	}
	for i, id := range want {
		if order[i].ID != id {
			t.Fatalf("order=%+v", order)
		}
	}
	order[3].Dependencies[0] = "changed"
	k.Tasks()[0].Dependencies[0] = "changed-again"
	if k.TaskDefinitions[0].Dependencies[0] != "left" {
		t.Fatal("task copy aliases immutable graph")
	}
}

func TestTaskOrderRejectsMalformedGraphs(t *testing.T) {
	for _, tasks := range [][]TaskDefinition{
		{{ID: "root", Dependencies: []string{"missing"}}},
		{{ID: "root", Dependencies: []string{"other"}}, {ID: "other", Dependencies: []string{"root"}}},
		{{ID: "root", Dependencies: []string{"root"}}},
		{{ID: "root", Dependencies: []string{"leaf", "leaf"}}, {ID: "leaf"}},
		{{ID: "root", Dependencies: []string{"leaf"}}, {ID: "leaf"}, {ID: "leaf"}},
	} {
		if _, err := (&Knowledge{TaskDefinitions: tasks}).TaskOrder("root"); err == nil {
			t.Fatalf("accepted %+v", tasks)
		}
	}
}

func TestLoadTasksMissingDependencyIsNotExecutable(t *testing.T) {
	data, dir := taskFixtureDirs(t)
	task := validTask()
	task.Dependencies = []string{"missing-task"}
	writeTaskJSON(t, dir, "dependency.json", task)
	if _, _, _, _, err := loadTasks(dir, data, nil, testNPC(), true); err == nil {
		t.Fatal("strict load accepted missing dependency")
	}
	tasks, _, _, issues, err := loadTasks(dir, data, nil, testNPC(), false)
	if err != nil || len(tasks) != 1 || tasks[0].Status != TaskUnsupported || !hasIssue(issues, "task_dependencies") {
		t.Fatalf("load=%+v %+v %v", tasks, issues, err)
	}
}

func TestTaskDependencyCannotUseConnectionLocalSubmissionAsCompletion(t *testing.T) {
	k := &Knowledge{TaskDefinitions: []TaskDefinition{
		{ID: "root", Dependencies: []string{"dialogue"}},
		{ID: "dialogue", Success: []SuccessCondition{{MachineCondition: MachineCondition{Kind: "window_submitted", ID: "npc", Value: 231}}}},
	}}
	if _, err := k.TaskOrder("root"); err == nil {
		t.Fatal("dependency accepted a non-recoverable local submission marker")
	}
	if _, err := k.TaskOrder("dialogue"); err != nil {
		t.Fatalf("standalone dialogue compatibility changed: %v", err)
	}
}
