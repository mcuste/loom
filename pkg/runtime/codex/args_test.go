package codex

import (
	"io"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

func TestArgsReadsPromptFromStdin(t *testing.T) {
	got := args(runtime.Request{Model: "gpt-5.4", Effort: "high", Prompt: "ignored"})
	if got[len(got)-1] != "-" {
		t.Fatalf("last arg = %q, want - so codex reads prompt from stdin; args=%v", got[len(got)-1], got)
	}
}

func TestStdinCarriesPrompt(t *testing.T) {
	r := stdin(runtime.Request{Prompt: "hello from stdin"})
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(b) != "hello from stdin" {
		t.Fatalf("stdin content = %q, want prompt", string(b))
	}
}
