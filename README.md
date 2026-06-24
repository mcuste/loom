# Loom

Loom runs a YAML-defined DAG of LLM (and shell) tasks. You write a workflow
manifest; loom parses it, builds a dependency graph over `{{placeholder}}`
references and `depends_on` edges, and dispatches each task to a runtime
(`claude-code`, which shells out to the `claude` CLI, or `codex`, which shells
out to the `codex` CLI). Independent tasks run concurrently.

## Documentation

- [docs/workflow-spec.md](docs/workflow-spec.md) — the complete manifest
  reference: every top-level and task-level field, placeholders, dependencies,
  command tasks, params, conditionals (`when`), retry, output schema, budgets,
  caching, cross-run state, and scoped loops (`loop` / `for_each`).
- [docs/cli.md](docs/cli.md) — the command surface: `loom run`,
  `loom run check`, `loom resume`, `loom runs`; the on-disk run record; resume
  semantics; and the `LOOM_HOME` data directory.

## Mental model

A workflow is a directed acyclic graph. Each task is a node; an edge `a -> b`
(declared by listing `a` in `b`'s `depends_on`) means "b depends on a", so loom
runs `a` first and makes its output available to `b` as `{{a}}`. Tasks with no
edge between them run concurrently.

The graph can be any shape. There is no requirement to fan out or to converge;
these are all valid workflows:

```txt
linear chain          independent tasks       fan-out then fan-in
                      (all concurrent)
┌────────┐            ┌────┐ ┌────┐ ┌────┐     ┌──────┐
│  one   │            │ a  │ │ b  │ │ c  │     │ seed │
└───┬────┘            └────┘ └────┘ └────┘     └──┬───┘
    ▼                                          ┌──┴──┐
┌────────┐            no edges: loom runs       ▼     ▼
│  two   │            them all at once       ┌────┐ ┌────┐
└───┬────┘                                   │ x  │ │ y  │
    ▼                                        └──┬─┘ └─┬──┘
┌────────┐                                      └──┬──┘
│ three  │                                         ▼
└────────┘                                     ┌───────┐
                                               │ merge │
                                               └───────┘
```

A single task is a workflow too. Pick whatever shape the work needs.

Two rules anchor everything:

1. **`depends_on` is the only source of truth for the graph.** A `{{task_id}}`
   placeholder in a prompt is pure templating; it never adds an edge on its own.
   Every `{{task_id}}` you reference must also be declared in that task's
   `depends_on`. (The loop-only `{{prev.id}}` and `for_each` `{{as}}`
   placeholders are the documented exceptions.)
2. **Validation runs before execution.** `loom run check <file>` parses and
   validates the entire manifest (cycles, unknown deps, unknown placeholders,
   bad model/effort, malformed blocks) without dispatching a single model call.
   Run it until clean before spending tokens.

## 60-second start

```yaml
# hello.yaml
name: hello
runtime: claude-code
model: sonnet
effort: low

tasks:
  - id: idea
    prompt: Give me one surprising fact about octopuses. One sentence.

  - id: expand
    depends_on: [idea]
    prompt: |
      Expand this into a short paragraph for a child:
      {{idea}}
```

```bash
loom run check hello.yaml   # validate + print the plan, no model calls
loom run hello.yaml         # execute; prints progress, tokens, cost, run-file path
```

See [docs/workflow-spec.md](docs/workflow-spec.md) for the full field reference
and [docs/cli.md](docs/cli.md) for everything the CLI can do with a run after it
finishes.
