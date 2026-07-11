// Package store persists the artifacts of a loom run to disk so users can
// inspect prompts, outputs, and accounting after the fact.
//
// A Run is opened against a root directory (loom's home directory: $LOOM_HOME,
// or $HOME/.loom by default; ".loom" is the fallback when no root is given) and
// writes a single self-contained JSON file per run:
//
//	<root>/runs/<workflow_id>/<run_id>.json
//	<root>/runs/<workflow_id>/latest.json -> <run_id>.json
//
// where run_id is "20060102T150405Z-<6 random hex>". The file embeds the
// verbatim workflow manifest plus a per-task array with the substituted
// prompt, the model output, usage, timing, and status. It is rewritten
// atomically (write to .tmp, rename) on every OnStart/OnFinish so a crash
// mid-run still leaves a valid file on disk.
//
// Run.OnFinish consumes a [TaskRecord] pre-mapped by pkg/runner, so the
// executor import lives only in the runner layer; the store owns only
// [Summary], its workflow-level Close input. The package never reaches into
// runtime or workflow internals beyond the small surface those packages already
// expose. Disk errors are reported via the optional OnError callback so a
// failing disk does not abort a workflow.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/task"
	"github.com/mcuste/loom/pkg/workflow"
)

// Summary is the workflow-level input to [Run.Close]: aggregate accounting
// plus the count of tasks that completed successfully. Pass nil to Close to
// leave the totals unset (useful when the run aborted before producing a
// summary).
type Summary struct {
	Usage     runtime.Usage
	TaskCount int
}

// runIDLayout is the time layout for the human-readable prefix of a run id.
// UTC and lexicographically sortable.
const runIDLayout = "20060102T150405Z"

// Status values written to the on-disk run record. The store is the
// serialization boundary for these values, but the definitions are re-exported
// from [task] so the executor's live result and this on-disk record share one
// vocabulary; a rename in [task] is a compile-time signal to every consumer.
const (
	StatusRunning = string(task.StatusRunning)
	StatusStarted = string(task.StatusStarted)
	StatusOK      = string(task.StatusOK)
	StatusFailed  = string(task.StatusFailed)
	StatusSkipped = string(task.StatusSkipped)
)

// Run is an open, on-disk record of a single workflow execution. Callers
// create a Run with Open, feed it executor events via OnStart/OnFinish, and
// finalize it with Close. Methods are safe for concurrent use.
type Run struct {
	path         string // <root>/runs/<workflow_id>/<run_id>.json
	dir          string // parent directory; "latest.json" is created here
	id           string
	clock        func() time.Time
	errorHandler func(error)

	mu     sync.Mutex
	state  RunRecord
	tasks  map[taskKey]int // (task id, iteration) -> index into state.Tasks
	closed bool            // Close is a no-op after the first call
}

// taskKey identifies a single task record. A looped task contributes one
// record per pass, so the iteration is part of the key: keying by id alone
// would collapse every iteration onto the first entry, losing all but the
// last pass. iter is 0 for a non-looped task.
type taskKey struct {
	id   workflow.TaskID
	iter int
}

// Config is the optional configuration for Open.
type Config struct {
	// Root is the loom data directory; defaults to ".loom".
	Root string
	// Now overrides the clock used for the run id and timestamps; nil uses
	// time.Now. Tests inject a fixed clock to assert layout deterministically.
	Now func() time.Time
	// Rand returns the random suffix appended to the run id. nil uses
	// crypto/rand; tests inject a deterministic source.
	Rand func() (string, error)
	// OnError is invoked for non-fatal write errors so the caller can surface
	// them without aborting the workflow. nil discards.
	OnError func(error)
	// Params holds resolved parameter values (key → value) for this run.
	// Stored verbatim; no provenance is recorded.
	Params map[string]string
	// Cwd is the working directory the run was invoked from. Recorded so a
	// later resume can restore it before re-running shell tasks and relative
	// paths. Empty means it is not recorded.
	Cwd string
	// ScheduleID links a run to the schedule that started it; empty for a run
	// launched directly from the CLI.
	ScheduleID string
	// TriggeredBy records what initiated the run ("cli" or "schedule"). Empty
	// means it is not recorded.
	TriggeredBy string
}

