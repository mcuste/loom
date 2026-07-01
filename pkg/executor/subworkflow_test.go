package executor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// releaseChild is a two-task child workflow whose explicit `output:` selects
// the publish task. It uses the exec-echo runtime (returns the prompt verbatim
// with fixed usage), so a build of "1.4.0" yields a deterministic result and a
// predictable usage sum. Hand-built so the test exercises the executor dispatch
// branch independently of parse support for `workflow:`/`output:`.
func releaseChild() *workflow.Workflow {
	return &workflow.Workflow{
		ID:      "release",
		Runtime: "exec-echo",
		Model:   "m1",
		Output:  "publish",
		Params:  []workflow.Param{{Name: "version", Required: true}},
		Tasks: []workflow.Task{
			{ID: "build", Prompt: "build {{params.version}}"},
			{ID: "publish", Prompt: "publish {{build}}", DependsOn: []workflow.TaskID{"build"}},
		},
	}
}

func childMissingRuntime() *workflow.Workflow {
	return &workflow.Workflow{
		ID: "release",
		Tasks: []workflow.Task{
			{ID: "build", Prompt: "build"},
		},
	}
}

// TestRunSubWorkflowLeaf pins the runtime-nesting model: a parent whose Subs
// holds a child runs the child as a leaf, the parent task's output equals the
// child's output-task value (release's publish), and the parent usage is the
// sum of the child's task usage.
func TestRunSubWorkflowLeaf(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "1.4.0"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// publish prompt "publish {{build}}" with build = "build 1.4.0".
	const want = "publish build 1.4.0"
	if rep.Outputs["cut"] != want {
		t.Errorf("Outputs[cut] = %q, want %q (child output-task value)", rep.Outputs["cut"], want)
	}
	if len(rep.Tasks) != 1 || rep.Tasks[0].TaskID != "cut" {
		t.Errorf("rep.Tasks = %+v, want a single row for the cut task (no per-child rows in v1)", rep.Tasks)
	}
	// exec-echo reports {in:10, out:20, cost:0.001} per task; the child ran two
	// tasks, and the parent must accumulate their summed usage.
	if rep.Usage.InputTokens != 20 || rep.Usage.OutputTokens != 40 {
		t.Errorf("Usage = %+v, want summed 20 in / 40 out from the child", rep.Usage)
	}
	if rep.Usage.TotalCostUSD != 0.002 {
		t.Errorf("Usage.TotalCostUSD = %v, want 0.002 (summed child cost)", rep.Usage.TotalCostUSD)
	}
}

// TestRunSubWorkflowDefaultSink pins that with no explicit child Output, the
// sub-workflow result is the child's lone sink output.
func TestRunSubWorkflowDefaultSink(t *testing.T) {
	child := releaseChild()
	child.Output = "" // fall back to the lone sink (publish has no dependents)

	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "9.9.9"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["cut"] != "publish build 9.9.9" {
		t.Errorf("Outputs[cut] = %q, want %q (default-sink result)", rep.Outputs["cut"], "publish build 9.9.9")
	}
}

