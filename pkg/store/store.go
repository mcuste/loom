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
// Run.OnFinish consumes [executor.TaskResult] directly, so the CLI hooks the
// store into the executor with no field-by-field translation at the boundary;
// the store owns only [Summary], its workflow-level Close input. The package
// never reaches into runtime or workflow internals beyond the small surface
// those packages already expose. Disk errors are reported via the optional
// OnError callback so a failing disk does not abort a workflow.
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

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
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

// Status values written to the on-disk run record. Exported so callers (e.g.
// the resume command) reference the same string the store writes; a rename
// here is a compile-time signal to every consumer instead of a silent miss.
const (
	StatusRunning = "running"
	StatusStarted = "started"
	StatusOK      = "ok"
	StatusFailed  = "failed"
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
}

// Open creates a new run JSON file for workflowID under cfg.Root, seeded
// with the embedded manifest, and returns a Run ready to receive task
// events. The manifest bytes are stored verbatim so the on-disk snapshot
// is byte-identical to what the user ran.
func Open(workflowID workflow.WorkflowID, manifest []byte, cfg Config) (*Run, error) {
	root := cfg.Root
	if root == "" {
		root = ".loom"
	}
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

	dir := filepath.Join(root, "runs", string(workflowID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create run dir: %w", err)
	}
	path := filepath.Join(dir, id+".json")

	r := &Run{
		path:         path,
		dir:          dir,
		id:           id,
		clock:        now,
		errorHandler: cfg.OnError,
		state: RunRecord{
			RunID:      id,
			WorkflowID: string(workflowID),
			StartedAt:  started,
			Status:     StatusRunning,
			Manifest:   string(manifest),
			Params:     cfg.Params,
			Cwd:        cfg.Cwd,
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

// OnFinish satisfies [executor.Hooks].OnFinish: it records a task's final
// prompt, output, usage, and status, then rewrites the run file. Errors are
// surfaced via the configured OnError callback and do not propagate to the
// caller. As with OnStart, callers pass the method value directly.
func (r *Run) OnFinish(t workflow.Task, iter int, res executor.TaskResult, runErr error) {
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
	tr.Prompt = res.Prompt
	tr.Command = res.Command
	tr.Output = res.Output
	tr.Usage = usageDTO(res.Usage)
	tr.ElapsedMs = res.Elapsed.Milliseconds()
	tr.FinishedAt = r.now()
	if runErr != nil {
		tr.Status = StatusFailed
		tr.Error = runErr.Error()
	} else {
		tr.Status = StatusOK
		tr.Error = ""
	}
	if err := r.flushLocked(); err != nil {
		r.report(fmt.Errorf("store: task %s: write: %w", t.ID, err))
	}
}

// Close finalizes the run: it records totals from summary, sets the overall
// status, and refreshes the latest.json symlink. Idempotent — subsequent
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
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
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

func usageDTO(u runtime.Usage) usageJSON {
	return usageJSON{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens,
		TotalCostUSD:    u.TotalCostUSD,
	}
}

// LoadState reads the cross-run state map for wf from
// <root>/state/<workflow_id>.json. A missing file is not an error: it returns
// a fresh empty map so first-tick callers can write into it directly. root
// defaults to ".loom" when empty, matching [Config.Root].
func LoadState(root string, wf workflow.WorkflowID) (map[string]string, error) {
	if root == "" {
		root = ".loom"
	}
	path := statePath(root, wf)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("store: read state %s: %w", path, err)
	}
	state := map[string]string{}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("store: parse state %s: %w", path, err)
	}
	return state, nil
}

// SaveState atomically writes the cross-run state map for wf to
// <root>/state/<workflow_id>.json. It mirrors [Run.flushLocked]: the bytes go
// to a .tmp sibling and are renamed into place, so a crash mid-write never
// leaves a half-written state file. root defaults to ".loom" when empty.
func SaveState(root string, wf workflow.WorkflowID, state map[string]string) error {
	if root == "" {
		root = ".loom"
	}
	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: create state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal state: %w", err)
	}
	data = append(data, '\n')
	path := statePath(root, wf)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("store: write state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: rename state: %w", err)
	}
	return nil
}

// statePath is the on-disk location of a workflow's cross-run state file.
func statePath(root string, wf workflow.WorkflowID) string {
	return filepath.Join(root, "state", string(wf)+".json")
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

// RunRecord is the top-level on-disk structure for a single workflow run.
// Exported so callers (e.g. the resume command) bind to the same JSON shape
// the store writes; a field rename here is a compile-time error at the call
// site instead of a silent JSON decode miss.
type RunRecord struct {
	RunID      string            `json:"run_id"`
	WorkflowID string            `json:"workflow_id"`
	Cwd        string            `json:"cwd,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at,omitzero"`
	ElapsedMs  int64             `json:"elapsed_ms,omitempty"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	TaskCount  int               `json:"task_count,omitempty"`
	Usage      usageJSON         `json:"usage,omitzero"`
	Manifest   string            `json:"manifest"`
	Params     map[string]string `json:"params,omitempty"`
	Tasks      []TaskRecord      `json:"tasks"`
}

// TaskRecord is the per-task entry within a [RunRecord]. Iteration is the
// 1-based scoped-loop pass that produced this record and 0 for a non-looped
// task, so a looped task contributes one record per pass.
type TaskRecord struct {
	ID         string    `json:"id"`
	Iteration  int       `json:"iteration,omitempty"`
	Runtime    string    `json:"runtime,omitempty"`
	Model      string    `json:"model,omitempty"`
	Effort     string    `json:"effort,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	ElapsedMs  int64     `json:"elapsed_ms,omitempty"`
	Status     string    `json:"status,omitempty"`
	Error      string    `json:"error,omitempty"`
	Usage      usageJSON `json:"usage,omitzero"`
	Prompt     string    `json:"prompt,omitempty"`
	Command    string    `json:"command,omitempty"`
	Output     string    `json:"output,omitempty"`
}

// usageJSON is unexported; external callers read accounting via the embedded
// fields of RunRecord/TaskRecord rather than through this inner type.
type usageJSON struct {
	InputTokens     int     `json:"input_tokens,omitempty"`
	OutputTokens    int     `json:"output_tokens,omitempty"`
	CacheReadTokens int     `json:"cache_read_tokens,omitempty"`
	TotalCostUSD    float64 `json:"total_cost_usd,omitempty"`
}
