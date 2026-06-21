package executor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// loopRuntime is a scripted, prompt-recording fake. For each task id it returns
// the next entry of a per-task output script (saturating at the last entry once
// exhausted) and records every prompt it was handed, in call order. Scripting
// the convergence target's output independently of its prompt lets a test drive
// a scoped loop to converge (or not) deterministically, and the recorded
// prompts let a test observe what substitution produced on each pass.
type loopRuntime struct {
	mu      sync.Mutex
	scripts map[string][]string
	calls   map[string]int
	prompts map[string][]string
	usage   runtime.Usage
}

// newLoopRT arms a fresh scripted runtime with a per-task output script and
// per-call usage, registers it under a runtime name unique to the calling test,
// and returns both. Each test gets its own instance and name, so cases share no
// mutable state and can run with t.Parallel(); the name is interpolated into
// the workflow's `runtime:` field.
func newLoopRT(t *testing.T, scripts map[string][]string, usage runtime.Usage) (runtime.Name, *loopRuntime) {
	t.Helper()
	rt := &loopRuntime{
		scripts: scripts,
		calls:   map[string]int{},
		prompts: map[string][]string{},
		usage:   usage,
	}
	name := runtime.Name("loop-rt-" + t.Name())
	runtime.Register(name, rt)
	return name, rt
}

func (r *loopRuntime) promptsFor(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.prompts[id])
}

