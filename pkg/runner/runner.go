// Package runner is the unified workflow-run engine. It is the single place
// that owns the store open/close lifecycle, seeded-task stamping, writes_state
// fold-back, executor hook joining, and exit-code seeding. cmd/loom and the
// daemon both call runner.Run; neither needs to know the internals.
package runner

import (
	"context"
	"strings"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/run"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// SeedPlan carries the optional seeded tasks threaded into the unified run
// pipeline. Each entry holds the per-task material a resume carries over from a
// prior run: the stored output (fed to executor.Options.Seed and used to
// annotate the printed plan) plus the metadata the store needs to stamp a
// synthetic ok record before the executor starts. A zero SeedPlan (nil entries)
// marks a plain run, where the Seeded line and the seed-stamping are both
// suppressed.
type SeedPlan struct {
	entries []seedEntry
}

// seedEntry is the per-task material stamped into a fresh run record for a
// task whose output is being carried over from a prior run. Fields are
// unexported: the type is package-internal and only assembled by callers in
// this package.
type seedEntry struct {
	id       workflow.TaskID
	prompt   string
	command  string
	output   string
	exitCode int
}

// SeedPlanFromRecord builds the carry-over plan from a prior run's ok tasks.
// It is the one seedEntry-building site: every ok task in the record becomes an
// entry unfiltered, leaving resolveSeed as the single authority that later drops
// ids no longer present in the current workflow.
func SeedPlanFromRecord(rec *store.RunRecord) SeedPlan {
	var plan SeedPlan
	for _, t := range rec.Tasks {
		if t.Status != store.StatusOK {
			continue
		}
		plan.entries = append(plan.entries, seedEntry{
			id:       workflow.TaskID(t.ID),
			prompt:   t.Prompt,
			command:  t.Command,
			output:   t.Output,
			exitCode: t.ExitCode,
		})
	}
	return plan
}

// resolvedSeed is the seed material a run actually honors, all of it filtered to
// ids still present in the current workflow. seed is the per-task output fed to
// executor.Options.Seed; set is the id set the executor skips and the plan
// annotates; entries is the per-task material stamped into the fresh run record.
// A zero value (all nil) marks a plain run.
type resolvedSeed struct {
	seed    map[workflow.TaskID]string
	set     map[workflow.TaskID]bool
	entries map[workflow.TaskID]seedEntry
}

// Request bundles everything the unified run pipeline consumes: the parsed
// workflow and its inlined manifest, the resolved params, the resolved
// LOOM_HOME, the invocation working directory, the optional seed plan, and the
// provenance. Wf, Manifest, and Resolved arrive together from the check phase,
// so grouping them keeps the pipeline signature stable. A zero Plan and zero
// Prov mark a plain direct run. Cwd is the directory the run is launched from;
// it is stored in the run record so a later resume can restore it.
type Request struct {
	Wf       *workflow.Workflow
	Manifest []byte
	Resolved workflow.ParamValues
	Catalog  runtime.Catalog
	Home     string
	Cwd      string
	Plan     SeedPlan
	Prov     store.Provenance
}

// RunMeta carries the per-run facts a run output prints in its header block
// before execution: the run-record path, the invocation cwd, and the seeded
// task count (0 on a plain run). Total is the progress denominator for the
// per-task lines (the expected task count, i.e. total tasks minus seeded ones);
// the executor never reports it, so the caller threads it in here.
type RunMeta struct {
	RunFile string
	Cwd     string
	Seeded  int
	Total   int
}

// RunOutput is runner's presentation seam. Callers own output construction
// and teardown; the execution core only drives the run header, execution event
// stream, summary, and best-effort store-error reporting.
type RunOutput interface {
	Header(meta RunMeta) error
	Events() run.EventSink
	Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error
	StoreError(err error)
}

// Run is the unified run pipeline shared by doRun, runFromRecord, and the
// daemon. It parses nothing itself; callers hand it the already-parsed workflow
// and resolved params after the check phase has already validated and printed
// the plan. req.Plan carries the optional seed: a zero plan runs the whole
// workflow, while a non-empty plan stamps each seeded task into the fresh run
// record as already-ok and tells the executor to skip them. The store's Close
// error is reported independently so a write failure after a successful run does
// not mask the nil return value. ctx should be a signal-aware context created by
// the caller; Run cancels execution on ctx cancellation.
func Run(ctx context.Context, output RunOutput, req Request) (string, error) {
	if req.Prov.Trigger == "" {
		req.Prov.Trigger = store.TriggerCLI
	}

	// The plan was already printed by the check phase in the caller (doRun /
	// runFromRecord), which validates and prints before any execution. Here we
	// only need the seed material the executor will actually honor.
	rs := resolveSeed(req.Wf, req.Plan)

	// Home is resolved once by the caller (before any resume-time chdir) so a
	// relative LOOM_HOME cannot split the store across two dirs. Cwd is supplied
	// by the caller so it reflects the directory the run was launched from (not
	// the directory Run executes in, which may differ after a resume-time chdir).
	cwd := req.Cwd

	// Load cross-run state once. Each run substitutes `{{state.key}}` from it
	// and folds its `writes_state` outputs back in. A missing file yields an
	// empty map.
	state, err := store.LoadState(req.Home, req.Wf.ID)
	if err != nil {
		return "", err
	}

	x := &runExec{output: output, req: req, state: state}
	return x.execute(ctx, cwd, rs)
}

// ResumeRequest assembles a runner.Request from a prior run record so the
// CLI or daemon can resume it. manifest is the workflow body to execute:
// either the stored manifest embedded in rec (for `loom resume`) or fresh
// bytes read from disk (for `loom run --resume-latest`). selfPath is the
// workflow's on-disk file path when known; it is empty for stored-manifest
// resumes where path refs resolve relative to the restored working directory.
// cliParams are the already-parsed CLI overrides (map from param name to
// value); rec.Params supplies the lower-precedence tier so the original
// run's params are reused without the caller having to re-supply them.
//
// It returns the assembled request with Wf, Manifest, Plan, and Cwd
// (from rec.Cwd) populated, the seeded task set for the plan renderer, and
// any parse error.
//
// The caller is responsible for:
//   - Calling workflow.Link on req.Wf (linking needs the CLI resolver).
//   - Setting req.Home.
//   - Calling os.Chdir(req.Cwd) when needsChdir is true and printing the
//     "Cwd restored" message.
func ResumeRequest(rec *store.RunRecord, manifest []byte, selfPath string, cliParams map[string]string) (req Request, needsChdir bool, err error) {
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return Request{}, false, err
	}

	// Seed every ok task from the record unfiltered; resolveSeed (called inside
	// Run) is the single authority that drops ids no longer present in the
	// current workflow, so no pre-filtering is needed here.
	plan := SeedPlanFromRecord(rec)

	req = Request{
		Wf:       wf,
		Manifest: manifest,
		Plan:     plan,
		Cwd:      rec.Cwd,
		Prov:     store.Provenance{Trigger: store.TriggerCLI},
	}
	// needsChdir is true when the record carried a working directory, signalling
	// that the caller must os.Chdir there before executing so shell tasks and
	// relative paths resolve against the original directory.
	needsChdir = rec.Cwd != ""
	return req, needsChdir, nil
}

