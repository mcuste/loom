package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

func TestEffectiveFallsBackToWorkflow(t *testing.T) {
	wf := &workflow.Workflow{
		Runtime: "test-rt",
		Model:   "m1",
		Effort:  "low",
	}
	t1 := workflow.Task{ID: "a"}
	rt, m, e := wf.Effective(&t1)
	if rt != runtime.Name("test-rt") || m != runtime.Model("m1") || e != runtime.Effort("low") {
		t.Fatalf("Effective = (%q,%q,%q), want (test-rt, m1, low)", rt, m, e)
	}
}

func TestEffectiveTaskOverrides(t *testing.T) {
	wf := &workflow.Workflow{
		Runtime: "test-rt",
		Model:   "m1",
		Effort:  "low",
	}
	t1 := workflow.Task{ID: "a", Model: "m2", Effort: "high"}
	rt, m, e := wf.Effective(&t1)
	if rt != runtime.Name("test-rt") || m != runtime.Model("m2") || e != runtime.Effort("high") {
		t.Fatalf("Effective = (%q,%q,%q), want (test-rt, m2, high)", rt, m, e)
	}
}

func TestSubstitute(t *testing.T) {
	outputs := map[workflow.TaskID]string{
		"a": "Apple",
		"b": "Banana",
	}
	tests := []struct {
		name, in, want string
	}{
		{"single placeholder", "got {{a}}", "got Apple"},
		{"two placeholders", "{{a}} and {{b}}", "Apple and Banana"},
		{"repeated placeholder", "{{a}} {{a}}", "Apple Apple"},
		{"no placeholders", "hello world", "hello world"},
		{"unknown placeholder kept", "got {{c}}", "got {{c}}"},
		{"adjacent text preserved", "x{{a}}y", "xAppley"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workflow.Substitute(tc.in, outputs, nil, nil, nil)
			if got != tc.want {
				t.Fatalf("Substitute(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSubstituteSinglePassParamValue pins edge case #27: a param whose value
// is the literal string "{{a}}" must NOT be re-expanded against the task
// outputs map. Single-pass substitution makes this fall out naturally.
func TestSubstituteSinglePassParamValue(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "Apple"}
	params := workflow.ParamValues{"x": "{{a}}"}
	got := workflow.Substitute("got {{params.x}}", outputs, params, nil, nil)
	if got != "got {{a}}" {
		t.Fatalf("Substitute = %q, want %q (param value must not be re-expanded)", got, "got {{a}}")
	}
}

// TestSubstituteSinglePassTaskOutput pins edge case #10: a task output that
// is the literal string "{{params.token}}" must NOT be re-expanded against
// the params map.
func TestSubstituteSinglePassTaskOutput(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "{{params.token}}"}
	params := workflow.ParamValues{"token": "SECRET"}
	got := workflow.Substitute("got {{a}}", outputs, params, nil, nil)
	if got != "got {{params.token}}" {
		t.Fatalf("Substitute = %q, want %q (task output must not be re-expanded)", got, "got {{params.token}}")
	}
}

func TestSubstituteUnknownParamLeftInPlace(t *testing.T) {
	got := workflow.Substitute("got {{params.ghost}}", nil, workflow.ParamValues{}, nil, nil)
	if got != "got {{params.ghost}}" {
		t.Fatalf("Substitute = %q, want unchanged", got)
	}
}

func TestSubstituteNilParamsMap(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "Apple"}
	got := workflow.Substitute("got {{a}} and {{params.x}}", outputs, nil, nil, nil)
	if got != "got Apple and {{params.x}}" {
		t.Fatalf("Substitute = %q, want %q", got, "got Apple and {{params.x}}")
	}
}

// TestSubstituteStateHit pins that a `{{state.key}}` placeholder resolves to
// the matching state value.
func TestSubstituteStateHit(t *testing.T) {
	state := map[string]string{"done": "item-1"}
	got := workflow.Substitute("skip {{state.done}}", nil, nil, state, nil)
	if got != "skip item-1" {
		t.Fatalf("Substitute = %q, want %q", got, "skip item-1")
	}
}

// TestSubstituteStateMissCollapsesToEmpty pins the first-tick semantics: a
// state key with no entry substitutes to the empty string rather than being
// left verbatim like an unknown task/param placeholder.
func TestSubstituteStateMissCollapsesToEmpty(t *testing.T) {
	got := workflow.Substitute("before {{state.ghost}} after", nil, nil, nil, nil)
	if got != "before  after" {
		t.Fatalf("Substitute = %q, want %q", got, "before  after")
	}
}

// TestSubstituteStateNoDoubleExpansion pins that a state value containing
// placeholder text is not re-expanded against the task/param maps in the same
// single pass.
func TestSubstituteStateNoDoubleExpansion(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "Apple"}
	state := map[string]string{"x": "{{a}}"}
	got := workflow.Substitute("got {{state.x}}", outputs, nil, state, nil)
	if got != "got {{a}}" {
		t.Fatalf("Substitute = %q, want %q (state value must not be re-expanded)", got, "got {{a}}")
	}
}

// TestSubstituteAllFourNamespaces pins that task, param, state, and prev
// placeholders are spliced together in one pass.
func TestSubstituteAllFourNamespaces(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "A"}
	params := workflow.ParamValues{"p": "P"}
	state := map[string]string{"s": "S"}
	prev := map[workflow.TaskID]string{"r": "R"}
	got := workflow.Substitute("{{a}}-{{params.p}}-{{state.s}}-{{prev.r}}", outputs, params, state, prev)
	if got != "A-P-S-R" {
		t.Fatalf("Substitute = %q, want %q", got, "A-P-S-R")
	}
}

