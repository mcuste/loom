package workflow

import "github.com/mcuste/loom/pkg/syntax"

// taskBodyDecl groups the mutually exclusive YAML body fields before they are
// validated and lowered into a typed Action. Keeping the body as one parser
// declaration makes taskDecl about task metadata rather than a bag of optional
// executable forms.
type taskBodyDecl struct {
	prompt     string
	promptFile string
	command    string
	workflow   string
	script     string
	args       []string
	with       syntax.Value
}

func newTaskBodyDecl(rt syntax.DraftTask) taskBodyDecl {
	return taskBodyDecl{
		prompt:     rt.Prompt,
		promptFile: rt.PromptFile,
		command:    rt.Command,
		workflow:   rt.Workflow,
		script:     rt.Script,
		args:       append([]string(nil), rt.Args...),
		with:       rt.With,
	}
}

func (b taskBodyDecl) hasWith() bool { return b.with.Present() }
func (b taskBodyDecl) hasArgs() bool { return len(b.args) > 0 }

func (b taskBodyDecl) isCommand() bool     { return b.command != "" }
func (b taskBodyDecl) isScript() bool      { return b.script != "" }
func (b taskBodyDecl) isSubWorkflow() bool { return b.workflow != "" }

// bodyForm describes one of the mutually exclusive task body forms.
// The conflict rules are encoded in the table once; parser and inliner both
// consult it instead of open-coding their own lists.
type bodyForm struct {
	// field is the YAML key name used in error messages and detection.
	field string
}

// bodyForms lists every mutually exclusive task body form in declaration order.
// Adding a new form requires exactly one entry here.
var bodyForms = []bodyForm{
	{field: "prompt"},
	{field: "prompt_file"},
	{field: "command"},
	{field: "workflow"},
	{field: "script"},
}

// detectBodyForms returns the names of the body-form fields that are set on rt,
// in declaration order. The caller signals a conflict when len > 1 and missing
// when len == 0.
func detectBodyForms(rt taskDecl) []string {
	return rt.body.presentForms()
}

func (b taskBodyDecl) presentForms() []string {
	var present []string
	if b.prompt != "" {
		present = append(present, "prompt")
	}
	if b.promptFile != "" {
		present = append(present, "prompt_file")
	}
	if b.command != "" {
		present = append(present, "command")
	}
	if b.workflow != "" {
		present = append(present, "workflow")
	}
	if b.script != "" {
		present = append(present, "script")
	}
	return present
}

// isBodyFormKey reports whether key is one of the mutually exclusive task body
// form keys, used by the inliner to detect conflicts with prompt_file.
func isBodyFormKey(key string) bool {
	for _, bf := range bodyForms {
		if bf.field == key {
			return true
		}
	}
	return false
}
