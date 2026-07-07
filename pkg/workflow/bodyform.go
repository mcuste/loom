package workflow

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
	var present []string
	if rt.prompt != "" {
		present = append(present, "prompt")
	}
	if rt.promptFile != "" {
		present = append(present, "prompt_file")
	}
	if rt.command != "" {
		present = append(present, "command")
	}
	if rt.workflow != "" {
		present = append(present, "workflow")
	}
	if rt.script != "" {
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
