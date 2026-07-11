package runner

import (
	"fmt"
	"io"
	"sync"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/run"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// testOutput reproduces the plain run lines the runner tests assert on
// without importing pkg/tui back into pkg/runner's test binary.
type testOutput struct {
	w     io.Writer
	total int
	step  int
	mu    sync.Mutex
}

func newTestOutput(w io.Writer) RunOutput { return &testOutput{w: w} }

func (o *testOutput) Header(meta RunMeta) error {
	o.total = meta.Total
	o.step = 0
	if _, err := fmt.Fprintf(o.w, "Run file : %s\nCwd      : %s\n\n", meta.RunFile, meta.Cwd); err != nil {
		return err
	}
	if meta.Seeded == 0 {
		return nil
	}
	_, err := fmt.Fprintf(o.w, "Seeded   : %d task(s) from prior run\n\n", meta.Seeded)
	return err
}

func (o *testOutput) Events() run.EventSink {
	return run.SinkFromHooks(executor.Hooks{
		OnStart: func(t workflow.Task, iter int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.step++
			if iter >= 2 {
				o.total++
			}
			_, _ = fmt.Fprintf(o.w, "[%d/%d] %s\n", o.step, o.total, t.ID)
		},
		OnFinish: func(t workflow.Task, _ int, _ executor.TaskResult, err error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			if err != nil {
				_, _ = fmt.Fprintf(o.w, "  %s FAIL: %v\n", t.ID, err)
				return
			}
			_, _ = fmt.Fprintf(o.w, "  %s done\n", t.ID)
		},
	})
}

func (o *testOutput) Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error {
	done := len(distinctTaskIDs(rep))
	if done == expected {
		_, err := fmt.Fprintf(o.w, "✓ workflow %q complete\n", wf.ID)
		return err
	}
	_, err := fmt.Fprintf(o.w, "✗ workflow %q stopped after %d/%d tasks\n", wf.ID, done, expected)
	return err
}

func (o *testOutput) StoreError(err error) {
	_, _ = fmt.Fprintf(o.w, "  store: %v\n", err)
}

func distinctTaskIDs(rep *executor.Report) map[workflow.TaskID]struct{} {
	seen := make(map[workflow.TaskID]struct{}, len(rep.Tasks))
	for _, task := range rep.Tasks {
		seen[task.TaskID] = struct{}{}
	}
	return seen
}
