package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflowcheck"
)

// eval runs a compiled sub-workflow task by resolving its linked child, then
// executing that child via the public Run path.
func (subWorkflowOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := &n.task
	action, ok := n.action.(plan.CallWorkflow)
	if !ok {
		return TaskResult{}, nil, invalidActionError(n, "sub-workflow call")
	}
	child := i.program.wf.Subs[t.ID]
	if child == nil {
		return TaskResult{}, nil, fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, action.Ref)
	}
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	vals := renderWorkflowArgs(action, st, i.opts)
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t.ID, n.policy.Retry, n.policy.Budget, baseDelay, func() (TaskResult, error) {
		start := time.Now()
		cp, err := workflowcheck.ResolveAndValidateParams(child, vals, nil, catalogValidator(i.opts))
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		childRep, err := Run(ctx, child, Hooks{}, Options{
			Params:         cp,
			State:          i.opts.State,
			Cache:          i.opts.Cache,
			WorkDir:        st.workDir,
			Catalog:        i.opts.Catalog,
			Resolver:       i.opts.Resolver,
			RetryBaseDelay: i.opts.RetryBaseDelay,
		})
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		outputTask, err := child.OutputTask()
		if err != nil {
			return TaskResult{TaskID: t.ID}, err
		}
		out := childRep.Outputs[outputTask]
		// One parent row, child result + SUMMED child usage; schema (if any)
		// validates the child result uniformly with the LLM branch.
		r := TaskResult{TaskID: t.ID, Output: out, Usage: childRep.Usage, Elapsed: time.Since(start)}
		return r, evaluateSchemaGate(ctx, t, n.policy.Schema, out, r.ExitCode)
	})
	return res, runErr, nil
}

func renderWorkflowArgs(action plan.CallWorkflow, st *frame, opts Options) map[string]string {
	vals := make(map[string]string, len(action.WithTemplates))
	for _, arg := range action.WithTemplates {
		vals[string(arg.Name)] = renderTemplate(arg.Value, st, opts)
	}
	return vals
}

func catalogValidator(opts Options) runtime.Validator {
	if opts.Catalog != nil {
		return opts.Catalog
	}
	return runtime.Default()
}
