package workflow

import "fmt"

// depsCtx bundles the per-task context shared by the dependency builders: the
// task id, the set of known task ids and declared params used to validate
// references, and the enclosing for_each loop variable ("" outside a loop
// body), which is exempt from the task-ref check because it is bound
// per-instance at run time rather than via the DAG.
type depsCtx struct {
	tid     TaskID
	known   map[TaskID]struct{}
	params  map[ParamName]struct{}
	loopVar string
}

// buildDeclaredDeps validates a task's explicit depends_on list: every entry
// must be a well-formed, known, non-duplicate task id. It returns the
// dependency slice in declaration order together with the set of ids seen, so
// callers can union in further edges (e.g. from with-value placeholders)
// without re-scanning.
func buildDeclaredDeps(dc depsCtx, declared []string) ([]TaskID, map[TaskID]struct{}, error) {
	deps := make([]TaskID, 0, len(declared))
	seen := make(map[TaskID]struct{}, len(declared))
	for _, raw := range declared {
		d, err := NewTaskID(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("task %q depends_on: %w", dc.tid, err)
		}
		if _, ok := dc.known[d]; !ok {
			return nil, nil, &UnknownDependencyError{Task: dc.tid, Dep: d}
		}
		if _, dup := seen[d]; dup {
			return nil, nil, &DuplicateDependencyError{Task: dc.tid, Dep: d}
		}
		seen[d] = struct{}{}
		deps = append(deps, d)
	}
	return deps, seen, nil
}

// buildDeps validates a task's depends_on list and checks that every
// {{x}} and {{params.x}} placeholder in its prompt is well-defined.
func buildDeps(dc depsCtx, declared []string, prompt string) ([]TaskID, error) {
	deps, seen, err := buildDeclaredDeps(dc, declared)
	if err != nil {
		return nil, err
	}
	rs := refScope(dc)
	if err := rs.resolveRefs(prompt, false, seen, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

// findCycle runs a DFS over the dependency graph and returns the first cycle
// it discovers.
func findCycle(wf *Workflow) ([]TaskID, bool) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[TaskID]int, len(wf.Tasks))
	adj := make(map[TaskID][]TaskID, len(wf.Tasks))
	for _, t := range wf.Tasks {
		adj[t.ID] = t.DependsOn
	}

	var stack []TaskID
	var cycle []TaskID

	var dfs func(TaskID) bool
	dfs = func(u TaskID) bool {
		color[u] = gray
		stack = append(stack, u)
		for _, v := range adj[u] {
			switch color[v] {
			case gray:
				for i, n := range stack {
					if n == v {
						cycle = append([]TaskID{}, stack[i:]...)
						cycle = append(cycle, v)
						return true
					}
				}
			case white:
				if dfs(v) {
					return true
				}
			}
		}
		color[u] = black
		stack = stack[:len(stack)-1]
		return false
	}

	for _, t := range wf.Tasks {
		if color[t.ID] == white && dfs(t.ID) {
			return cycle, true
		}
	}
	return nil, false
}
