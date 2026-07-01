package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// RunHeader is the lightweight summary of a single run, decoded from a run
// record's top-level fields without its (potentially large) manifest or
// per-task array. The listing commands and the run browser use it to render
// the runs index; the full [RunRecord] is read lazily via [Load] only when a
// run is opened. Path is the on-disk location of the record so a caller can
// hand it straight to Load.
type RunHeader struct {
	Path         string
	RunID        string
	WorkflowID   string
	Status       string
	Error        string
	StartedAt    time.Time
	FinishedAt   time.Time
	ElapsedMs    int64
	TaskCount    int
	TotalCostUSD float64
}

// runHeaderJSON mirrors the subset of the on-disk run record the listing path
// needs. Decoding into this struct (rather than a full RunRecord) skips
// binding the manifest string and the task array, which dominate a record's
// size; the file is still read whole, but nothing downstream retains the heavy
// fields.
type runHeaderJSON struct {
	RunID      string    `json:"run_id"`
	WorkflowID string    `json:"workflow_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	ElapsedMs  int64     `json:"elapsed_ms"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	TaskCount  int       `json:"task_count"`
	Usage      usageJSON `json:"usage"`
}

// ListWorkflows returns the ids of every workflow that has at least one run
// recorded under root, sorted lexicographically. root defaults to ".loom"
// when empty, matching [Config.Root]. A missing runs directory is not an
// error: it returns an empty slice so a fresh install lists cleanly.
func ListWorkflows(root string) ([]string, error) {
	h := NewHome(root)
	runsDir := h.runsDir()
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read runs dir %s: %w", runsDir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ListRuns returns the run headers for a single workflow, newest first. A
// missing workflow directory is not an error: it returns an empty slice. root
// defaults to ".loom" when empty.
func ListRuns(root, workflowID string) ([]RunHeader, error) {
	h := NewHome(root)
	dir := h.workflowRunsDir(workflowID)
	headers, err := readRunDir(dir)
	if err != nil {
		return nil, err
	}
	sortNewestFirst(headers)
	return headers, nil
}

// ListAllRuns returns the run headers across every workflow under root, newest
// first, so the browser can present one unified index. A missing runs
// directory yields an empty slice. root defaults to ".loom" when empty.
func ListAllRuns(root string) ([]RunHeader, error) {
	ids, err := ListWorkflows(root)
	if err != nil {
		return nil, err
	}
	h := NewHome(root)
	var all []RunHeader
	for _, id := range ids {
		dir := h.workflowRunsDir(id)
		headers, err := readRunDir(dir)
		if err != nil {
			return nil, err
		}
		all = append(all, headers...)
	}
	sortNewestFirst(all)
	return all, nil
}

// readRunDir decodes every run record header in dir, skipping the latest.json
// symlink (it duplicates a real record) and any half-written .tmp file. A
// missing directory yields a nil slice and no error.
func readRunDir(dir string) ([]RunHeader, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read run dir %s: %w", dir, err)
	}
	var headers []RunHeader
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || filepath.Ext(name) != ".json" {
			continue
		}
		h, err := readHeader(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		headers = append(headers, h)
	}
	return headers, nil
}

// readHeader reads and decodes a single run record's header fields.
func readHeader(path string) (RunHeader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunHeader{}, fmt.Errorf("store: read run header %s: %w", path, err)
	}
	var hj runHeaderJSON
	if err := json.Unmarshal(data, &hj); err != nil {
		return RunHeader{}, fmt.Errorf("store: parse run header %s: %w", path, err)
	}
	return RunHeader{
		Path:         path,
		RunID:        hj.RunID,
		WorkflowID:   hj.WorkflowID,
		Status:       hj.Status,
		Error:        hj.Error,
		StartedAt:    hj.StartedAt,
		FinishedAt:   hj.FinishedAt,
		ElapsedMs:    hj.ElapsedMs,
		TaskCount:    hj.TaskCount,
		TotalCostUSD: hj.Usage.TotalCostUSD,
	}, nil
}

// sortNewestFirst orders headers by start time descending; ties break on the
// run id descending so the order is total and stable (run ids carry a random
// suffix, so two runs sharing a start instant still order deterministically).
func sortNewestFirst(h []RunHeader) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].StartedAt.Equal(h[j].StartedAt) {
			return h[i].RunID > h[j].RunID
		}
		return h[i].StartedAt.After(h[j].StartedAt)
	})
}
