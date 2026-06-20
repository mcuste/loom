// Package claudecode registers the claude-code CLI runtime with the runtime
// registry. Import it for side effects to make the runtime available:
//
//	import _ "github.com/mcuste/loom/pkg/runtime/claudecode"
package claudecode

import (
	"encoding/json"
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
)

// Name is the runtime identifier used in workflow YAML.
const Name runtime.Name = "claude-code"

const binary = "claude"

var (
	models  = map[runtime.Model]struct{}{"sonnet": {}, "opus": {}, "haiku": {}}
	efforts = map[runtime.Effort]struct{}{"low": {}, "medium": {}, "high": {}, "max": {}}
)

var spec = runtime.Spec{
	Name:               Name,
	BinaryName:         binary,
	Models:             models,
	Efforts:            efforts,
	AcceptSystemPrompt: true,
	Args:               args,
	Decode:             decode,
}

func args(req runtime.Request) []string {
	a := []string{
		"-p",
		"--output-format", "json",
		"--model", string(req.Model),
	}
	if req.Effort != "" {
		a = append(a, "--effort", string(req.Effort))
	}
	if req.SystemPrompt != "" {
		a = append(a, "--system-prompt", req.SystemPrompt)
	}
	return append(a, "--dangerously-skip-permissions", req.Prompt)
}

func decode(stdout []byte) (runtime.Response, error) {
	var env struct {
		Result string `json:"result"`
		Usage  struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		return runtime.Response{}, fmt.Errorf("parse json: %w", err)
	}
	return runtime.Response{
		Output: env.Result,
		Usage: runtime.Usage{
			InputTokens:     env.Usage.InputTokens,
			OutputTokens:    env.Usage.OutputTokens,
			CacheReadTokens: env.Usage.CacheReadInputTokens,
			TotalCostUSD:    env.TotalCostUSD,
		},
	}, nil
}

func init() {
	runtime.Register(Name, spec)
}
