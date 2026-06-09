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
		name    string
		raw     any
		public  any
		aliases map[string]string // raw field name -> public field name
	}{
		{
			name:    "workflow",
			raw:     rawWorkflow{},
			public:  Workflow{},
			aliases: map[string]string{"Name": "ID"},
		},
		{
			name:   "task",
			raw:    rawTask{},
			public: Task{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFieldParity(t, reflect.TypeOf(tc.raw), reflect.TypeOf(tc.public), tc.aliases)
		})
	}
}

func assertFieldParity(t *testing.T, raw, public reflect.Type, aliases map[string]string) {
	t.Helper()

	pubFields := map[string]struct{}{}
	for f := range public.Fields() {
		// Unexported fields are internal caches (e.g. parse-built indices), not
		// part of the YAML-to-domain schema, and must not trigger drift alarms.
		if !f.IsExported() {
			continue
		}
		pubFields[f.Name] = struct{}{}
	}

	for rf := range raw.Fields() {
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
// string accepted as a TaskID by identifierRe is also captured by the
// parser's placeholderRe when wrapped in `{{...}}`, and that strings rejected
// by identifierRe are also rejected by placeholderRe. If someone widens or
// narrows identifierClass without updating one regex, this test fails.
func TestPlaceholderRegexMatchesIdentifierAlphabet(t *testing.T) {
	accept := []string{"a", "A", "task1", "joke", "summary_v2", "_under", "123abc"}
	reject := []string{"", "with-dash", "with.dot", "with space", "with$dollar", "with/slash"}

	for _, s := range accept {
		if !identifierRe.MatchString(s) {
			t.Errorf("identifierRe rejected %q; update the accept list if intended", s)
			continue
		}
		matches := placeholderRe.FindStringSubmatch("{{" + s + "}}")
		if len(matches) != 2 || matches[1] != s {
			t.Errorf("placeholderRe did not capture %q from {{%s}}; got %v — regex drift", s, s, matches)
		}
	}
	for _, s := range reject {
		if identifierRe.MatchString(s) {
			t.Errorf("identifierRe accepted %q; update the reject list if intended", s)
			continue
		}
		if matches := placeholderRe.FindStringSubmatch("{{" + s + "}}"); matches != nil {
			t.Errorf("placeholderRe matched {{%s}} for a non-identifier string; got %v — regex drift", s, matches)
		}
	}
}
