package workflow

// Waves groups the task ids into execution waves by topological level. A task's
// level is the length of the longest dependency path from any root, so tasks in
// the same wave share no edge and can run concurrently. The slices preserve
// declaration order within each wave, mirroring Plan's tie-break.
//
// Waves is pure: it reads the dependency graph (the same data Plan uses) and
// never mutates the workflow.
func (w *Workflow) Waves() [][]TaskID {
	if len(w.Tasks) == 0 {
		return nil
	}

	// Plan returns a topological order, so every task's dependencies are visited
	// before it: levels can be filled in a single forward pass.
	level := make(map[TaskID]int, len(w.Tasks))
	maxLevel := 0
	for _, id := range w.Plan() {
		lvl := 0
		for _, d := range w.ByID(id).DependsOn {
			if l := level[d] + 1; l > lvl {
				lvl = l
			}
		}
		level[id] = lvl
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	// Declaration order preserves the same tie-break as Plan.
	waves := make([][]TaskID, maxLevel+1)
	for _, t := range w.Tasks {
		waves[level[t.ID]] = append(waves[level[t.ID]], t.ID)
	}
	return waves
}
