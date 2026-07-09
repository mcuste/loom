package workflow

import "slices"

// Plan returns the task ids in deterministic execution order. The order is a
// topological sort of the dependency graph; among tasks that become ready in
// the same step, ties are broken by declaration order in the YAML file (so
// reading a workflow top-to-bottom matches the order loom will execute it in).
//
// Plan assumes w has been produced by Parse, which guarantees the graph is a
// DAG. If a cycle is present (hand-built Workflow only), Plan panics: the
// invariant is violated and there is no defensible partial-order behavior.
func (w *Workflow) Plan() []TaskID {
	tasks := make([]planTask, len(w.Tasks))
	for i, task := range w.Tasks {
		tasks[i] = planTask{id: task.ID, deps: append([]TaskID(nil), task.DependsOn...)}
	}
	return planTaskOrder(tasks)
}

func planDefinition(def WorkflowDefinition) []TaskID {
	nodes := definitionTaskNodes(def)
	tasks := make([]planTask, len(nodes))
	for i, task := range nodes {
		deps := make([]TaskID, len(task.DependsOn))
		for j, dep := range task.DependsOn {
			deps[j] = TaskID(dep)
		}
		tasks[i] = planTask{id: task.ID, deps: deps}
	}
	return planTaskOrder(tasks)
}

type planTask struct {
	id   TaskID
	deps []TaskID
}

func planTaskOrder(tasks []planTask) []TaskID {
	pos := make(map[TaskID]int, len(tasks))
	for i, task := range tasks {
		pos[task.id] = i
	}

	// Reverse dependents-edges (dependency -> dependent) plus in-degrees so
	// Kahn's algorithm can release a task once all its dependencies are done.
	// This is the OPPOSITE direction from findCycle's forward depends-on edges;
	// the two are kept separate on purpose, so no shared builder.
	inDeg := make(map[TaskID]int, len(tasks))
	adj := make(map[TaskID][]TaskID, len(tasks))
	for _, task := range tasks {
		inDeg[task.id] = len(task.deps)
		for _, dep := range task.deps {
			adj[dep] = append(adj[dep], task.id)
		}
	}

	cmpPos := func(a, b TaskID) int { return pos[a] - pos[b] }

	ready := make([]TaskID, 0, len(tasks))
	for _, task := range tasks {
		if inDeg[task.id] == 0 {
			ready = append(ready, task.id)
		}
	}
	slices.SortFunc(ready, cmpPos)

	order := make([]TaskID, 0, len(tasks))
	for len(ready) > 0 {
		u := ready[0]
		ready = ready[1:]
		order = append(order, u)
		for _, v := range adj[u] {
			inDeg[v]--
			if inDeg[v] == 0 {
				i, found := slices.BinarySearchFunc(ready, v, cmpPos)
				if !found {
					ready = slices.Insert(ready, i, v)
				}
			}
		}
	}
	if len(order) != len(tasks) {
		panic("workflow.Plan: cycle detected; Workflow must be produced by Parse")
	}
	return order
}

// ByID returns a pointer to the task with the given id, or nil if no such task
// exists. The returned pointer is valid for the lifetime of w.
//
// For workflows produced by Parse this is O(1); for hand-constructed workflows
// (no internal index) it falls back to a linear scan over Tasks.
func (w *Workflow) ByID(id TaskID) *Task {
	if w.byID != nil {
		if i, ok := w.byID[id]; ok {
			return &w.Tasks[i]
		}
		return nil
	}
	for i := range w.Tasks {
		if w.Tasks[i].ID == id {
			return &w.Tasks[i]
		}
	}
	return nil
}
