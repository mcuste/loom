package syntax

import (
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownKeysAndKeepsSource(t *testing.T) {
	t.Parallel()

	_, err := Decode([]byte("name: demo\nbogus: true\n"), Source{Path: "wf.yaml"})
	if err == nil || !strings.Contains(err.Error(), "field bogus not found") {
		t.Fatalf("Decode unknown key error = %v", err)
	}

	draft, err := Decode([]byte("name: demo\ntasks:\n  - id: a\n    prompt: hi\n"), Source{Path: "wf.yaml"})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if draft.Source.Path != "wf.yaml" {
		t.Fatalf("Source.Path = %q, want wf.yaml", draft.Source.Path)
	}
	if draft.Name != "demo" || len(draft.Tasks) != 1 || draft.Tasks[0].Prompt != "hi" {
		t.Fatalf("Draft = %#v", draft)
	}
}

func TestDecodeReportsYAMLTypeErrors(t *testing.T) {
	t.Parallel()

	_, err := Decode([]byte("name: demo\ntasks: nope\n"), Source{})
	if err == nil || !strings.HasPrefix(err.Error(), "yaml: ") {
		t.Fatalf("Decode error = %v, want yaml prefix", err)
	}
}
