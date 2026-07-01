package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
)

// dispatchSubWorkflow dispatches a sub-workflow task: it recursively runs the linked
// child via Run and captures its result. It is the BodySubWorkflow arm of dispatch.
func dispatchSubWorkflow(ctx context.Context, wf *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options, baseDelay time.Duration) (TaskResult, error, error) {
	// A sub-workflow task is a leaf in this DAG: at dispatch it recursively
	// runs the linked child via Run and captures its result. The child brings
	// its own runtime, so there is no runtime.Lookup here (like a shell task).
	// wf.Subs is the authoritative link source: the executor already holds the
	// parent workflow, so callers need not copy the links into Options.
	child := wf.Subs[t.ID]
	if child == nil {
		return TaskResult{}, nil, fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, t.Workflow)
	}
	if hooks.OnStart != nil {
		hooks.OnStart(*t, st.iteration, "", "", "")
	}
	// with-values are substituted against the PARENT context first, then
	// handed to the child as its CLI-tier param values.
	st.mu.Lock()
	vals := make(map[string]string, len(t.With))
	for _, a := range t.With {
		vals[string(a.Name)] = workflow.Substitute(bindLoopVar(a.Value, st), st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes)
	}
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
		start := time.Now()
		cp, err := workflowcheck.ResolveAndValidateParams(child, vals, nil, catalogValidator(opts))
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		childRep, err := Run(ctx, child, Hooks{}, Options{
			Params: cp,
			// Child shares the parent's cross-run state so {{state.x}} placeholders in
			// child prompts resolve the same store. Write-back (writes_state) is a
			// CLI-layer pass over the top-level report only, so the child never mutates
			// the map here.
			State:          opts.State,
			Cache:          opts.Cache,
			RetryBaseDelay: opts.RetryBaseDelay,
			// The parent's effective cwd is the child's inherited fallback; the
			// child's own working_dir (if any) overrides it inside Run.
			WorkDir:  st.workDir,
			Catalog:  opts.Catalog,
			Resolver: opts.Resolver,
		})
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		ot, err := child.OutputTask()
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		out := childRep.Outputs[ot]
		// One parent row, child result + SUMMED child usage; schema (if any)
		// validates the child result uniformly with the LLM branch.
		r := TaskResult{TaskID: t.ID, Output: out, Usage: childRep.Usage, Elapsed: time.Since(start)}
		return r, validateSchema(t, out)
	})
	return res, runErr, nil
}

func catalogValidator(opts Options) runtime.Validator {
	if opts.Catalog != nil {
		return opts.Catalog
	}
	return runtime.Default()
}
