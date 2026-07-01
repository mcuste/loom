package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
)

// eval runs a compiled sub-workflow task by resolving its linked child, then
// executing that child via the public Run path.
func (subWorkflowOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.task
	child := i.program.wf.Subs[t.ID]
	if child == nil {
		return TaskResult{}, nil, fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, t.Workflow)
	}
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	vals := make(map[string]string, len(t.With))
	if action, ok := t.ParsedAction(); ok {
		if wfAction, ok := action.(workflow.WorkflowAction); ok {
			for _, a := range wfAction.WithTemplates {
				vals[string(a.Name)] = renderTemplate(a.Value, st, i.opts)
			}
		}
	} else {
		for _, a := range t.With {
			vals[string(a.Name)] = workflow.Substitute(bindLoopVar(a.Value, st), st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes)
		}
	}
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
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