// TestRunSubWorkflowNested pins the multi-level threading path: a grandparent
// links a mid workflow whose own Subs link the leaf release child. The executor
// reads each level's own wf.Subs, so the deepest leaf still resolves without the
// caller threading any links through Options, and usage propagates up unchanged.
func TestRunSubWorkflowNested(t *testing.T) {
	leaf := releaseChild()
	mid := &workflow.Workflow{
		ID:      "mid",
		Runtime: "exec-echo",
		Model:   "m1",
		Output:  "cut",
		Params:  []workflow.Param{{Name: "version", Required: true}},
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "{{params.version}}"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": leaf},
	}
	top := &workflow.Workflow{
		ID:      "top",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "outer", Workflow: "mid", With: []workflow.WithArg{{Name: "version", Value: "3.3.3"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"outer": mid},
	}

	rep, err := executor.Run(context.Background(), top, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["outer"] != "publish build 3.3.3" {
		t.Errorf("Outputs[outer] = %q, want %q (leaf result threaded up two levels)", rep.Outputs["outer"], "publish build 3.3.3")
	}
	// Only the leaf runs real (echo) tasks: 2 tasks at 10 in / 20 out each. The
	// mid and top levels each accumulate that summed usage without adding more.
	if rep.Usage.InputTokens != 20 || rep.Usage.OutputTokens != 40 {
		t.Errorf("Usage = %+v, want summed 20 in / 40 out from the leaf", rep.Usage)
	}
}

// TestRunSubWorkflowSchemaWraps pins that per-task schema validation wraps the
// sub-workflow result uniformly: the child's plain-text output is not valid
// JSON, so a parent task carrying an object schema must fail the run.
func TestRunSubWorkflowSchemaWraps(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{
				ID:       "cut",
				Workflow: "release",
				With:     []workflow.WithArg{{Name: "version", Value: "1.0.0"}},
				Schema:   &workflow.Schema{Type: "object"},
			},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	if _, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{}); err == nil {
		t.Fatal("Run returned nil error; want schema-validation failure on the sub-workflow result")
	}
}

// TestRunSubWorkflowSubstitutesWithValues pins that `with:` values are
// substituted against the PARENT context before being passed to the child: a
// `{{seed}}` with-value must resolve to the upstream parent task's output.
func TestRunSubWorkflowSubstitutesWithValues(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "seed", Prompt: "2.0.0"},
			{
				ID:        "cut",
				Workflow:  "release",
				DependsOn: []workflow.TaskID{"seed"},
				With:      []workflow.WithArg{{Name: "version", Value: "{{seed}}"}},
			},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["cut"] != "publish build 2.0.0" {
		t.Errorf("Outputs[cut] = %q, want %q (with-value substituted from parent ctx)", rep.Outputs["cut"], "publish build 2.0.0")
	}
}

// TestRunSubWorkflowBindsLoopVarInWithValues pins that a for_each loop variable
// is bound into a sub-workflow member's `with:` values, exactly as it is in a
// plain task's prompt or command. Without the bindLoopVar pass on the
// with-values, `{{ver}}` would reach the child verbatim and every iteration
// would build the literal placeholder instead of the element.
func TestRunSubWorkflowBindsLoopVarInWithValues(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "{{ver}}"}}},
		},
		Loops: []workflow.LoopGroup{{
			ID:      "fan",
			Kind:    workflow.LoopForEach,
			List:    []string{"1.0.0", "2.0.0"},
			As:      "ver",
			Members: []workflow.TaskID{"cut"},
		}},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A sequential for_each publishes the final element's result; the loop var
	// must have been spliced into the with-value before reaching the child.
	if rep.Outputs["cut"] != "publish build 2.0.0" {
		t.Errorf("Outputs[cut] = %q, want %q (loop var bound into with-value)", rep.Outputs["cut"], "publish build 2.0.0")
	}
}

// TestRunSubWorkflowRejectsMissingRequiredParam pins the child param-resolution
// failure path: the parent dispatch must surface the child's missing required
// param before any child task runs.
func TestRunSubWorkflowRejectsMissingRequiredParam(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release"},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	_, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error; want missing required child param failure")
	}
	var miss *workflow.MissingRequiredParamError
	if !errors.As(err, &miss) {
		t.Fatalf("errors.As MissingRequiredParamError failed; err = %v", err)
	}
	if miss.Name != "version" {
		t.Fatalf("MissingRequiredParamError.Name = %q, want version", miss.Name)
	}
}

// TestRunSubWorkflowRejectsBadRouting pins the child routing-validation
// failure path: the parent dispatch must surface the child's missing runtime
// before any child task runs.
func TestRunSubWorkflowRejectsBadRouting(t *testing.T) {
	child := childMissingRuntime()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release"},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	_, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error; want child routing-validation failure")
	}
	if !errors.Is(err, runtime.ErrMissingRuntime) {
		t.Fatalf("err = %v, want missing runtime", err)
	}
}
