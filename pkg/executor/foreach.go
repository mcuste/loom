package executor

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// forEachConcurrency caps the number of fanout instances a single for_each task
// runs at once. A fixed bound (rather than a per-task knob) keeps v1 simple
// while still preventing an unbounded list from spawning hundreds of runtime
// calls in parallel; a `max_parallel` override is future work.
const forEachConcurrency = 8

// runForEach executes a fanout task: it resolves the instance list, runs one
// instance per value concurrently (bounded by forEachConcurrency, honoring ctx
// cancellation, each with the task's own retry policy), and joins the instance
// outputs with newlines. The joined string is the node's Output, so a
// downstream `{{base_id}}` reference sees every instance.
//
// The list is t.ForEach for a static fanout, or the parsed result of
// substituting t.ForEachSource for a dynamic one. Each instance binds {{As}} to
// its value before the normal placeholder substitution. An empty list yields an
// empty output and no instances (composes with loop-until-dry: nothing to fan
// out reads as drained).
//
// runner/model/effort/sysPrompt are used for LLM tasks and ignored for shell
// tasks (pass the zero values). mu guards outputs, shared with the caller's Run.
func runForEach(ctx context.Context, t *workflow.Task, mu *sync.Mutex, outputs map[workflow.TaskID]string, opts Options, base time.Duration, runner runtime.Runner, model runtime.Model, effort runtime.Effort, sysPrompt string) (TaskResult, error) {
	var items []string
	if t.ForEachSource != "" {
		mu.Lock()
		resolved := workflow.Substitute(t.ForEachSource, outputs, opts.Params, opts.State)
		mu.Unlock()
		items = parseList(resolved)
	} else {
		items = t.ForEach
	}

	if len(items) == 0 {
		return TaskResult{TaskID: t.ID}, nil
	}

	src := t.Prompt
	if t.IsShell() {
		src = t.Command
	}
	placeholder := "{{" + t.As + "}}"

	type instResult struct {
		res TaskResult
		err error
	}
	results := make([]instResult, len(items))
	sem := make(chan struct{}, forEachConcurrency)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = instResult{err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			// Bind the loop variable first, then run the normal substitution,
			// so a {{As}} value containing placeholder-looking text is spliced
			// before (not re-expanded across) the second pass.
			withAs := strings.ReplaceAll(src, placeholder, item)
			mu.Lock()
			body := workflow.Substitute(withAs, outputs, opts.Params, opts.State)
			mu.Unlock()

			if t.IsShell() {
				res, err := runWithRetry(ctx, t, base, func() (TaskResult, error) {
					return runShell(ctx, t, body)
				})
				results[i] = instResult{res: res, err: err}
				return
			}
			res, err := runWithRetry(ctx, t, base, func() (TaskResult, error) {
				return runLLM(ctx, t, body, runner, model, effort, sysPrompt)
			})
			results[i] = instResult{res: res, err: err}
		}()
	}
	wg.Wait()

	// Join in list order; the first instance error fails the whole node. Usage
	// sums across instances and Elapsed is the longest single instance (they ran
	// concurrently), mirroring the wall-clock the node actually took.
	combined := TaskResult{TaskID: t.ID}
	outs := make([]string, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			return combined, r.err
		}
		outs = append(outs, r.res.Output)
		combined.Usage.InputTokens += r.res.Usage.InputTokens
		combined.Usage.OutputTokens += r.res.Usage.OutputTokens
		combined.Usage.CacheReadTokens += r.res.Usage.CacheReadTokens
		combined.Usage.TotalCostUSD += r.res.Usage.TotalCostUSD
		if r.res.Elapsed > combined.Elapsed {
			combined.Elapsed = r.res.Elapsed
		}
	}
	combined.Output = strings.Join(outs, "\n")
	return combined, nil
}

// parseList parses a dynamic for_each source string into instance values. A
// string that parses as a JSON array of strings is used directly; otherwise the
// string is split on newlines. In both cases entries are trimmed and empties
// dropped, so trailing newlines or blank lines do not spawn empty instances.
func parseList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return trimDropEmpty(arr)
		}
		// Not a JSON string array; fall through to newline splitting.
	}
	return trimDropEmpty(strings.Split(s, "\n"))
}

// trimDropEmpty trims each entry and drops the ones that become empty.
func trimDropEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
