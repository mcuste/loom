---
name: loom
description: Author and run loom workflows — YAML DAGs of LLM tasks executed by the `loom` CLI installed from this repo. Use when the user wants to "write a loom workflow", "build a loom DAG", "run this with loom", "check a loom yaml", "chain prompts with loom", or asks for help with `loom run` / `loom check`.
allowed-tools: Bash(loom *), Read, Write, Edit, Glob, Grep
---

Author and execute loom workflows. Loom parses a YAML workflow, builds a DAG over `{{id}}` placeholders + `depends_on`, and dispatches each task to a registered runtime (currently only `claude-code`, which shells out to the `claude` CLI).

## CLI

```bash
loom check <workflow.yaml>   # parse + validate + print execution order, no execution
loom run   <workflow.yaml>   # check + execute every task (independent tasks run concurrently)
```

Always run `loom check <file>` first when authoring — it surfaces every validation error (cycles, unknown deps, unknown placeholders, bad model/effort) without burning tokens on `claude`.

`loom run` prints the plan, per-task progress (id, runtime/model/effort, tokens, cost), the run-file path, and a final summary. Independent tasks (no edge between them in the DAG) are dispatched concurrently — fan-out is free, you don't need to wait for siblings.

## YAML schema

Top level:

```yaml
name: my_workflow            # required, [A-Za-z0-9_]+
description: ...             # optional, plan-output only
runtime: claude-code         # default for tasks; only claude-code is registered
model: sonnet                # default; one of: haiku | sonnet | opus
effort: medium               # default; one of: low | medium | high | max  (claude-code)
system_prompt: ...           # optional, appended to every task

tasks:
  - id: <task_id>            # required, [A-Za-z0-9_]+, unique
    description: ...         # optional
    runtime: ...             # optional task-level override
    model: ...               # optional task-level override
    effort: ...              # optional task-level override
    depends_on: [a, b]       # optional; explicit DAG edges
    prompt: |                # required, non-empty
      Text with {{a}} and {{b}} placeholders.
```

Unknown top-level or task-level keys are **rejected** (parser uses `KnownFields(true)`). No `inputs:`, `output:`, `workflow:`, `with:` — sub-workflows aren't implemented.

## Placeholders and dependencies

- `{{task_id}}` in a prompt is substituted with that upstream task's full string output at runtime.
- Every `{{id}}` placeholder **must also appear in `depends_on`**. Placeholders never implicitly extend the DAG. Repeating a placeholder is fine; it's templating, not declaration.
- `depends_on` may list a task with no placeholder (pure ordering constraint).
- Cycles, unknown deps, duplicate deps, and unknown placeholders fail `loom check`.

## Runtime / model / effort

Resolution per task: task field → workflow default. A task with no runtime and a workflow with no default fails validation.

`claude-code` runtime (only one registered, see `pkg/runtime/claudecode/claudecode.go`):

- models: `haiku`, `sonnet`, `opus`
- efforts: `low`, `medium`, `high`, `max` (optional; empty = runtime default)
- binary: `claude` must be on PATH; invoked with `-p --output-format json --model X [--effort Y] [--system-prompt Z] --dangerously-skip-permissions <prompt>`

Per-task overrides are the standard pattern: cheap+fast (haiku/low) for fan-out drafting, escalate (opus/high or max) for synthesis or critique steps. Since independent tasks run in parallel, a wide fan-out of haiku drafts costs ~one task's wall time, not N.

## Authoring workflow

1. Decide the DAG shape first (fan-out, chain, diamond). Sketch task ids and edges.
2. Pick workflow-level defaults to cover the majority of tasks; override only where escalation matters.
3. Keep prompts tight — each `{{id}}` placeholder injects the *entire* upstream output verbatim.
4. Run `loom check` until it's clean, then `loom run`.

## Adding a new runtime

If the user wants to register another runtime (e.g. `openai-api`, `ollama`):

1. New package under `pkg/runtime/<name>/` exposing a type that satisfies `runtime.Runtime` (and optionally `SubprocessRuntime` or `APIRuntime`).
2. `Validate(req runtime.Request) error` enforces accepted models / effort / system-prompt rules, wrapping `runtime.ErrMissingModel`, `ErrUnsupportedModel`, `ErrUnsupportedEffort`, `ErrUnsupportedSystemPrompt` as appropriate.
3. `Run(ctx, req) (runtime.Response, error)` executes the request and returns text + `runtime.Usage`.
4. `init()` calls `runtime.Register(Name, spec{})`.
5. Add a side-effect import in `cmd/loom/main.go`.
6. Tests in `pkg/runtime/<name>/<name>_test.go` mirroring `claudecode_test.go`.

## Output

`loom run` writes plan + per-task progress + summary (tokens, cost, completion) to stdout. In parallel, it persists a self-contained JSON record of the run on disk:

```
.loom/runs/<workflow_id>/<run_id>.json     # full record, atomic rewrite per task event
.loom/runs/<workflow_id>/latest.json       # symlink to the most recent run
```

`<run_id>` is `YYYYMMDDTHHMMSSZ-<6 hex>` (UTC, sortable). The file embeds the verbatim manifest, per-task `prompt` (with placeholders already substituted), full `output`, `usage` (in / out / cache-read tokens, cost USD), timing, and status — plus a top-level `usage` total and `task_count`. It is rewritten on every `OnStart` / `OnFinish` via tmp+rename, so a crash mid-run still leaves a parseable file.

Use it to inspect what the model actually saw and produced, or to compare runs:

```bash
jq '.usage,.task_count' .loom/runs/<workflow_id>/latest.json
jq '.tasks[] | {id, model, effort, usage, elapsed_ms}' .loom/runs/<workflow_id>/latest.json
```

Stdout is the only progress channel; task outputs do not leak to stdout beyond the summary line. They live in memory for `{{id}}` substitution downstream and on disk in the run record.
