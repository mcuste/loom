package workflow

// PlanEntry annotates one task in the flow-ordered execution plan with the
// data renderers need without re-deriving the wave structure.
type PlanEntry struct {
	// ID is the task identifier.
	ID TaskID
	// Wave is the 0-based topological wave index. Tasks sharing a wave have
	// no dependency edge and can run concurrently.
	Wave int
	// LastInWave is true when this task is the last in its wave, driving the
	// tree-elbow vs tee glyph in TUI renderers.
	LastInWave bool
	// LoopID is the scoped loop this task belongs to, or "" for a top-level
	// (non-loop) task.
	LoopID LoopID
}

// AnnotatedPlan returns the workflow's tasks in wave-then-declaration order,
// each annotated with its wave index, last-in-wave flag, and loop membership.
// The wave structure is computed once internally via [Waves]. Returns nil for
// an empty workflow.
func (w *Workflow) AnnotatedPlan() []PlanEntry {
	waves := w.Waves()
	if len(waves) == 0 {
		return nil
	}
	entries := make([]PlanEntry, 0, len(w.Tasks))
	for wi, wave := range waves {
		for j, id := range wave {
			var loopID LoopID
			if t := w.ByID(id); t != nil {
				loopID = t.Loop
			}
			entries = append(entries, PlanEntry{
				ID:         id,
				Wave:       wi,
				LastInWave: j == len(wave)-1,
				LoopID:     loopID,
			})
		}
	}
	return entries
}