// resolveWF builds a Workflow with the given params (declaration order
// preserved) without going through Parse, keeps these tests focused on
// resolver behavior.
func resolveWF(params ...workflow.Param) *workflow.Workflow {
	return &workflow.Workflow{ID: "wf", Params: params}
}

func TestResolveDefaultsOnly(t *testing.T) {
	wf := resolveWF(
		workflow.Param{Name: "env", Default: "dev", HasDefault: true},
		workflow.Param{Name: "tag", Default: "latest", HasDefault: true},
	)
	got, err := workflow.ResolveParams(wf, nil, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	want := workflow.ParamValues{"env": "dev", "tag": "latest"}
	if len(got) != len(want) || got["env"] != "dev" || got["tag"] != "latest" {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestResolveCLIBeatsFileBeatsDefault(t *testing.T) {
	wf := resolveWF(workflow.Param{Name: "env", Default: "dev", HasDefault: true})
	got, err := workflow.ResolveParams(wf,
		map[string]string{"env": "cli"},
		map[string]string{"env": "file"},
	)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if got["env"] != "cli" {
		t.Errorf("got[env] = %q, want cli (CLI must win over file and default)", got["env"])
	}

	// file beats default
	got2, _ := workflow.ResolveParams(wf, nil, map[string]string{"env": "file"})
	if got2["env"] != "file" {
		t.Errorf("got[env] = %q, want file", got2["env"])
	}
}

func TestResolveMissingRequired(t *testing.T) {
	wf := resolveWF(workflow.Param{Name: "env", Required: true})
	_, err := workflow.ResolveParams(wf, nil, nil)
	var got *workflow.MissingRequiredParamError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MissingRequiredParamError failed; err = %v", err)
	}
	if got.Name != "env" {
		t.Errorf("MissingRequiredParamError.Name = %q, want env", got.Name)
	}
}

func TestResolveUnknownCLIKey(t *testing.T) {
	wf := resolveWF(workflow.Param{Name: "env", Default: "dev", HasDefault: true})
	_, err := workflow.ResolveParams(wf, map[string]string{"ghost": "x"}, nil)
	var got *workflow.UnknownCLIParamError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownCLIParamError failed; err = %v", err)
	}
	if got.Name != "ghost" {
		t.Errorf("UnknownCLIParamError.Name = %q, want ghost", got.Name)
	}
}

// TestResolveEmptyValueSatisfiesRequired pins that `-p env=` provides the
// empty string and satisfies a required param.
func TestResolveEmptyValueSatisfiesRequired(t *testing.T) {
	wf := resolveWF(workflow.Param{Name: "env", Required: true})
	got, err := workflow.ResolveParams(wf, map[string]string{"env": ""}, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if v, ok := got["env"]; !ok || v != "" {
		t.Errorf("got = %v, want env=\"\"", got)
	}
}

func TestParseParamArgsHappy(t *testing.T) {
	got, err := workflow.ParseParamArgs([]string{"env=prod", "url=https://x/y=z"})
	if err != nil {
		t.Fatalf("ParseParamArgs: %v", err)
	}
	if got["env"] != "prod" || got["url"] != "https://x/y=z" {
		t.Errorf("got = %v, want {env: prod, url: https://x/y=z}", got)
	}
}

func TestParseParamArgsEmpty(t *testing.T) {
	got, err := workflow.ParseParamArgs(nil)
	if err != nil {
		t.Fatalf("ParseParamArgs(nil): %v", err)
	}
	if got != nil {
		t.Errorf("ParseParamArgs(nil) = %v, want nil", got)
	}
}

func TestParseParamArgsMalformed(t *testing.T) {
	cases := []string{"env", "=val", "bad-key=v", "with.dot=v"}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			_, err := workflow.ParseParamArgs([]string{arg})
			var got *workflow.MalformedParamArgError
			if !errors.As(err, &got) {
				t.Fatalf("errors.As MalformedParamArgError failed for %q; err = %v", arg, err)
			}
		})
	}
}

func TestParseParamArgsDuplicate(t *testing.T) {
	_, err := workflow.ParseParamArgs([]string{"env=a", "env=b"})
	var got *workflow.DuplicateCLIParamError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As DuplicateCLIParamError failed; err = %v", err)
	}
	if got.Name != "env" {
		t.Errorf("DuplicateCLIParamError.Name = %q, want env", got.Name)
	}
}

func TestParseParamArgsRejectsNUL(t *testing.T) {
	_, err := workflow.ParseParamArgs([]string{"env=ok\x00bad"})
	var got *workflow.InvalidParamValueError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidParamValueError failed; err = %v", err)
	}
}

func TestParseParamArgsRejectsNonUTF8(t *testing.T) {
	bad := "env=" + string([]byte{0xff, 0xfe})
	_, err := workflow.ParseParamArgs([]string{bad})
	var got *workflow.InvalidParamValueError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidParamValueError failed; err = %v", err)
	}
}