// SeededSetFromRequest returns the set of task IDs that Run will skip when
// executing req, so CLI callers can annotate the plan display before calling
// renderCheckRun. It is a convenience wrapper around SeededSet for the resume
// path where the Request has already been assembled.
func SeededSetFromRequest(req Request) map[workflow.TaskID]bool {
	return SeededSet(req.Wf, req.Plan)
}

// SeededSet returns the set of task IDs that Run will skip when executing with
// plan, so cmd callers can annotate the execution-plan display before calling
// Run.
func SeededSet(wf *workflow.Workflow, plan SeedPlan) map[workflow.TaskID]bool {
	return resolveSeed(wf, plan).set
}

// runExec bundles the context that travels unchanged through the run pipeline's
// execute/finalize hops: the output the caller owns, the parsed run request,
// and the cross-run state map (read for substitution, written back in place).
// The per-run locals the executor produces as it goes (cwd, the resolved seed,
// the expected task count) stay as method arguments rather than fields, since
// they are only known partway through.
type runExec struct {
	output RunOutput
	req    Request
	state  map[string]string
}

// execute executes the DAG: it opens a fresh run record, stamps any seeded
// tasks, runs the executor, prints the summary, and folds `writes_state`
// outputs into state. cwd is the invocation directory the caller resolved (the
// one genuinely new root at this layer); rs is the seed material the run honors.
func (x *runExec) execute(ctx context.Context, cwd string, rs resolvedSeed) (runID string, runErr error) {
	wf := x.req.Wf
	record, err := store.Open(wf.ID, x.req.Manifest, store.Config{
		Root:       x.req.Home,
		Cwd:        cwd,
		OnError:    x.output.StoreError,
		Params:     stringifyParams(x.req.Resolved),
		Provenance: x.req.Prov,
	})
	if err != nil {
		return "", err
	}
	runID = record.ID()

	// Close is idempotent and must run even if the executor panics, so defer
	// it. rep and runErr are read at defer time, after the executor returns:
	// rep is a local the closure captures, runErr the named return. The store's
	// Close error is reported but does not mask runErr. The deferred Fprintf has
	// no error channel, so its own write error is intentionally discarded.
	var rep *executor.Report
	defer func() {
		if closeErr := record.Close(summaryFor(rep), runErr); closeErr != nil {
			x.output.StoreError(closeErr)
		}
	}()

	// The renderer is owned by the caller (doRun / runFromRecord), so execute
	// neither creates nor closes it. Header resets the progress counter and
	// records the denominator.
	expected := len(wf.Tasks) - len(rs.set)
	if err := x.output.Header(RunMeta{
		RunFile: record.Path(),
		Cwd:     cwd,
		Seeded:  len(rs.set),
		Total:   expected,
	}); err != nil {
		return runID, err
	}

	// One store-hooks instance drives both the seeded-task stamping and the
	// executor below, so the run is never double-wrapped.
	sh := storeHooks(record)
	stampSeeded(sh, wf, rs)
	seedExit := seededExitCodes(rs)

	rep, runErr = executor.Run(ctx, wf, run.HooksFromSink(run.JoinSinks(
		x.output.Events(),
		storeEvents(record),
	)), executor.Options{
		Params:        x.req.Resolved,
		Seed:          rs.seed,
		SeedExitCodes: seedExit,
		State:         x.state,
		Catalog:       x.req.Catalog,
	})
	runErr = x.finalize(rep, expected, runErr)
	return runID, runErr
}

