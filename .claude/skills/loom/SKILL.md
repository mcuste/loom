---
name: loom
description: Author and run loom workflows — YAML DAGs of LLM tasks executed by the `loom` CLI installed from this repo. Use when the user wants to "write a loom workflow", "build a loom DAG", "run this with loom", "check a loom yaml", "chain prompts with loom", or asks for help with `loom run` / `loom check`.
allowed-tools: Bash(loom *), Read, Write, Edit, Glob, Grep
---

Author and execute loom workflows. Loom parses a YAML workflow, builds a DAG over `{{id}}` placeholders + `depends_on`, and dispatches each task to a registered runtime (`claude-code`, which shells out to the `claude` CLI, or `codex`, which shells out to the `codex` CLI).

## CLI

```bash
loom check  <workflow.yaml>                 # parse + validate + print execution order, no execution
loom run    <workflow.yaml>                 # check + execute every task (independent tasks run concurrently)
loom run    <workflow.yaml> --resume-latest # resume the last run of this workflow: skip ok tasks, re-run the rest
loom resume <run-id>                        # resume a specific .loom/runs/<wf>/<id>.json; "latest" follows latest.json
```

Always run `loom check <file>` first when authoring — it surfaces every validation error (cycles, unknown deps, unknown placeholders, bad model/effort) without burning tokens on `claude`.

`loom run` prints the plan, per-task progress (id, runtime/model/effort, tokens, cost), the run-file path, and a final summary. Independent tasks (no edge between them in the DAG) are dispatched concurrently — fan-out is free, you don't need to wait for siblings.

## YAML schema

Top level:

```yaml
name: my_workflow            # required, [A-Za-z0-9_]+
description: ...             # optional, plan-output only
runtime: claude-code         # default for tasks; one of: claude-code | codex
model: sonnet                # default; one of: haiku | sonnet | opus
effort: medium               # default; one of: low | medium | high | max  (claude-code)
system_prompt: ...           # optional, appended to every task
params:                       # optional; see ## Params
  - name: ...                # required, [A-Za-z0-9_]+, unique
    description: ...         # optional
    default: ...             # optional; string-only, no {{}} expansion
    required: false          # optional; true if no default

tasks:
  - id: <task_id>            # required, [A-Za-z0-9_]+, unique
    description: ...         # optional
    runtime: ...             # optional task-level override
    model: ...               # optional task-level override
    effort: ...              # optional task-level override
    depends_on: [a, b]       # optional; explicit DAG edges
    prompt: |                # exactly one of prompt or command; non-empty
      Text with {{a}} and {{b}} placeholders.
    # — OR —
    command: |               # runs sh -c instead of dispatching to a runtime
      echo "{{a}}" | wc -l
```

Unknown top-level or task-level keys are **rejected** (parser uses `KnownFields(true)`). No `inputs:`, `output:`, `workflow:`, `with:` — sub-workflows aren't implemented.

## Placeholders and dependencies

- `{{task_id}}` in a prompt is substituted with that upstream task's full string output at runtime.
- Every `{{id}}` placeholder **must also appear in `depends_on`**. Placeholders never implicitly extend the DAG. Repeating a placeholder is fine; it's templating, not declaration. The one exception is the `{{prev.<id>}}` loop placeholder, which is explicitly exempt (see [Scoped loops](#scoped-loops)).
- `depends_on` may list a task with no placeholder (pure ordering constraint).
- Cycles, unknown deps, duplicate deps, and unknown placeholders fail `loom check`.

## Command tasks

A task with `command:` runs `sh -c <substituted-command>` instead of dispatching to a runtime.

**Discriminator rule** — exactly one of `prompt` or `command` must be set per task. Setting both or neither is rejected by the parser (`loom check` surfaces the error before execution).

**Rejected fields on command tasks** — `runtime`, `model`, and `effort` at the task level are hard validation errors. Workflow-level defaults are tolerated (a shell task silently ignores them), but task-level overrides are rejected.

**Placeholders** — `{{task_id}}` and `{{params.x}}` substitute into `command:` bodies identically to `prompt:` bodies; the same `depends_on` rule applies (every `{{id}}` placeholder must also appear in `depends_on`).

**Stdout is the output** — captured stdout becomes the task's full string output, consumable downstream via `{{task_id}}` exactly like LLM output.

**Stderr streams live** — stderr is not captured; it surfaces on failure via the task's error message.

**Non-zero exit fails the task** — a non-zero exit code is treated as a task failure and propagates through the DAG exactly like a runtime error.

