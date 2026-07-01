package workflowcheck

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// Validate checks every LLM task's effective runtime/model/effort against the
// supplied runtime catalog, recursing into linked sub-workflows.
//
// Whole-field `{{params.name}}` substitutions in runtime/model/effort are
// resolved from params first. When allowUnresolved is true, any task whose
// routing still depends on a missing param is skipped so advisory callers can
// still render a plan for workflows with required params.
func Validate(wf *workflow.Workflow, params workflow.ParamValues, catalog runtime.Validator, allowUnresolved bool) error {
	if catalog == nil {
		catalog = runtime.Default()
	}
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.IsShell() || t.IsSubWorkflow() || t.IsScript() {
			continue
		}
		if allowUnresolved && routingNeedsMissingParam(wf, t, params) {
			continue
		}
		rt, model, effort := wf.EffectiveWithParams(t, params)
		req := runtime.Request{
			TaskID:       string(t.ID),
			Prompt:       t.Prompt,
			Model:        model,
			Effort:       effort,
			SystemPrompt: wf.EffectiveSystemPrompt(t),
		}
		if err := catalog.Validate(rt, req); err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
	}
	for _, child := range wf.Subs {
		childParams, _ := workflow.ResolveParams(child, nil, nil)
		if err := Validate(child, childParams, catalog, true); err != nil {
			return err
		}
	}
	return nil
}

// ResolveAndValidateParams applies the non-advisory scheduling/execution path:
// resolve params from defaults, file, and CLI tiers, then validate routing
// against the fully resolved bag with the supplied runtime catalog.
func ResolveAndValidateParams(wf *workflow.Workflow, cli, file map[string]string, catalog runtime.Validator) (workflow.ParamValues, error) {
	out, err := workflow.ResolveParams(wf, cli, file)
	if err != nil {
		return nil, err
	}
	if err := Validate(wf, out, catalog, false); err != nil {
		return nil, err
	}
	return out, nil
}

func routingNeedsMissingParam(wf *workflow.Workflow, t *workflow.Task, params workflow.ParamValues) bool {
	for _, value := range []string{
		string(t.Runtime), string(wf.Runtime),
		string(t.Model), string(wf.Model),
		string(t.Effort), string(wf.Effort),
	} {
		name, ok := wholeParamPlaceholder(value)
		if !ok {
			continue
		}
		if _, found := params[name]; !found {
			return true
		}
	}
	return false
}

func wholeParamPlaceholder(s string) (workflow.ParamName, bool) {
	name, ok := workflow.PlaceholderParamName(s)
	if !ok {
		return "", false
	}
	return workflow.ParamName(name), true
}