// finalize renders the run summary and folds `writes_state` outputs back into
// state once the executor returns. It is a no-op when rep is nil (the store
// failed to open). A summary write error does not mask a real run failure: it is
// surfaced only when the run itself otherwise succeeded, so finalize returns
// the (possibly updated) runErr for the caller to propagate.
func (x *runExec) finalize(rep *executor.Report, expected int, runErr error) error {
	if rep == nil {
		return runErr
	}
	wf := x.req.Wf
	if err := x.output.Summary(wf, rep, expected); err != nil && runErr == nil {
		runErr = err
	}
	// Persist write-backs: each task with `writes_state` records its trimmed
	// output under the named key. Only completed tasks appear in rep.Outputs, so
	// a partial run carries over what it managed to produce.
	if persistState(x.state, wf, rep) {
		if err := store.SaveState(x.req.Home, wf.ID, x.state); err != nil {
			x.output.StoreError(err)
		}
	}
	return runErr
}

// persistState folds each `writes_state` task's trimmed output into state and
// reports whether any key changed (so the caller can skip a needless write).
func persistState(state map[string]string, wf *workflow.Workflow, rep *executor.Report) bool {
	changed := false
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.WritesState == "" {
			continue
		}
		out, ok := rep.Outputs[t.ID]
		if !ok {
			continue
		}
		state[t.WritesState] = strings.TrimSpace(out)
		changed = true
	}
	return changed
}

// stringifyParams returns nil for an empty bag so `omitempty` keeps params
// absent from the stored JSON rather than writing an empty object.
func stringifyParams(p workflow.ParamValues) map[string]string {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[string(k)] = v
	}
	return out
}

// toRecord maps an executor.TaskResult to the store.TaskRecord DTO that
// Run.OnFinish consumes. The translation lives here so pkg/store need not
// import pkg/executor.
func toRecord(res executor.TaskResult) store.TaskRecord {
	return store.NewTaskRecord(
		res.Prompt,
		res.Command,
		res.Output,
		res.ExitCode,
		res.Elapsed,
		string(res.Status),
		res.Usage,
	)
}

