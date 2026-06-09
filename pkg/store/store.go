// Package store persists the artifacts of a loom run to disk so users can
// inspect prompts, outputs, and accounting after the fact.
//
// A Run is opened against a root directory (typically ".loom") and writes a
// single self-contained JSON file per run:
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
// The store is intentionally orthogonal to the executor: it consumes
// [executor.TaskResult] values via hook adapters and never reaches into
// runtime or workflow internals beyond the small surface those packages
// already expose. Disk errors are reported via the optional OnError callback
// so a failing disk does not abort a workflow.
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

// runIDLayout is the time layout for the human-readable prefix of a run id.
// UTC and lexicographically sortable.
const runIDLayout = "20060102T150405Z"

// Run is an open, on-disk record of a single workflow execution. Callers
// create a Run with Open, feed it executor events via OnStart/OnFinish, and
// finalize it with Close. Methods are safe for concurrent use.
type Run struct {
	path         string // <root>/runs/<workflow_id>/<run_id>.json
	dir          string // parent directory; "latest.json" is created here
	id           string
	clock        func() time.Time
	errorHandler func(error)

	mu    sync.Mutex
	state runRecord
	tasks map[workflow.TaskID]int // task id -> index into state.Tasks
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
		state: runRecord{
			RunID:      id,
			WorkflowID: string(workflowID),
			StartedAt:  started,
			Status:     "running",
			Manifest:   string(manifest),
		},
		tasks: map[workflow.TaskID]int{},
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

// OnStart returns an [executor.Hooks].OnStart adapter that appends a task
// entry with its resolved routing fields and rewrites the run file.
func (r *Run) OnStart() func(workflow.Task, runtime.Name, runtime.Model, runtime.Effort) {
	return func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
		r.mu.Lock()
		defer r.mu.Unlock()
		idx, ok := r.tasks[t.ID]
		if !ok {
			r.state.Tasks = append(r.state.Tasks, taskRecord{ID: string(t.ID)})
			idx = len(r.state.Tasks) - 1
			r.tasks[t.ID] = idx
		}
		tr := &r.state.Tasks[idx]
		tr.Runtime = string(rt)
		tr.Model = string(m)
		tr.Effort = string(e)
		tr.StartedAt = r.now()
		tr.Status = "started"
		if err := r.flushLocked(); err != nil {
			r.report(fmt.Errorf("store: task %s: write: %w", t.ID, err))
		}
	}
}

// OnFinish returns an [executor.Hooks].OnFinish adapter that records the
// task's final prompt, output, usage, and status, then rewrites the run
// file. Errors are surfaced via the configured OnError callback and do not
// propagate to the executor.
func (r *Run) OnFinish() func(workflow.Task, executor.TaskResult, error) {
	return func(t workflow.Task, res executor.TaskResult, runErr error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		idx, ok := r.tasks[t.ID]
		if !ok {
			r.state.Tasks = append(r.state.Tasks, taskRecord{ID: string(t.ID)})
			idx = len(r.state.Tasks) - 1
			r.tasks[t.ID] = idx
		}
		tr := &r.state.Tasks[idx]
		tr.Prompt = res.Prompt
		tr.Output = res.Output
		tr.Usage = usageDTO(res.Usage)
		tr.FinishedAt = r.now()
		tr.ElapsedMs = res.Elapsed.Milliseconds()
		if runErr != nil {
			tr.Status = "failed"
			tr.Error = runErr.Error()
		} else {
			tr.Status = "ok"
			tr.Error = ""
		}
		if err := r.flushLocked(); err != nil {
			r.report(fmt.Errorf("store: task %s: write: %w", t.ID, err))
		}
	}
}

// Close finalizes the run: it records totals from report, sets the overall
// status, and refreshes the latest.json symlink. Safe to call once.
func (r *Run) Close(report *executor.Report, runErr error) error {
	r.mu.Lock()
	r.state.FinishedAt = r.now()
	r.state.ElapsedMs = r.state.FinishedAt.Sub(r.state.StartedAt).Milliseconds()
	r.state.Status = statusFor(runErr)
	if runErr != nil {
		r.state.Error = runErr.Error()
	}
	if report != nil {
		r.state.Usage = usageDTO(report.Usage)
		r.state.TaskCount = len(report.Tasks)
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
		return "ok"
	}
	return "failed"
}

func usageDTO(u runtime.Usage) usageJSON {
	return usageJSON{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens,
		TotalCostUSD:    u.TotalCostUSD,
	}
}

// On-disk DTOs. Private to the package so the JSON layout can evolve
// without breaking importers.

type runRecord struct {
	RunID      string       `json:"run_id"`
	WorkflowID string       `json:"workflow_id"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at,omitzero"`
	ElapsedMs  int64        `json:"elapsed_ms,omitempty"`
	Status     string       `json:"status"`
	Error      string       `json:"error,omitempty"`
	TaskCount  int          `json:"task_count,omitempty"`
	Usage      usageJSON    `json:"usage,omitzero"`
	Manifest   string       `json:"manifest"`
	Tasks      []taskRecord `json:"tasks"`
}

type taskRecord struct {
	ID         string    `json:"id"`
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
	Output     string    `json:"output,omitempty"`
}

type usageJSON struct {
	InputTokens     int     `json:"input_tokens,omitempty"`
	OutputTokens    int     `json:"output_tokens,omitempty"`
	CacheReadTokens int     `json:"cache_read_tokens,omitempty"`
	TotalCostUSD    float64 `json:"total_cost_usd,omitempty"`
}
