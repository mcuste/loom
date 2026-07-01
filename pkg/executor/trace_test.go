package executor

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

type recordingTraceSink struct {
	mu           sync.Mutex
	programs     int
	units        []string
	nodeStarts   []workflow.TaskID
	nodeFinishes []traceNodeFinish
	loopPasses   []string
}

type traceNodeFinish struct {
	id        workflow.TaskID
	iteration int
	status    string
	err       string
}

func (r *recordingTraceSink) ProgramStart(*program) {
	r.mu.Lock()
	r.programs++
	r.mu.Unlock()
}

func (r *recordingTraceSink) UnitStart(u unit) {
	r.mu.Lock()
	r.units = append(r.units, traceUnitLabel(u))
	r.mu.Unlock()
}

func (r *recordingTraceSink) NodeStart(n *node) {
	r.mu.Lock()
	r.nodeStarts = append(r.nodeStarts, n.id)
	r.mu.Unlock()
}

func (r *recordingTraceSink) NodeFinish(n *node, res TaskResult, err error) {
	finish := traceNodeFinish{
		id:        n.id,
		iteration: res.Iteration,
		status:    string(res.Status),
	}
	if err != nil {
		finish.err = err.Error()
	}
	r.mu.Lock()
	r.nodeFinishes = append(r.nodeFinishes, finish)
	r.mu.Unlock()
}

func (r *recordingTraceSink) LoopPassStart(lp *loopProgram, iter int) {
	r.mu.Lock()
	r.loopPasses = append(r.loopPasses, fmt.Sprintf("start:%s:%d", lp.group.ID, iter))
	r.mu.Unlock()
}

func (r *recordingTraceSink) LoopPassFinish(lp *loopProgram, iter int, err error) {
	label := fmt.Sprintf("finish:%s:%d", lp.group.ID, iter)
	if err != nil {
		label += ":" + err.Error()
	}
	r.mu.Lock()
	r.loopPasses = append(r.loopPasses, label)
	r.mu.Unlock()
}

func traceUnitLabel(u unit) string {
	switch u := u.(type) {
	case taskUnit:
		return "task:" + string(u.id)
	case loopUnit:
		return fmt.Sprintf("loop:%d", u.index)
	default:
		return fmt.Sprintf("%T", u)
	}
}

func TestInterpreterTraceNodeLifecycle(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "trace_task", Command: "printf traced"},
		},
	}

	prog := compileProgram(wf)
	rep := newReport(prog.order, Options{})
	st := newRootFrame(wf, rep, prog.order, Options{})
	trace := &recordingTraceSink{}
	var (
		started  []workflow.TaskID
		finished []workflow.TaskID
	)
	hooks := Hooks{
		OnStart: func(t workflow.Task, _ int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			started = append(started, t.ID)
		},
		OnFinish: func(t workflow.Task, _ int, _ TaskResult, _ error) {
			finished = append(finished, t.ID)
		},
	}

	interp := newInterpreterWithTrace(prog, hooks, Options{}, trace)
	if err := interp.run(context.Background(), st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if trace.programs != 1 {
		t.Fatalf("program starts = %d, want 1", trace.programs)
	}
	if !slices.Equal(trace.units, []string{"task:trace_task"}) {
		t.Fatalf("unit starts = %v, want [task:trace_task]", trace.units)
	}
	if !slices.Equal(trace.nodeStarts, []workflow.TaskID{"trace_task"}) {
		t.Fatalf("node starts = %v, want [trace_task]", trace.nodeStarts)
	}
	if len(trace.nodeFinishes) != 1 {
		t.Fatalf("node finishes = %d, want 1", len(trace.nodeFinishes))
	}
	finish := trace.nodeFinishes[0]
	if finish.id != "trace_task" {
		t.Fatalf("node finish id = %q, want trace_task", finish.id)
	}
	if finish.iteration != 0 {
		t.Fatalf("node finish iteration = %d, want 0", finish.iteration)
	}
	if finish.status != string(StatusOK) {
		t.Fatalf("node finish status = %q, want %q", finish.status, StatusOK)
	}
	if finish.err != "" {
		t.Fatalf("node finish err = %q, want empty", finish.err)
	}
	if !slices.Equal(started, []workflow.TaskID{"trace_task"}) {
		t.Fatalf("hook starts = %v, want [trace_task]", started)
	}
	if !slices.Equal(finished, []workflow.TaskID{"trace_task"}) {
		t.Fatalf("hook finishes = %v, want [trace_task]", finished)
	}
}

func TestInterpreterTraceLoopPassLifecycle(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "handle", Command: "printf {{item}}"},
		},
		Loops: []workflow.LoopGroup{
			{
				ID:      "fan",
				Kind:    workflow.LoopForEach,
				List:    []string{"red", "blue"},
				As:      "item",
				Members: []workflow.TaskID{"handle"},
			},
		},
	}

	prog := compileProgram(wf)
	rep := newReport(prog.order, Options{})
	st := newRootFrame(wf, rep, prog.order, Options{})
	trace := &recordingTraceSink{}

	interp := newInterpreterWithTrace(prog, Hooks{}, Options{}, trace)
	if err := interp.run(context.Background(), st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !slices.Equal(trace.units, []string{"loop:0"}) {
		t.Fatalf("unit starts = %v, want [loop:0]", trace.units)
	}
	if !slices.Equal(trace.loopPasses, []string{
		"start:fan:1",
		"finish:fan:1",
		"start:fan:2",
		"finish:fan:2",
	}) {
		t.Fatalf("loop pass events = %v, want sequential start/finish pairs", trace.loopPasses)
	}
	if len(trace.nodeFinishes) != 2 {
		t.Fatalf("node finishes = %d, want 2", len(trace.nodeFinishes))
	}
	if trace.nodeFinishes[0].iteration != 1 || trace.nodeFinishes[1].iteration != 2 {
		t.Fatalf("node finish iterations = [%d %d], want [1 2]", trace.nodeFinishes[0].iteration, trace.nodeFinishes[1].iteration)
	}
	for _, finish := range trace.nodeFinishes {
		if finish.id != "handle" {
			t.Fatalf("node finish id = %q, want handle", finish.id)
		}
		if finish.status != string(StatusOK) {
			t.Fatalf("node finish status = %q, want %q", finish.status, StatusOK)
		}
		if finish.err != "" {
			t.Fatalf("node finish err = %q, want empty", finish.err)
		}
	}
}