**Security caveat** — LLM output substituted via `{{task_id}}` into a `command:` body is untrusted input. If upstream tasks are prompted with user-supplied data, shell-injection is possible. Sanitise or quote values before splicing them into commands.

## Params

Workflows can accept parameters passed at runtime via the `-p` CLI flag. Params are declared at the top level, reference-able via `{{params.name}}` syntax in task prompts and `system_prompt`, and do not create DAG dependencies.

Declare params as a list of objects with:
- `name` (required, `[A-Za-z0-9_]+`): the identifier.
- `description` (optional): documented in the plan output.
- `default` (optional): string value used if not provided via CLI; if `default` is absent and `required: false` (or omitted), the param is optional with no default.
- `required` (optional, default `false`): if `true`, the param **must** be provided via `-p`; cannot set both `required: true` and `default`.

String values only — `default: 1` and `default: true` are stringified (`"1"`, `"true"`).

Substitute params in prompts with `{{params.foo}}` (separate namespace from task ids; `{{foo}}` is a task ref, never a param):

```yaml
name: deploy
description: Deploy service to an env
runtime: claude-code
model: sonnet
effort: low
params:
  - name: env
    description: Target environment
    required: true
  - name: replicas
    default: "1"
  - name: tag
    default: latest
tasks:
  - id: plan
    prompt: |
      Plan deploy of image {{params.tag}} to {{params.env}} ({{params.replicas}}x).
  - id: apply
    depends_on: [plan]
    prompt: |
      Apply plan for {{params.env}}:
      {{plan}}
```

Provide values on the CLI with repeatable `-p key=val`:

```bash
loom run examples/deploy.yaml -p env=prod -p replicas=3
loom check examples/deploy.yaml -p env=prod
```

Resolution order (right-to-left wins): declared defaults → `-p` values. Unknown keys or missing required params are hard errors.

## Scoped loops

A top-level `loops:` block declares one or more **scoped loops** — named subgraphs that the run pipeline re-executes until a convergence target is reached. A scoped loop re-runs only its own body tasks; the rest of the DAG runs once.

Declare each loop as a list entry with a nested `tasks:` body:

```yaml
loops:
  - id: <loop_id>            # required, [A-Za-z0-9_]+; unique, and distinct
                             # from every task id and param name
    until_empty: <member>    # converge when this member's trimmed output is empty
    # — OR —
    until: '{{member}} == "done"'  # converge when this when-style expression is true
    max: 4                   # required, >= 1; hard cap on iterations
    tasks:                   # required, non-empty; same task schema as top-level
      - id: drain
        depends_on: [seed]
        prompt: drain {{seed}} {{prev.refine}}
      - id: refine
        depends_on: [drain]
        prompt: refine {{drain}}
```

Rules (all surfaced by `loom check`):

- **Convergence** — exactly one of `until_empty` or `until` per loop. `until_empty` names a body member whose empty trimmed output ends the loop; `until` is a `when`-style expression that may reference **only** members of the same loop.
- **`max`** — required and `>= 1`; bounds iterations even if the target never converges.
- **Body** — `tasks:` is non-empty and uses the identical task schema (prompt/command, model/effort, etc.). Each body task carries its loop id; a task belongs to at most one loop.
- **Connected** — the body must form a single connected subgraph via its internal edges.

### Edge semantics

A loop body has three edge kinds, distinguished by where each `depends_on` endpoint lives:

- **Entry edge** — a body task depends on a non-member (e.g. `drain` depends on the top-level `seed`). Entry edges are satisfied once, before the loop starts; the upstream output is the same for every iteration.
- **Internal edge** — a body task depends on another member of the same loop (e.g. `refine` depends on `drain`). Internal edges order the tasks *within* each iteration.
- **Exit edge** — a non-member depends on a body member. The downstream task runs once, after the loop converges, seeing the final iteration's output.

### `prev.<id>` placeholder

Inside a loop body, `{{prev.<member>}}` injects the **prior iteration's** output of a sibling member (empty on the first iteration). It lets an iteration build on the last one without forming a cycle — `drain` above reads `{{prev.refine}}` to carry state forward. A `prev` reference is valid only inside a loop body and may only name a member of that same loop; using it elsewhere or across loops fails `loom check`.

Unlike a plain `{{id}}` placeholder, a `{{prev.<member>}}` reference is **exempt from the `depends_on` rule** and must *not* be listed in `depends_on`: it reads across iterations, not within one, so adding the edge would form a cycle. In the example above `drain` reads `{{prev.refine}}` yet only declares `depends_on: [seed]`.

