package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

func TestTemplateStringRefsAndRender(t *testing.T) {
	t.Parallel()

	tpl := workflow.ParseTemplateInScope("{{params.name}} {{state.x}} {{prev.a}} {{a}} {{a.exit}} {{item}}", "item")
	if got := tpl.String(); got != "{{params.name}} {{state.x}} {{prev.a}} {{a}} {{a.exit}} {{item}}" {
		t.Fatalf("String = %q", got)
	}
	refs := tpl.Refs()
	if len(refs) != 6 {
		t.Fatalf("Refs len = %d, want 6", len(refs))
	}
	if _, ok := refs[0].(workflow.ParamRef); !ok {
		t.Fatalf("refs[0] = %T, want ParamRef", refs[0])
	}
	if _, ok := refs[1].(workflow.StateRef); !ok {
		t.Fatalf("refs[1] = %T, want StateRef", refs[1])
	}
	if _, ok := refs[2].(workflow.PrevRef); !ok {
		t.Fatalf("refs[2] = %T, want PrevRef", refs[2])
	}
	if _, ok := refs[3].(workflow.TaskOutputRef); !ok {
		t.Fatalf("refs[3] = %T, want TaskOutputRef", refs[3])
	}
	if _, ok := refs[4].(workflow.TaskExitRef); !ok {
		t.Fatalf("refs[4] = %T, want TaskExitRef", refs[4])
	}
	if _, ok := refs[5].(workflow.LoopVarRef); !ok {
		t.Fatalf("refs[5] = %T, want LoopVarRef", refs[5])
	}

	got := tpl.Render(workflow.RenderContext{
		Outputs:   map[workflow.TaskID]string{"a": "out"},
		Params:    workflow.ParamValues{"name": "param"},
		State:     map[string]string{"x": "state"},
		Prev:      map[workflow.TaskID]string{"a": "prev"},
		ExitCodes: map[workflow.TaskID]int{"a": 7},
		LoopVars:  map[string]string{"item": "loop"},
	})
	if got != "param state prev out 7 loop" {
		t.Fatalf("Render = %q", got)
	}
}

func TestParsedWorkflowCarriesActions(t *testing.T) {
	t.Parallel()

	wf, err := workflow.Parse([]byte(`name: demo
tasks:
  - id: a
    prompt: hi {{params.name}}
params:
  - name: name
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	action, ok := wf.Tasks[0].Action().(workflow.PromptAction)
	if !ok {
		t.Fatalf("Action = %T, want PromptAction", wf.Tasks[0].Action())
	}
	if action.Prompt.String() != "hi {{params.name}}" {
		t.Fatalf("Prompt.String = %q", action.Prompt.String())
	}
}
