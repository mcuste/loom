// Package task holds the small domain types shared across the executor, store,
// and tui layers. It depends on nothing else in the tree so those packages can
// agree on a vocabulary (notably [Status]) without importing one another and
// without any one of them owning the definition.
package task

// Status is a task's (or a run's) terminal disposition. It is the single
// authoritative definition threaded through the executor's live result, the
// store's on-disk record, and the tui's badge rendering, so all three name the
// same states and a rename here is one compile-time signal to every consumer.
type Status string

const (
	// StatusRunning is the workflow-level status while a run is still in flight.
	StatusRunning Status = "running"
	// StatusStarted marks a task that has begun but not yet finished.
	StatusStarted Status = "started"
	// StatusOK marks a task (or run) that ran to completion.
	StatusOK Status = "ok"
	// StatusFailed marks a task (or run) that ended in error.
	StatusFailed Status = "failed"
	// StatusSkipped marks a task whose `when:` guard evaluated false: it produced
	// no output but still closed its gate so downstream tasks proceed.
	StatusSkipped Status = "skipped"
)
