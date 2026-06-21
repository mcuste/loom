package executor_test

import (
	"context"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// countingRuntime echoes the prompt and counts how many times Run was invoked,
// so a test can prove a cache hit skipped the runtime entirely.
type countingRuntime struct {
	mu    sync.Mutex
	calls int
}

func (c *countingRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (c *countingRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 0.001},
	}, nil
}

func (c *countingRuntime) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// registerCounter installs a fresh countingRuntime under a test-unique name and
// returns the double plus the name to wire into a workflow. Scoping the
// registration to each test keeps the call count private, so tests neither
// couple on ordering nor block t.Parallel.
func registerCounter(t *testing.T) (*countingRuntime, runtime.Name) {
	t.Helper()
	rt := &countingRuntime{}
	name := runtime.Name("exec-cache-" + t.Name())
	runtime.Register(name, rt)
	return rt, name
}

// savedEntry records one Cache.Save call so tests can assert exactly which
// tasks were memoized.
type savedEntry struct {
	rt           runtime.Name
	model        runtime.Model
	effort       runtime.Effort
	systemPrompt string
	prompt       string
	output       string
}

// fakeCache is an in-memory executor.Cache that records lookups and saves.
type fakeCache struct {
	mu      sync.Mutex
	entries map[string]string
	saved   []savedEntry
	lookups int
}

func newFakeCache() *fakeCache { return &fakeCache{entries: map[string]string{}} }

func fakeKey(rt runtime.Name, m runtime.Model, e runtime.Effort, sys, prompt string) string {
	return string(rt) + "\x00" + string(m) + "\x00" + string(e) + "\x00" + sys + "\x00" + prompt
}

func (c *fakeCache) Lookup(rt runtime.Name, m runtime.Model, e runtime.Effort, sys, prompt string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups++
	out, ok := c.entries[fakeKey(rt, m, e, sys, prompt)]
	return out, ok, nil
}

func (c *fakeCache) Save(rt runtime.Name, m runtime.Model, e runtime.Effort, sys, prompt, output string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[fakeKey(rt, m, e, sys, prompt)] = output
	c.saved = append(c.saved, savedEntry{rt, m, e, sys, prompt, output})
	return nil
}

func boolPtr(b bool) *bool { return &b }

// cacheWorkflow builds a single-LLM-task workflow with caching opted in,
// routed to the given runtime.
func cacheWorkflow(rt runtime.Name) *workflow.Workflow {
	return &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "hello", Cache: boolPtr(true)},
		},
	}
}

// TestRun_CacheHitReplaysWithoutRuntime is the miss-then-hit scenario: the
// first run dispatches the runtime and records the output; the second run, with
// the cache warm, replays the stored output without calling the runtime, with
// zero usage and the CacheHit marker set.
func TestRun_CacheHitReplaysWithoutRuntime(t *testing.T) {
	t.Parallel()
	rt, name := registerCounter(t)
	cache := newFakeCache()
	wf := cacheWorkflow(name)

	first, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{Cache: cache})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if rt.count() != 1 {
		t.Fatalf("after first run: runtime calls = %d, want 1", rt.count())
	}
	if first.Tasks[0].CacheHit {
		t.Errorf("first run: CacheHit = true, want false on a cold cache")
	}
	if first.Tasks[0].Usage == (runtime.Usage{}) {
		t.Errorf("first run: Usage = zero, want non-zero usage recorded on a cold-cache miss")
	}

	second, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{Cache: cache})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if rt.count() != 1 {
		t.Fatalf("after second run: runtime calls = %d, want 1 (cache hit must skip runtime)", rt.count())
	}
	res := second.Tasks[0]
	if !res.CacheHit {
		t.Errorf("second run: CacheHit = false, want true on a warm cache")
	}
	if res.Output != "hello" {
		t.Errorf("second run: Output = %q, want %q", res.Output, "hello")
	}
	if res.Usage != (runtime.Usage{}) {
		t.Errorf("second run: Usage = %+v, want zero on a cache hit", res.Usage)
	}
}

// TestRun_CachesLLMTaskButNotShellTask pins that shell tasks are never
// memoized: with caching enabled on both an LLM task and a shell task, only the
// LLM task is saved to the cache.
func TestRun_CachesLLMTaskButNotShellTask(t *testing.T) {
	t.Parallel()
	_, name := registerCounter(t)
	cache := newFakeCache()
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: name,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "llm", Prompt: "hello", Cache: boolPtr(true)},
			{ID: "sh", Command: "echo hi", Cache: boolPtr(true)},
		},
	}

	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{Cache: cache}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.saved) != 1 {
		t.Fatalf("Cache.Save called %d times, want 1 (LLM task only)", len(cache.saved))
	}
	if cache.saved[0].prompt != "hello" {
		t.Errorf("saved entry prompt = %q, want the LLM task's prompt %q", cache.saved[0].prompt, "hello")
	}
}

// TestRun_ForEachWithCacheIsRejected pins the diagnostic for the unsupported
// combination: a for_each LLM task with caching opted in must fail loudly
// rather than silently bypass memoization.
func TestRun_ForEachWithCacheIsRejected(t *testing.T) {
	t.Parallel()
	rt, name := registerCounter(t)
	cache := newFakeCache()
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: name,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "{{x}}", ForEach: []string{"one", "two"}, As: "x", Cache: boolPtr(true)},
		},
	}

	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{Cache: cache})
	if err == nil {
		t.Fatal("Run: error = nil, want a diagnostic for for_each + cache")
	}
	if rt.count() != 0 {
		t.Errorf("runtime calls = %d, want 0 (must reject before dispatching)", rt.count())
	}
}
