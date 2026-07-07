package workflow

import (
	"testing"

	"github.com/mcuste/loom/pkg/syntax"
)

// TestSyntaxDraftCapturesSemanticBlocksRaw pins the syntax/workflow boundary:
// YAML-only fields are decoded as raw syntax values first, then interpreted by
// the workflow parser. The syntax structs intentionally do not mirror Workflow
// and Task field-for-field.
func TestSyntaxDraftCapturesSemanticBlocksRaw(t *testing.T) {
	draft, err := syntax.Decode([]byte(`
name: wf
params:
  - name: topic
    default: 1
budget:
  max_cost_usd: 1.25
tasks:
  - id: a
    prompt: "{{params.topic}}"
    retry:
      max: 1
    schema:
      type: object
`), syntax.Source{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !draft.Params.Present() || draft.Params.Kind() != syntax.SequenceNode {
		t.Fatalf("Params raw kind = (%v, %v), want present sequence", draft.Params.Present(), draft.Params.Kind())
	}
	if !draft.Budget.Present() || draft.Budget.Kind() != syntax.MappingNode {
		t.Fatalf("Budget raw kind = (%v, %v), want present mapping", draft.Budget.Present(), draft.Budget.Kind())
	}
	if len(draft.Tasks) != 1 {
		t.Fatalf("Tasks len = %d, want 1", len(draft.Tasks))
	}
	task := draft.Tasks[0]
	if !task.Retry.Present() || task.Retry.Kind() != syntax.MappingNode {
		t.Fatalf("Retry raw kind = (%v, %v), want present mapping", task.Retry.Present(), task.Retry.Kind())
	}
	if !task.Schema.Present() || task.Schema.Kind() != syntax.MappingNode {
		t.Fatalf("Schema raw kind = (%v, %v), want present mapping", task.Schema.Present(), task.Schema.Kind())
	}
}

// TestPlaceholderRegexMatchesIdentifierAlphabet pins the contract that every
// string accepted as a TaskID/ParamName by identifierRe is also captured by
// every `{{...}}` form recognized by the parser, and that strings rejected by
// identifierRe are also rejected by every placeholder regex. If someone widens
// or narrows identifierClass without updating one regex, this test fails.
func TestPlaceholderRegexMatchesIdentifierAlphabet(t *testing.T) {
	accept := []string{"a", "A", "task1", "joke", "summary_v2", "_under", "123abc"}
	reject := []string{"", "with-dash", "with.dot", "with space", "with$dollar", "with/slash"}

	for _, s := range accept {
		if !identifierRe.MatchString(s) {
			t.Errorf("identifierRe rejected %q; update the accept list if intended", s)
			continue
		}
		if m := placeholderRe.FindStringSubmatch("{{" + s + "}}"); len(m) != 2 || m[1] != s {
			t.Errorf("placeholderRe did not capture %q from {{%s}}; got %v — regex drift", s, s, m)
		}
		if m := paramPlaceholderRe.FindStringSubmatch("{{params." + s + "}}"); len(m) != 2 || m[1] != s {
			t.Errorf("paramPlaceholderRe did not capture %q from {{params.%s}}; got %v — regex drift", s, s, m)
		}
		// combinedPlaceholderRe distinguishes branches via capture groups: group
		// 1 is the param name, group 2 is the state key, group 3 is the prev id,
		// group 4 is the bare task id, group 5 is the task id of an `{{id.exit}}`
		// reference.
		if m := combinedPlaceholderRe.FindStringSubmatch("{{params." + s + "}}"); len(m) != 6 || m[1] != s || m[2] != "" || m[3] != "" || m[4] != "" || m[5] != "" {
			t.Errorf("combinedPlaceholderRe param branch dropped %q from {{params.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{state." + s + "}}"); len(m) != 6 || m[2] != s || m[1] != "" || m[3] != "" || m[4] != "" || m[5] != "" {
			t.Errorf("combinedPlaceholderRe state branch dropped %q from {{state.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{prev." + s + "}}"); len(m) != 6 || m[3] != s || m[1] != "" || m[2] != "" || m[4] != "" || m[5] != "" {
			t.Errorf("combinedPlaceholderRe prev branch dropped %q from {{prev.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{" + s + "}}"); len(m) != 6 || m[4] != s || m[1] != "" || m[2] != "" || m[3] != "" || m[5] != "" {
			t.Errorf("combinedPlaceholderRe task branch dropped %q from {{%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{" + s + ".exit}}"); len(m) != 6 || m[5] != s || m[1] != "" || m[2] != "" || m[3] != "" || m[4] != "" {
			t.Errorf("combinedPlaceholderRe exit branch dropped %q from {{%s.exit}}; got %v — regex drift", s, s, m)
		}
	}
	for _, s := range reject {
		if identifierRe.MatchString(s) {
			t.Errorf("identifierRe accepted %q; update the reject list if intended", s)
			continue
		}
		if m := placeholderRe.FindStringSubmatch("{{" + s + "}}"); m != nil {
			t.Errorf("placeholderRe matched {{%s}} for a non-identifier string; got %v — regex drift", s, m)
		}
		if m := paramPlaceholderRe.FindStringSubmatch("{{params." + s + "}}"); m != nil {
			t.Errorf("paramPlaceholderRe matched {{params.%s}} for a non-identifier string; got %v — regex drift", s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{params." + s + "}}"); m != nil {
			t.Errorf("combinedPlaceholderRe param branch matched {{params.%s}}; got %v — regex drift", s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{state." + s + "}}"); m != nil {
			t.Errorf("combinedPlaceholderRe state branch matched {{state.%s}}; got %v — regex drift", s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{prev." + s + "}}"); m != nil {
			t.Errorf("combinedPlaceholderRe prev branch matched {{prev.%s}}; got %v — regex drift", s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{" + s + "}}"); m != nil {
			t.Errorf("combinedPlaceholderRe task branch matched {{%s}}; got %v — regex drift", s, m)
		}
	}
}
