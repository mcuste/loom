// Package claudecode registers the claude-code CLI runtime with the runtime
// registry. Import it for side effects to make the runtime available:
//
//	import _ "github.com/mcuste/loom/pkg/runtime/claudecode"
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/mcuste/loom/pkg/runtime"
)

// Name is the runtime identifier used in workflow YAML.
const Name runtime.Name = "claude-code"

const binary = "claude"

var (
	models  = map[runtime.Model]struct{}{"sonnet": {}, "opus": {}, "haiku": {}}
	efforts = map[runtime.Effort]struct{}{"low": {}, "medium": {}, "high": {}, "max": {}}
)

type spec struct{}

// Compile-time proof that spec satisfies the Subprocess contract.
var _ runtime.Subprocess = spec{}

func (spec) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	if _, ok := models[req.Model]; !ok {
		return fmt.Errorf("model %q: %w", req.Model, runtime.ErrUnsupportedModel)
	}
	if req.Effort != "" {
		if _, ok := efforts[req.Effort]; !ok {
			return fmt.Errorf("effort %q: %w", req.Effort, runtime.ErrUnsupportedEffort)
		}
	}
	// SystemPrompt is always accepted; no check needed.
	return nil
}

func (spec) Binary() string { return binary }

func (spec) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	args := []string{
		"-p",
		"--output-format", "json",
		"--model", string(req.Model),
	}
	if req.Effort != "" {
		args = append(args, "--effort", string(req.Effort))
	}
	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}
	args = append(args, "--dangerously-skip-permissions", req.Prompt)

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return runtime.Response{}, fmt.Errorf("claude-code: %w (stderr: %s)", err, stderr.String())
	}

	var env struct {
		Result string `json:"result"`
		Usage  struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return runtime.Response{}, fmt.Errorf("claude-code: parse json: %w", err)
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
	runtime.Register(Name, spec{})
}