// storeHooks binds store.Run.OnStart as a direct method value and wraps
// OnFinish with toRecord so seeded task stamping can reuse the same store write
// path as execution events.
func storeHooks(run *store.Run) executor.Hooks {
	return executor.Hooks{
		OnStart: run.OnStart,
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			run.OnFinish(t, iter, toRecord(res), err)
		},
	}
}

// storeEvents records live executor events into the run store.
func storeEvents(record *store.Run) run.EventSink {
	return run.EventSinkFunc(func(e run.Event) {
		switch ev := e.(type) {
		case run.StepStarted:
			record.OnStart(ev.Task, ev.Iteration, ev.Runtime, ev.Model, ev.Effort)
		case run.StepFinished:
			record.OnFinish(ev.Task, ev.Iteration, toRecord(ev.Result), ev.Err)
		}
	})
}

// summaryFor returns nil when rep is nil so store.Run.Close leaves totals unset.
func summaryFor(rep *executor.Report) *store.Summary {
	if rep == nil {
		return nil
	}
	return &store.Summary{Usage: rep.Usage, TaskCount: len(rep.Tasks)}
}

// resolveSeed reduces a SeedPlan to the seed material the run will actually
// honor, dropping ids no longer present in the current workflow. It is the
// single filtering authority: an id that no longer resolves cannot be re-gated,
// stamped, or skipped, so dropping it here keeps the plan annotation, the
// stamped record, the executor's seed map, and its task count consistent. The
// executor ignores Seed keys with no matching task, so callers hand resolveSeed
// every prior-run task unfiltered and let it decide. A zero plan yields a zero
// resolvedSeed.
func resolveSeed(wf *workflow.Workflow, plan SeedPlan) resolvedSeed {
	if len(plan.entries) == 0 {
		return resolvedSeed{}
	}
	rs := resolvedSeed{
		seed:    make(map[workflow.TaskID]string, len(plan.entries)),
		set:     make(map[workflow.TaskID]bool, len(plan.entries)),
		entries: make(map[workflow.TaskID]seedEntry, len(plan.entries)),
	}
	for _, s := range plan.entries {
		if wf.ByID(s.id) == nil {
			continue
		}
		rs.seed[s.id] = s.output
		rs.entries[s.id] = s
		rs.set[s.id] = true
	}
	return rs
}

// stampSeeded records each seeded task into the new run record as an already-ok
// entry (via the store hooks sh) so a future resume of this run can find them.
// The executor fires no hooks for seeded tasks by design, so this mimics what it
// would have done. On a plain run rs.entries is empty and nothing is stamped, so
// the output stays byte-identical. sh is the same hooks instance the executor
// reuses, so the run is never double-wrapped.
func stampSeeded(sh executor.Hooks, wf *workflow.Workflow, rs resolvedSeed) {
	// Stamp in topological (plan) order so a seeded task is never recorded
	// before a seeded dependency it relies on.
	for _, id := range wf.Plan() {
		s, ok := rs.entries[id]
		if !ok {
			continue
		}
		t := wf.ByID(id)
		if t == nil {
			continue
		}
		// StartMeta is the single authority for which task kinds carry runtime
		// metadata: a prompt task reports its effective triple, every other kind
		// the empty ("", "", "") triple, matching the OnStart runTask fires.
		rt, m, e := wf.StartMeta(t)
		sh.OnStart(*t, 0, rt, m, e)
		sh.OnFinish(*t, 0, executor.TaskResult{
			TaskID:   id,
			Prompt:   s.prompt,
			Command:  s.command,
			Output:   s.output,
			ExitCode: s.exitCode,
		}, nil)
	}
}

// seededExitCodes returns the per-seeded-task exit codes to feed the executor as
// SeedExitCodes, so a resumed downstream task's `{{id.exit}}` resolves to the
// recorded code. Every seeded task is recorded (not just non-zero ones),
// mirroring a fresh run where recordResult stores an exit code for every
// completed task: a seeded clean-exit script must resolve to "0", not be left
// verbatim as an unknown reference. It returns nil on a plain run so the
// executor's output stays byte-identical.
func seededExitCodes(rs resolvedSeed) map[workflow.TaskID]int {
	if len(rs.entries) == 0 {
		return nil
	}
	seedExit := make(map[workflow.TaskID]int, len(rs.entries))
	for id, s := range rs.entries {
		seedExit[id] = s.exitCode
	}
	return seedExit
}