// Open creates a new run JSON file for workflowID under cfg.Root, seeded
// with the embedded manifest, and returns a Run ready to receive task
// events. The manifest bytes are stored verbatim so the on-disk snapshot
// is byte-identical to what the user ran.
func Open(workflowID workflow.WorkflowID, manifest []byte, cfg Config) (*Run, error) {
	h := NewHome(cfg.Root)
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	randFn := defaultRandSuffix
	if cfg.Rand != nil {
		randFn = cfg.Rand
	}

	started := now().UTC()
	suffix, err := randFn()
	if err != nil {
		return nil, fmt.Errorf("store: generate run id: %w", err)
	}
	id := started.Format(runIDLayout) + "-" + suffix

	dir := h.workflowRunsDir(string(workflowID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create run dir: %w", err)
	}
	path := h.runPath(string(workflowID), id)

	r := &Run{
		path:         path,
		dir:          dir,
		id:           id,
		clock:        now,
		errorHandler: cfg.OnError,
		state: RunRecord{
			RunID:       id,
			WorkflowID:  string(workflowID),
			StartedAt:   started,
			Status:      StatusRunning,
			Manifest:    string(manifest),
			Params:      cfg.Params,
			Cwd:         cfg.Cwd,
			ScheduleID:  cfg.ScheduleID,
			TriggeredBy: cfg.TriggeredBy,
		},
		tasks: map[taskKey]int{},
	}
	if err := r.flushLocked(); err != nil {
		return nil, fmt.Errorf("store: write run file: %w", err)
	}
	return r, nil
}

// ID returns the run id (sortable timestamp + random suffix).
func (r *Run) ID() string { return r.id }

// Path returns the absolute or root-relative path of the run JSON file.
func (r *Run) Path() string { return r.path }

// OnStart satisfies [executor.Hooks].OnStart: it appends a task entry with
// its resolved routing fields and rewrites the run file. The store receiver
// is the only captured state, so callers pass the method value directly
// (executor.Hooks{OnStart: run.OnStart}).
func (r *Run) OnStart(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := taskKey{id: t.ID, iter: iter}
	idx, ok := r.tasks[key]
	if !ok {
		r.state.Tasks = append(r.state.Tasks, TaskRecord{ID: string(t.ID)})
		idx = len(r.state.Tasks) - 1
		r.tasks[key] = idx
	}
	tr := &r.state.Tasks[idx]
	tr.Iteration = iter
	tr.Runtime = string(rt)
	tr.Model = string(m)
	tr.Effort = string(e)
	tr.StartedAt = r.now()
	tr.Status = StatusStarted
	if err := r.flushLocked(); err != nil {
		r.report(fmt.Errorf("store: task %s: write: %w", t.ID, err))
	}
}

// OnFinish records a task's final prompt, output, usage, and status, then
// rewrites the run file. rec is a [TaskRecord] DTO pre-populated by
// pkg/runner so the store need not import pkg/executor. Errors are surfaced
// via the configured OnError callback and do not propagate to the caller.
func (r *Run) OnFinish(t workflow.Task, iter int, rec TaskRecord, runErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := taskKey{id: t.ID, iter: iter}
	idx, ok := r.tasks[key]
	if !ok {
		r.state.Tasks = append(r.state.Tasks, TaskRecord{ID: string(t.ID)})
		idx = len(r.state.Tasks) - 1
		r.tasks[key] = idx
	}
	tr := &r.state.Tasks[idx]
	tr.Iteration = iter
	tr.Prompt = rec.Prompt
	tr.Command = rec.Command
	tr.Output = rec.Output
	tr.ExitCode = rec.ExitCode
	tr.Usage = rec.Usage
	tr.ElapsedMs = rec.ElapsedMs
	tr.FinishedAt = r.now()
	if runErr != nil {
		tr.Status = StatusFailed
		tr.Error = runErr.Error()
	} else {
		// Preserve the executor's terminal disposition: a skipped task must not be
		// recorded as "ok", or the persisted run view would disagree with the live
		// TUI about the same task. Any other nil-error result is a completed task.
		if rec.Status == StatusSkipped {
			tr.Status = StatusSkipped
		} else {
			tr.Status = StatusOK
		}
		tr.Error = ""
	}
	if err := r.flushLocked(); err != nil {
		r.report(fmt.Errorf("store: task %s: write: %w", t.ID, err))
	}
}

// Close finalizes the run: it records totals from summary, sets the overall
// status, and refreshes the latest.json symlink. Idempotent; subsequent
// calls return nil without rewriting the file or updating the symlink.
func (r *Run) Close(summary *Summary, runErr error) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.state.FinishedAt = r.now()
	r.state.ElapsedMs = r.state.FinishedAt.Sub(r.state.StartedAt).Milliseconds()
	r.state.Status = statusFor(runErr)
	if runErr != nil {
		r.state.Error = runErr.Error()
	}
	if summary != nil {
		r.state.Usage = usageDTO(summary.Usage)
		r.state.TaskCount = summary.TaskCount
	}
	err := r.flushLocked()
	r.mu.Unlock()

	if err != nil {
		return fmt.Errorf("store: finalize: %w", err)
	}
	if err := updateLatestSymlink(r.dir, r.id+".json"); err != nil {
		r.report(fmt.Errorf("store: update latest symlink: %w", err))
	}
	return nil
}

// flushLocked atomically rewrites the run JSON file. Caller must hold r.mu.
// The write goes to <path>.tmp then renames, so a crashed loom never leaves
// a half-written file under the canonical path.
func (r *Run) flushLocked() error {
	return writeJSONAtomic(r.path, r.state)
}

func (r *Run) now() time.Time {
	return r.clock().UTC()
}

func (r *Run) report(err error) {
	if r.errorHandler != nil {
		r.errorHandler(err)
	}
}

// updateLatestSymlink replaces <dir>/latest.json with a symlink pointing to
// target (a basename, relative to dir, so the link stays valid if the .loom
// tree is moved).
func updateLatestSymlink(dir, target string) error {
	link := filepath.Join(dir, "latest.json")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, link)
}

func defaultRandSuffix() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func statusFor(err error) string {
	if err == nil {
		return StatusOK
	}
	return StatusFailed
}

// Load reads a run record from path and decodes it into a [RunRecord]. Used
// by the resume command to recover the persisted manifest, params, and per-
// task outputs from a prior run. Errors are wrapped with the source path so
// the caller can surface them without rebuilding context.
func Load(path string) (*RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("store: read run record %s: %w", path, err)
	}
	var rec RunRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("store: parse run record %s: %w", path, err)
	}
	return &rec, nil
}
