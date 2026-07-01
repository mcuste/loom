package executor

import (
	"maps"
	"sync"

	"github.com/mcuste/loom/pkg/workflow"
)

// scopeState bundles the four run-scope maps that are always cloned and merged
// together: outputs, succeeded, skipped, and exitCodes. Grouping them into one
// value eliminates the lockstep quad-clone in forParallelIteration and the
// duplicate workflow.Env construction in evalWhen and loopConverged.
type scopeState struct {
	outputs   map[workflow.TaskID]string
	succeeded map[workflow.TaskID]bool
	skipped   map[workflow.TaskID]bool
	exitCodes map[workflow.TaskID]int
}

// cloneUnderLock returns a deep copy of s, acquiring mu for the duration.
// The caller must NOT already hold mu.
func (s scopeState) cloneUnderLock(mu *sync.Mutex) scopeState {
	mu.Lock()
	defer mu.Unlock()
	return scopeState{
		outputs:   maps.Clone(s.outputs),
		succeeded: maps.Clone(s.succeeded),
		skipped:   maps.Clone(s.skipped),
		exitCodes: maps.Clone(s.exitCodes),
	}
}

// snapshotEnv snapshots s into a workflow.Env, acquiring mu for the duration.
// The caller must NOT already hold mu.
func (s scopeState) snapshotEnv(mu *sync.Mutex) workflow.Env {
	mu.Lock()
	defer mu.Unlock()
	return workflow.Env{
		Outputs:   maps.Clone(s.outputs),
		Succeeded: maps.Clone(s.succeeded),
		Skipped:   maps.Clone(s.skipped),
		ExitCodes: maps.Clone(s.exitCodes),
	}
}

// toEnvLocked snapshots s into a workflow.Env. The caller MUST hold the
// associated mutex.
func (s scopeState) toEnvLocked() workflow.Env {
	return workflow.Env{
		Outputs:   maps.Clone(s.outputs),
		Succeeded: maps.Clone(s.succeeded),
		Skipped:   maps.Clone(s.skipped),
		ExitCodes: maps.Clone(s.exitCodes),
	}
}

// recordSkipLocked writes the skipped disposition for id. The caller MUST
// hold the associated mutex.
func (s *scopeState) recordSkipLocked(id workflow.TaskID) {
	s.outputs[id] = ""
	s.skipped[id] = true
	s.exitCodes[id] = 0
}

// recordResultLocked writes the completed disposition for id. The caller MUST
// hold the associated mutex.
func (s *scopeState) recordResultLocked(id workflow.TaskID, output string, exitCode int) {
	s.outputs[id] = output
	s.succeeded[id] = true
	s.exitCodes[id] = exitCode
}

// mergeParallelLocked merges the parallel pass's member entries from src into
// s, applying the success-dominates-skip rule:
//   - A pass where a member succeeded publishes its output and clears any
//     prior skip marker; success cannot be downgraded.
//   - A pass where a member was skipped takes effect only when no element has
//     succeeded yet, so a later success is never clobbered.
//
// The caller MUST hold the associated mutex.
func (s *scopeState) mergeParallelLocked(members []workflow.TaskID, src scopeState) {
	for _, m := range members {
		switch {
		case src.succeeded[m]:
			s.outputs[m] = src.outputs[m]
			s.exitCodes[m] = src.exitCodes[m]
			s.succeeded[m] = true
			s.skipped[m] = false
		case src.skipped[m] && !s.succeeded[m]:
			s.outputs[m] = src.outputs[m]
			s.exitCodes[m] = src.exitCodes[m]
			s.skipped[m] = true
		}
	}
}

// passOutputsLocked snapshots the current output of each member task into a
// fresh map. Used by a loop pass to capture body outputs for threading into
// the next iteration as prev. The caller MUST hold the associated mutex.
func (s scopeState) passOutputsLocked(members []workflow.TaskID) map[workflow.TaskID]string {
	out := make(map[workflow.TaskID]string, len(members))
	for _, m := range members {
		out[m] = s.outputs[m]
	}
	return out
}