func (r *loopRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (r *loopRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := req.TaskID
	r.prompts[id] = append(r.prompts[id], req.Prompt)
	n := r.calls[id]
	r.calls[id]++
	seq := r.scripts[id]
	out := ""
	if len(seq) > 0 {
		if n < len(seq) {
			out = seq[n]
		} else {
			out = seq[len(seq)-1]
		}
	}
	return runtime.Response{Output: out, Usage: r.usage}, nil
}

// convergeWorkflow is a top-level seed feeding a three-member scoped loop
// {a -> b -> c} that converges when c's trimmed output is empty, with an exit
// consumer reading body member a. Member a folds in both the pinned entry
// dependency {{seed}} and the prior iteration {{prev.c}}.
func convergeWorkflow(rt runtime.Name) string {
	return fmt.Sprintf(`
name: wf_conv
runtime: %s
model: m1
tasks:
  - id: seed
    prompt: S
  - id: exit
    depends_on: [a]
    prompt: "exit {{a}}"
loops:
  - id: work
    until_empty: c
    max: 5
    tasks:
      - id: a
        depends_on: [seed]
        prompt: "a {{seed}} {{prev.c}}"
      - id: b
        depends_on: [a]
        prompt: "b {{a}}"
      - id: c
        depends_on: [b]
        prompt: "c {{b}}"
`, rt)
}

// convergeScripts drives convergeWorkflow to converge on the second pass: c
// yields "c1" on pass 1 and "" on pass 2, so the loop runs exactly twice.
func convergeScripts() map[string][]string {
	return map[string][]string{
		"seed": {"S"},
		"a":    {"a1", "a2"},
		"b":    {"b1", "b2"},
		"c":    {"c1", ""},
		"exit": {"EXIT"},
	}
}

// iterationsOf collects, in completion order, the Iteration stamp of every
// result for the given task id.
func iterationsOf(rep *executor.Report, id workflow.TaskID) []int {
	var its []int
	for _, r := range rep.Tasks {
		if r.TaskID == id {
			its = append(its, r.Iteration)
		}
	}
	return its
}

// TestRun_ScopedLoop_ConvergesByUntilEmptyInTwoPasses pins that a three-member
// loop whose until_empty target drains on the second pass runs exactly two
// iterations, each body result stamped with its 1-based pass number.
func TestRun_ScopedLoop_ConvergesByUntilEmptyInTwoPasses(t *testing.T) {
	t.Parallel()
	rt, _ := newLoopRT(t, convergeScripts(), runtime.Usage{})

	wf, err := workflow.Parse([]byte(convergeWorkflow(rt)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := iterationsOf(rep, "c")
	want := []int{1, 2}
	if !slices.Equal(got, want) {
		t.Errorf("iterations of c = %v, want %v", got, want)
	}
}

// TestRun_ScopedLoop_ConvergesByUntilExpression pins the until-expression
// convergence path: the loop ends once the compiled `until:` condition over the
// body outputs holds (c == "done" on the second pass), exercising the
// loopConverged Cond.Eval branch rather than until_empty.
func TestRun_ScopedLoop_ConvergesByUntilExpression(t *testing.T) {
	t.Parallel()
	rt, _ := newLoopRT(t, map[string][]string{
		"seed": {"S"},
		"a":    {"a1", "a2"},
		"b":    {"b1", "b2"},
		"c":    {"working", "done"}, // until '{{c}} == "done"' holds on pass 2
	}, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf_until
runtime: %s
model: m1
tasks:
  - id: seed
    prompt: S
loops:
  - id: work
    until: '{{c}} == "done"'
    max: 5
    tasks:
      - id: a
        depends_on: [seed]
        prompt: "a {{seed}}"
      - id: b
        depends_on: [a]
        prompt: "b {{a}}"
      - id: c
        depends_on: [b]
        prompt: "c {{b}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := iterationsOf(rep, "c")
	want := []int{1, 2}
	if !slices.Equal(got, want) {
		t.Errorf("iterations of c = %v, want %v (until expression converges on pass 2)", got, want)
	}
}

// TestRun_ScopedLoop_StopsAtMaxWithoutConvergence pins that a loop whose target
// never drains halts at Max passes without error rather than spinning forever.
func TestRun_ScopedLoop_StopsAtMaxWithoutConvergence(t *testing.T) {
	t.Parallel()
	rt, _ := newLoopRT(t, map[string][]string{
		"seed": {"S"},
		"a":    {"a"},
		"b":    {"b"},
		"c":    {"still-work"}, // never empty: saturates, target never drains
	}, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf_max
runtime: %s
model: m1
tasks:
  - id: seed
    prompt: S
loops:
  - id: work
    until_empty: c
    max: 3
    tasks:
      - id: a
        depends_on: [seed]
        prompt: "a {{seed}}"
      - id: b
        depends_on: [a]
        prompt: "b {{a}}"
      - id: c
        depends_on: [b]
        prompt: "c {{b}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := iterationsOf(rep, "c")
	want := []int{1, 2, 3}
	if !slices.Equal(got, want) {
		t.Errorf("iterations of c = %v, want %v (loop should cap at max=3)", got, want)
	}
}

// TestRun_ScopedLoop_PinsEntryDependencyAcrossPasses pins that an entry
// dependency ({{seed}}) substitutes to the same value on every pass even as the
// prior-iteration placeholder ({{prev.c}}) changes between passes.
func TestRun_ScopedLoop_PinsEntryDependencyAcrossPasses(t *testing.T) {
	t.Parallel()
	rt, loopRT := newLoopRT(t, convergeScripts(), runtime.Usage{})

	wf, err := workflow.Parse([]byte(convergeWorkflow(rt)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Member a's prompt is "a {{seed}} {{prev.c}}": field 1 is the pinned entry
	// value, which must be identical on both passes.
	seen := loopRT.promptsFor("a")
	if len(seen) != 2 {
		t.Fatalf("prompts for a = %d (%v), want 2 passes", len(seen), seen)
	}
	entry := func(prompt string) string {
		f := strings.Fields(prompt)
		if len(f) < 2 {
			return ""
		}
		return f[1]
	}
	if e1, e2 := entry(seen[0]), entry(seen[1]); e1 != "S" || e2 != "S" {
		t.Errorf("entry value across passes = (%q, %q), want both %q", e1, e2, "S")
	}
}

// TestRun_ScopedLoop_ExitConsumerSeesFinalPassOutput pins that a task outside
// the loop depending on a body member reads that member's final-iteration
// output (a2), not its first-pass output.
func TestRun_ScopedLoop_ExitConsumerSeesFinalPassOutput(t *testing.T) {
	t.Parallel()
	rt, loopRT := newLoopRT(t, convergeScripts(), runtime.Usage{})

	wf, err := workflow.Parse([]byte(convergeWorkflow(rt)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	seen := loopRT.promptsFor("exit")
	if len(seen) != 1 {
		t.Fatalf("prompts for exit = %d (%v), want 1", len(seen), seen)
	}
	if got, want := seen[0], "exit a2"; got != want {
		t.Errorf("exit consumer prompt = %q, want %q (final pass output)", got, want)
	}
}

// TestRun_ScopedLoop_UsageSumsAllIterations pins that Report.Usage accumulates
// every iteration's cost: 8 runtime calls (seed + exit + 3 members x 2 passes)
// at a fixed per-call usage must sum across all of them.
func TestRun_ScopedLoop_UsageSumsAllIterations(t *testing.T) {
	t.Parallel()
	perCall := runtime.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 0.001}
	rt, _ := newLoopRT(t, convergeScripts(), perCall)

	wf, err := workflow.Parse([]byte(convergeWorkflow(rt)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := runtime.Usage{InputTokens: 80, OutputTokens: 160, TotalCostUSD: 0.008}
	if rep.Usage.InputTokens != want.InputTokens ||
		rep.Usage.OutputTokens != want.OutputTokens ||
		rep.Usage.TotalCostUSD != want.TotalCostUSD {
		t.Errorf("Usage = %+v, want %+v (sum over all iterations)", rep.Usage, want)
	}
}

// blockingRuntime returns immediately for every task except blockOn, for which
// it signals started and then blocks until the context is cancelled, returning
// ctx.Err(). It lets a test park a loop-body member inside the runtime and then
// cancel, observing that cancellation propagates into the loop body.
type blockingRuntime struct {
	blockOn string
	started chan struct{}
	once    sync.Once
}

func (b *blockingRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (b *blockingRuntime) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	if req.TaskID != b.blockOn {
		return runtime.Response{Output: req.TaskID}, nil
	}
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return runtime.Response{}, ctx.Err()
}

// TestRun_ScopedLoop_CancellationPropagatesIntoBody pins that cancelling the
// Run context while a loop-body member is in flight aborts the loop: the body
// task observes ctx.Done() and Run returns a context.Canceled error.
func TestRun_ScopedLoop_CancellationPropagatesIntoBody(t *testing.T) {
	t.Parallel()
	rt := &blockingRuntime{blockOn: "a", started: make(chan struct{})}
	name := runtime.Name("loop-block-" + t.Name())
	runtime.Register(name, rt)

	src := fmt.Sprintf(`
name: wf_cancel
runtime: %s
model: m1
tasks:
  - id: seed
    prompt: S
loops:
  - id: work
    until_empty: c
    max: 5
    tasks:
      - id: a
        depends_on: [seed]
        prompt: "a {{seed}}"
      - id: b
        depends_on: [a]
        prompt: "b {{a}}"
      - id: c
        depends_on: [b]
        prompt: "c {{b}}"
`, name)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-rt.started // body member a is now in flight inside the loop
		cancel()
	}()

	_, err = executor.Run(ctx, wf, executor.Hooks{}, executor.Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

// TestRun_ScopedLoop_BudgetGateAbortsLoop pins that the workflow cost budget is
// enforced inside the loop body: with each call costing 0.001 and a 0.0025
// ceiling, the loop's third dispatch (c on pass 1, after seed/a/b spend 0.003)
// trips the gate and Run returns a *BudgetExceededError.
func TestRun_ScopedLoop_BudgetGateAbortsLoop(t *testing.T) {
	t.Parallel()
	perCall := runtime.Usage{TotalCostUSD: 0.001}
	rt, _ := newLoopRT(t, map[string][]string{
		"seed": {"S"},
		"a":    {"a"},
		"b":    {"b"},
		"c":    {"still-work"},
	}, perCall)

	src := fmt.Sprintf(`
name: wf_budget
runtime: %s
model: m1
budget:
  max_cost_usd: 0.0025
tasks:
  - id: seed
    prompt: S
loops:
  - id: work
    until_empty: c
    max: 5
    tasks:
      - id: a
        depends_on: [seed]
        prompt: "a {{seed}}"
      - id: b
        depends_on: [a]
        prompt: "b {{a}}"
      - id: c
        depends_on: [b]
        prompt: "c {{b}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	var be *executor.BudgetExceededError
	if !errors.As(err, &be) {
		t.Fatalf("Run error = %v, want *BudgetExceededError", err)
	}
}