`loom check` renders each loop as a labeled group showing its id, convergence target (`until_empty=` / `until=`), `max`, and every body task with its deps, so the loop's execution shape is visible without running it.

## Runtime / model / effort

These fields are LLM-only and are ignored by command tasks; task-level overrides of `runtime`, `model`, or `effort` on a command task are a hard validation error.

Resolution per task: task field → workflow default. A task with no runtime and a workflow with no default fails validation.

`claude-code` runtime (see `pkg/runtime/claudecode/claudecode.go`):

Models — pick by task difficulty:

- `haiku` — very simple, mechanical tasks: rename a file, format JSON, run a fixed shell command, summarize a short input.
- `sonnet` — standard, not-so-challenging work: implement code from an already-architected plan, write tests against a defined contract, follow a clear spec.
- `opus` — the most challenging work: architecture decisions, ambiguous requirements, design synthesis, hard debugging, adversarial review.

Efforts — pick by how much thinking the task warrants:

- `low` — one-shot, no real deliberation needed (the task is mostly typing).
- `medium` — default; some reasoning, weighing a couple of options before acting.
- `high` — extended thinking: multi-step reasoning, exploring alternatives, careful verification.
- `max` — burn maximum compute: only for the hardest synthesis / critique / design steps where getting it right dwarfs cost.

Resolve per task. The workflow-level `model` / `effort` covers the majority; override only on tasks that need to step up or down. Independent tasks run in parallel, so a wide fan-out of haiku/low drafts costs ~one task's wall time, not N.

`codex` runtime (see `pkg/runtime/codex/codex.go`):

Requires `OPENAI_API_KEY` (or `CODEX_API_KEY`) in the environment, or run `codex login` before `loom run`.

Models:

- `gpt-5.5` — reasoning; accepts effort.
- `gpt-5.4` — reasoning; accepts effort.
- `gpt-5.4-mini` — reasoning; smaller / faster; accepts effort.
- `gpt-5.3-codex-spark` — non-reasoning, text-only; effort is forwarded but may be ignored by the backend.

Efforts: `minimal | low | medium | high | xhigh`. Empty effort means "leave runtime default" (same convention as `claude-code`).

**`system_prompt` is not supported** by the `codex` runtime. Codex CLI has no headless system-prompt flag — use an `AGENTS.md` file in the working directory for persistent instructions instead. Setting `system_prompt` with `runtime: codex` is a hard validation error.

## Authoring workflow

1. Decide the DAG shape first (fan-out, chain, diamond). Sketch task ids and edges.
2. Pick workflow-level defaults to cover the majority of tasks; override only where escalation matters.
3. Keep prompts tight — each `{{id}}` placeholder injects the *entire* upstream output verbatim.
4. Run `loom check` until it's clean, then `loom run`. When params are declared, `loom check -p key=val` resolves `{{params.X}}` placeholders before printing the plan, so you can preview the actual prompts the model will see.

## Output

`loom run` writes plan + per-task progress + summary (tokens, cost, completion) to stdout. In parallel, it persists a self-contained JSON record of the run on disk:

```
.loom/runs/<workflow_id>/<run_id>.json     # full record, atomic rewrite per task event
.loom/runs/<workflow_id>/latest.json       # symlink to the most recent run
```

`<run_id>` is `YYYYMMDDTHHMMSSZ-<6 hex>` (UTC, sortable). The file embeds the verbatim manifest, per-task `prompt` (with placeholders already substituted), full `output`, `usage` (in / out / cache-read tokens, cost USD), timing, and status — plus a top-level `usage` total and `task_count`. It is rewritten on every `OnStart` / `OnFinish` via tmp+rename, so a crash mid-run still leaves a parseable file.

Failed or interrupted runs can be resumed: `loom resume <run-id>` (or `loom resume latest`) loads the record, seeds every task whose `status` is `ok` with its stored `output`, and only dispatches the remaining tasks. The original params block is reused, so no `-p` flags are required on the resume invocation. `loom run wf.yaml --resume-latest` is the same operation keyed off the workflow path instead of a run id.

Use it to inspect what the model actually saw and produced, or to compare runs:

```bash
jq '.usage,.task_count' .loom/runs/<workflow_id>/latest.json
jq '.tasks[] | {id, model, effort, usage, elapsed_ms}' .loom/runs/<workflow_id>/latest.json
```

Stdout is the only progress channel; task outputs do not leak to stdout beyond the summary line. They live in memory for `{{id}}` substitution downstream and on disk in the run record.
