// Package codex registers the codex CLI runtime with the runtime registry.
// Import it for side effects to make the runtime available:
//
//	import _ "github.com/mcuste/loom/pkg/runtime/codex"
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"

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
	// TotalCostUSD = 0; non-fatal so a newly released model still runs;
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

var spec = runtime.Spec{
	Name:       Name,
	BinaryName: binary,
	Models:     models,
	Efforts:    efforts,
	// `codex exec` has no system-prompt flag; AGENTS.md is the only mechanism
	// and is filesystem-resident, so a per-request SystemPrompt cannot be
	// honored without silently changing semantics.
	AcceptSystemPrompt: false,
	Args:               args,
	Decode:             decode,
	Price:              costUSD,
}

func args(req runtime.Request) []string {
	a := []string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--model", string(req.Model),
	}
	if req.Effort != "" {
		a = append(a, "-c", "model_reasoning_effort="+string(req.Effort))
	}
	return append(a, req.Prompt)
}

// decode scans codex's JSONL event stream for the final agent message and turn
// usage. Pricing is applied by Spec.Run via the Price hook; decode is a pure
// stdout-to-Response transform.
func decode(stdout []byte) (runtime.Response, error) {
	var (
		lastAgentMessage string
		usage            runtime.Usage
	)
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
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
			return runtime.Response{}, fmt.Errorf("parse json: %w", err)
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
		return runtime.Response{}, fmt.Errorf("read json stream: %w", err)
	}
	if lastAgentMessage == "" {
		return runtime.Response{}, fmt.Errorf("no agent_message in event stream")
	}

	return runtime.Response{
		Output: lastAgentMessage,
		Usage:  usage,
	}, nil
}

func init() {
	runtime.Register(Name, spec)
}
