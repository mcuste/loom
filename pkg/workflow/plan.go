package workflow

import "slices"

// Plan returns the task ids in deterministic execution order. The order is a
// topological sort of the dependency graph; among tasks that become ready in
// the same step, ties are broken by declaration order in the YAML file (so
// reading a workflow top-to-bottom matches the order loom will execute it in).
//
// Plan assumes w has been produced by Parse, which guarantees the graph is a
// DAG. If w was constructed by hand and contains a cycle, Plan returns the
// partial order it managed to compute — callers running parse-validated
// workflows can ignore that edge case.
func (w *Workflow) Plan() []TaskID {
	pos := make(map[TaskID]int, len(w.Tasks))
	for i, t := range w.Tasks {
		pos[t.ID] = i
	}

	inDeg := make(map[TaskID]int, len(w.Tasks))
	adj := make(map[TaskID][]TaskID, len(w.Tasks))
	for _, t := range w.Tasks {
		inDeg[t.ID] = len(t.DependsOn)
		for _, d := range t.DependsOn {
			adj[d] = append(adj[d], t.ID)
		}
	}

	ready := make([]TaskID, 0, len(w.Tasks))
	for _, t := range w.Tasks {
		if inDeg[t.ID] == 0 {
			ready = append(ready, t.ID)
		}
	}

	order := make([]TaskID, 0, len(w.Tasks))
	for len(ready) > 0 {
		slices.SortFunc(ready, func(a, b TaskID) int { return pos[a] - pos[b] })
		u := ready[0]
		ready = ready[1:]
		order = append(order, u)
		for _, v := range adj[u] {
			inDeg[v]--
			if inDeg[v] == 0 {
				ready = append(ready, v)
			}
		}
	}
	return order
}

// ByID returns a pointer to the task with the given id, or nil if no such task
// exists. The returned pointer is valid for the lifetime of w.
func (w *Workflow) ByID(id TaskID) *Task {
	for i := range w.Tasks {
		if w.Tasks[i].ID == id {
			return &w.Tasks[i]
		}
	}
	return nil
}
