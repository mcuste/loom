package workflow

import (
	"reflect"
	"testing"
)

// TestRawMirrorsPublic guards against silent drift between the YAML-decode
// structs (rawWorkflow, rawTask) and the validated domain types (Workflow,
// Task): adding or renaming a field on one without the other would otherwise
// be a silent data loss. Aliases capture intentional renamings (e.g. YAML
// `name:` decodes into rawWorkflow.Name but lives on Workflow.ID).
func TestRawMirrorsPublic(t *testing.T) {
	cases := []struct {
		name        string
		raw         any
		public      any
		aliases     map[string]string   // raw field name -> public field name
		extraPublic map[string]struct{} // public fields with no raw counterpart (derived)
		extraRaw    map[string]struct{} // raw fields with no public counterpart (parse-only)
	}{
		{
			name:    "workflow",
			raw:     rawWorkflow{},
			public:  Workflow{},
			aliases: map[string]string{"Name": "ID"},
			// Loops is derived: there is no top-level `loops:` YAML key. Loops are
			// declared inline as tasks carrying a `loop:` block (rawTask.Loop) and
			// folded into Workflow.Loops during parse, so the public field has no
			// rawWorkflow counterpart.
			extraPublic: map[string]struct{}{"Loops": {}},
		},
		{
			name:   "task",
			raw:    rawTask{},
			public: Task{},
			// ForEachSource is derived by parseForEach from the raw `for_each`
			// scalar (the dynamic-fanout case); a static `for_each` sequence
			// decodes into ForEach instead. Cond is compiled by ParseCondition
			// from the raw `when:` text. Task.Loop is derived from the enclosing
			// `loops:` entry (or `loop:` wrapper) a task is nested under, not a
			// task-level YAML key. None has a separate raw field.
			extraPublic: map[string]struct{}{"ForEachSource": {}, "Cond": {}, "Loop": {}},
			// rawTask.Loop is the per-task `loop:` block: a parse-only wrapper that
			// is folded into a LoopGroup and never becomes a Task, so it has no
			// public counterpart (it shares the name with the derived Task.Loop
			// above only by coincidence).
			extraRaw: map[string]struct{}{"Loop": {}},
		},
		{
			name:   "param",
			raw:    rawParam{},
			public: Param{},
			// `default:` is decoded by hand from the raw yaml.Node so the
			// scalar text survives without coercion (e.g. `default: 1` stays
			// "1"); it has no rawParam field. Default and HasDefault both
			// receive their values that way.
			extraPublic: map[string]struct{}{"Default": {}, "HasDefault": {}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFieldParity(t, reflect.TypeOf(tc.raw), reflect.TypeOf(tc.public), tc.aliases, tc.extraPublic, tc.extraRaw)
		})
	}
}

func assertFieldParity(t *testing.T, raw, public reflect.Type, aliases map[string]string, extraPublic, extraRaw map[string]struct{}) {
	t.Helper()

	pubFields := map[string]struct{}{}
	for f := range public.Fields() {
		// Unexported fields are internal caches (e.g. parse-built indices), not
		// part of the YAML-to-domain schema, and must not trigger drift alarms.
		if !f.IsExported() {
			continue
		}
		if _, ok := extraPublic[f.Name]; ok {
			continue
		}
		pubFields[f.Name] = struct{}{}
	}

	for rf := range raw.Fields() {
		if _, ok := extraRaw[rf.Name]; ok {
			continue
		}
		want := rf.Name
		if alias, ok := aliases[rf.Name]; ok {
			want = alias
		}
		if _, ok := pubFields[want]; !ok {
			t.Errorf("%s.%s has no matching field %q on %s — schema drift",
				raw.Name(), rf.Name, want, public.Name())
			continue
		}
		delete(pubFields, want)
	}
	for name := range pubFields {
		t.Errorf("%s.%s has no matching field on %s — schema drift",
			public.Name(), name, raw.Name())
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
		// group 4 is the task id.
		if m := combinedPlaceholderRe.FindStringSubmatch("{{params." + s + "}}"); len(m) != 5 || m[1] != s || m[2] != "" || m[3] != "" || m[4] != "" {
			t.Errorf("combinedPlaceholderRe param branch dropped %q from {{params.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{state." + s + "}}"); len(m) != 5 || m[2] != s || m[1] != "" || m[3] != "" || m[4] != "" {
			t.Errorf("combinedPlaceholderRe state branch dropped %q from {{state.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{prev." + s + "}}"); len(m) != 5 || m[3] != s || m[1] != "" || m[2] != "" || m[4] != "" {
			t.Errorf("combinedPlaceholderRe prev branch dropped %q from {{prev.%s}}; got %v — regex drift", s, s, m)
		}
		if m := combinedPlaceholderRe.FindStringSubmatch("{{" + s + "}}"); len(m) != 5 || m[4] != s || m[1] != "" || m[2] != "" || m[3] != "" {
			t.Errorf("combinedPlaceholderRe task branch dropped %q from {{%s}}; got %v — regex drift", s, s, m)
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
