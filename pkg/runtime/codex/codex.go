// Package codex registers the codex CLI runtime with the runtime registry.
// Import it for side effects to make the runtime available:
//
//	import _ "github.com/mcuste/loom/pkg/runtime/codex"
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mcuste/loom/pkg/runtime"
)

// Name is the runtime identifier used in workflow YAML.
const Name runtime.Name = "codex"

const binary = "codex"

var (
	models = map[runtime.Model]struct{}{
		"gpt-5.5":             {},
		"gpt-5.4":             {},
		"gpt-5.4-mini":        {},
		"gpt-5.3-codex-spark": {},
	}
	efforts = map[runtime.Effort]struct{}{
		"minimal": {},
		"low":     {},
		"medium":  {},
		"high":    {},
		"xhigh":   {},
	}

	// pricing carries per-million-token USD rates for each whitelisted model
	// (OpenAI standard pricing, June 2026). Cache reads are 90% discounted
	// from the standard input rate. Models absent from this map produce
	// TotalCostUSD = 0 — non-fatal so a newly released model still runs;
	// callers wanting strict accounting should keep this map in sync with
	// OpenAI's published rates.
	pricing = map[runtime.Model]prices{
		"gpt-5.5":             {input: 5.00, output: 30.00, cache: 0.50},
		"gpt-5.4":             {input: 2.50, output: 15.00, cache: 0.25},
		"gpt-5.4-mini":        {input: 0.75, output: 4.50, cache: 0.075},
		"gpt-5.3-codex-spark": {input: 1.75, output: 14.00, cache: 0.175},
	}
)

// prices holds per-million-token USD rates for one model.
type prices struct {
	input, output, cache float64
}

// costUSD derives the USD cost of a usage record using the model's pricing.
// Codex reports InputTokens as the TOTAL input count (fresh + cached); the
// cached subset is billed at the discounted rate, the remainder at full rate.
// Returns 0 for any model not in the pricing map.
func costUSD(model runtime.Model, u runtime.Usage) float64 {
	p, ok := pricing[model]
	if !ok {
		return 0
	}
	fresh := max(u.InputTokens-u.CacheReadTokens, 0)
	const perMillion = 1e6
	return (float64(fresh)*p.input +
		float64(u.CacheReadTokens)*p.cache +
		float64(u.OutputTokens)*p.output) / perMillion
}

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
	// `codex exec` has no system-prompt flag; AGENTS.md is the only mechanism
	// and is filesystem-resident, so a per-request SystemPrompt cannot be
	// honored without silently changing semantics.
	if req.SystemPrompt != "" {
		return runtime.ErrUnsupportedSystemPrompt
	}
	return nil
}

func (spec) Binary() string { return binary }

func (spec) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	args := []string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--model", string(req.Model),
	}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+string(req.Effort))
	}
	args = append(args, req.Prompt)

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return runtime.Response{}, &ExecError{Err: err, Stderr: stderr.String()}
	}

	var (
		lastAgentMessage string
		usage            runtime.Usage
	)
	scanner := bufio.NewScanner(&stdout)
	// Individual JSONL events (especially reasoning items) can exceed the
	// default 64 KiB scanner buffer; raise the ceiling to 1 MiB per line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return runtime.Response{}, fmt.Errorf("codex: parse json: %w", err)
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				lastAgentMessage = ev.Item.Text
			}
		case "turn.completed":
			// reasoning_output_tokens is a breakdown of output_tokens per
			// Responses API convention, so it is not added separately.
			usage = runtime.Usage{
				InputTokens:     ev.Usage.InputTokens,
				OutputTokens:    ev.Usage.OutputTokens,
				CacheReadTokens: ev.Usage.CachedInputTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return runtime.Response{}, fmt.Errorf("codex: read json stream: %w", err)
	}

	usage.TotalCostUSD = costUSD(req.Model, usage)
	return runtime.Response{
		Output: lastAgentMessage,
		Usage:  usage,
	}, nil
}

func init() {
	runtime.Register(Name, spec{})
}

// ExecError is returned when the `codex` subprocess fails. It carries the
// wrapped exec error and the verbatim stderr so callers (and `errors.Is` /
// `errors.As`) can inspect each separately instead of digging through a
// formatted string.
type ExecError struct {
	Err    error
	Stderr string
}

func (e *ExecError) Error() string {
	if e.Stderr == "" {
		return "codex: " + e.Err.Error()
	}
	return fmt.Sprintf("codex: %s: %s", e.Err, strings.TrimSpace(e.Stderr))
}

func (e *ExecError) Unwrap() error { return e.Err }
