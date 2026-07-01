package codex

import (
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

func TestDecodeAgentMessage(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2,"reasoning_output_tokens":1}}`,
	}, "\n")

	got, err := decode([]byte(stdout))
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	if got.Output != "done" {
		t.Fatalf("Output = %q, want %q", got.Output, "done")
	}
	wantUsage := runtime.Usage{InputTokens: 10, CacheReadTokens: 4, OutputTokens: 2}
	if got.Usage != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got.Usage, wantUsage)
	}
}

func TestDecodeCompletedTurnWithoutAgentMessage(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}`,
	}, "\n")

	got, err := decode([]byte(stdout))
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	if got.Output != "" {
		t.Fatalf("Output = %q, want empty", got.Output)
	}
	wantUsage := runtime.Usage{InputTokens: 10, CacheReadTokens: 4, OutputTokens: 2}
	if got.Usage != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got.Usage, wantUsage)
	}
}

func TestDecodeTurnFailed(t *testing.T) {
	_, err := decode([]byte(`{"type":"turn.failed","error":{"message":"boom"}}` + "\n"))
	if err == nil {
		t.Fatal("decode() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("decode() error = %v, want boom", err)
	}
}

func TestDecodeNoCompletedTurnOrAgentMessage(t *testing.T) {
	_, err := decode([]byte(`{"type":"thread.started","thread_id":"t"}` + "\n"))
	if err == nil {
		t.Fatal("decode() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "no agent_message") {
		t.Fatalf("decode() error = %v, want no agent_message", err)
	}
}
