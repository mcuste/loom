---
name: loom
description: Author and run loom workflows — YAML DAGs of LLM tasks executed by the `loom` CLI installed from this repo. Use when the user wants to "write a loom workflow", "build a loom DAG", "run this with loom", "check a loom yaml", "chain prompts with loom", or asks for help with `loom run` / `loom run check`.
allowed-tools: Bash(loom *), Read, Write, Edit, Glob, Grep
---

Author and execute loom workflows. Loom parses a YAML workflow, builds a DAG over `{{id}}` placeholders + `depends_on`, and dispatches each task to a registered runtime (`claude-code`, which shells out to the `claude` CLI, or `codex`, which shells out to the `codex` CLI).

## CLI

```bash
loom run check <workflow>                      # parse + validate + print execution order, no execution
loom run       <workflow>                      # check + execute every task (independent tasks run concurrently)
loom run       <workflow> --resume-latest      # resume the last run of this workflow: skip ok tasks, re-run the rest
loom resume    <run-id>                        # resume a specific run; "latest" follows latest.json
loom runs                                      # browse past runs (TUI); `ls` lists, `show <id>` prints one inline
loom workflows ls                              # list registry workflows runnable by name
```

`<workflow>` is a YAML path **or a registry name**. A name has no path separator and either contains `:` (hierarchy separator, e.g. `deploy:prod`) or lacks a `.yaml`/`.yml` extension. Names are resolved by searching registry roots in order: project-local `.loom/workflows/` dirs walking up from cwd to the git repo root (stop at `.git`), then global `$LOOM_HOME/workflows/` — nearest root wins (shadows global). `loom workflows ls` lists the merged registry. A nested path maps to a `:`-joined name (`ci/test.yaml` → `ci:test`), except the eponymous-dir form `<name>/<name>.yaml` collapses to just `<name>` — put a workflow in its own dir so its `prompt_file:` text sits beside it (a flat `<name>.yaml` wins if both exist). Name resolution is exact-only; the workflow argument tab-completes against the registry (`loom completion <shell>`), so a typed prefix like `tui<TAB>` expands to `tui_demo` at the shell. See `docs/cli.md` for the full classification rule and search-order details.

Always run `loom run check <file>` first when authoring — it surfaces every validation error (cycles, unknown deps, unknown placeholders, bad model/effort) without burning tokens on `claude`.

`loom run` prints the plan, per-task progress (id, runtime/model/effort, tokens, cost), the run-file path, and a final summary. Independent tasks (no edge between them in the DAG) are dispatched concurrently — fan-out is free, you don't need to wait for siblings.

## YAML schema

Top level:

```yaml
name: my_workflow            # required, [A-Za-z0-9_]+
description: ...             # optional, plan-output only
runtime: claude-code         # default for tasks; one of: claude-code | codex
model: sonnet                # default; one of: haiku | sonnet | opus
effort: medium               # default; one of: low | medium | high | max  (claude-code)
system_prompt: ...           # optional default system prompt; per-task overridable
output: <task_id>            # optional; which task is this workflow's result when linked as sub-workflow
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
    system_prompt: ...       # optional task-level override of the workflow default
    # — OR —
    system_prompt_file: prompts/sys_a.txt  # file-backed override; inlined before validation
    depends_on: [a, b]       # optional; explicit DAG edges
    prompt: |                # exactly one of prompt / prompt_file / command; non-empty
      Text with {{a}} and {{b}} placeholders.
    # — OR —
    prompt_file: prompts/step_a.txt   # relative path; inlined before validation
    # — OR —
    command: |               # runs sh -c instead of dispatching to a runtime
      echo "{{a}}" | wc -l
    # — OR —
    workflow: release        # run another workflow (registry name or ./path) as a leaf
    with:                    # values bound to the child's params; substituted with parent ctx
      version: "{{a}}"
```

`prompt_file` is a path to a plain-text file resolved relative to the workflow YAML's directory. The file is read and inlined before validation, so the run record stores the verbatim prompt text (not the path). Use it to keep long or shared prompts out of the YAML.

`system_prompt` is the system prompt sent to the runtime. The workflow-level value is the default for every LLM task; a task-level `system_prompt` (or `system_prompt_file`, the file-backed spelling, inlined like `prompt_file`) overrides it for that one task, falling back to the workflow default when unset. The inline and file spellings are mutually exclusive on the same scope. It carries `{{params.x}}` / `{{state.k}}` placeholders (never task-id placeholders) and is meaningless on command and sub-workflow tasks, which reject it.

A task may instead link another workflow with `workflow:` (a registry name or a path relative to the linking YAML); `with:` binds values to the child's params (substituted against the parent context first, which also creates the dep edge). The child runs as one atomic leaf: its result (the child's top-level `output:` task, or its lone terminal task) becomes this task's output. A top-level `output:` names which task is this workflow's result when it is itself linked. See `docs/workflow-spec.md` → Sub-workflows.

Unknown top-level or task-level keys are **rejected** (parser uses `KnownFields(true)`). No `inputs:` or `uses:` key.

## Placeholders and dependencies

- `{{task_id}}` in a prompt is substituted with that upstream task's full string output at runtime.
- Every `{{id}}` placeholder **must also appear in `depends_on`**. Placeholders never implicitly extend the DAG. Repeating a placeholder is fine; it's templating, not declaration. The one exception is the `{{prev.<id>}}` loop placeholder, which is explicitly exempt (see [Scoped loops](#scoped-loops)).
- `depends_on` may list a task with no placeholder (pure ordering constraint).
- Cycles, unknown deps, duplicate deps, and unknown placeholders fail `loom run check`.

## Command tasks

A task with `command:` runs `sh -c <substituted-command>` instead of dispatching to a runtime.

**Discriminator rule** — exactly one of `prompt`, `prompt_file`, `command`, or `workflow` must be set per task (loop/for_each wrappers replace all of them). Setting more than one, or none, is rejected by the parser (`loom run check` surfaces the error before execution).

**Rejected fields on command tasks** — `runtime`, `model`, `effort`, and `system_prompt` (or `system_prompt_file`) at the task level are hard validation errors. Workflow-level defaults are tolerated (a shell task silently ignores them), but task-level overrides are rejected. Sub-workflow (`workflow:`) tasks reject the same fields.

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
loom run workflows/deploy.yaml -p env=prod -p replicas=3
loom run check workflows/deploy.yaml -p env=prod
```

Resolution order (right-to-left wins): declared defaults → `-p` values. Unknown keys or missing required params are hard errors.

## Scoped loops

A **scoped loop** is a named subgraph that the run pipeline re-executes until a convergence target is reached; only the loop's own body tasks re-run, the rest of the DAG runs once. A loop is declared **inline as a task** carrying a `loop:` block instead of a `prompt:`/`command:`. The wrapper task's `id` is the loop id, and the loop renders in the execution flow at its position (not a separate section).

There is **no top-level `loops:` block** — a stray top-level `loops:` is rejected as an unknown field. Loops live in `tasks:`:

```yaml
tasks:
  - id: seed
    prompt: seed it
  - id: work                   # the loop id (a task carrying loop:, not prompt/command)
    description: ...            # optional; becomes the loop's description in plan output
    loop:
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
  - id: report                 # exit consumer: depends on a body MEMBER
    depends_on: [drain]
    prompt: summarize {{drain}}
```

Rules (all surfaced by `loom run check`):

- **Discriminator** — a task sets exactly one of `prompt`, `command`, `loop`, `for_each`, or `for_each_parallel` (`loop:`, `for_each:`, and `for_each_parallel:` are sibling scoped-block forms; a task setting more than one is rejected). A loop-wrapper task must not also set `prompt`/`command`, runtime knobs (`runtime`/`model`/`effort`), or task-only fields (`depends_on`, `when`, `writes_state`, `schema`, `retry`, `budget`, `cache`) — those belong to the body tasks. Inside the `loop:` block only `until_empty`/`until`, `max`, and `tasks` are allowed; `id` and `description` come from the wrapper task.
- **Loop id** — the wrapper task id; `[A-Za-z0-9_]+`, unique, and distinct from every other task id and param name.
- **Convergence** — exactly one of `until_empty` or `until` per loop. `until_empty` names a body member whose empty trimmed output ends the loop; `until` is a `when`-style expression that may reference **only** members of the same loop.
- **`max`** — required and `>= 1`; bounds iterations even if the target never converges.
- **Body** — `tasks:` is non-empty and uses the identical task schema (prompt/command, model/effort, etc.). Each body task carries its loop id; a task belongs to at most one loop.
- **DAG body** — the body is an ordinary DAG; members need not be connected to one another. Members with no `depends_on` between them run **in parallel within a pass**, exactly like independent top-level tasks. `depends_on` edges between members order them within a pass.

### `for_each:` block — iterate a finite list

A `for_each:` block is the loop's sibling: instead of converging, it runs its body **once per element** of a finite list, sequentially (iteration count = `len(list)`). It is declared inline as a task carrying `for_each:` (not `prompt`/`command`/`loop`), with the same wrapper rules as `loop:`.

```yaml
tasks:
  - id: discover
    command: "ls cmd"          # produces a newline list (or a JSON array of strings)
  - id: process                # the loop id (a task carrying for_each:)
    for_each:
      in: [redis, postgres]    # static list — OR — a single placeholder: in: '{{discover}}'
      as: backend              # the loop variable, bound to each element in turn
      tasks:                   # required, non-empty; same task schema as top-level
        - id: probe
          prompt: probe {{backend}} building on {{prev.probe}}
  - id: report                 # exit consumer: depends on a body MEMBER
    depends_on: [probe]
    prompt: summarize {{probe}}
```

- **`in`** — either a static YAML sequence (`[a, b, c]`) or a single `{{...}}` placeholder scalar resolved at run time. A `{{taskid}}` source makes that task an entry dependency (the list is resolved once it closes); a `{{params.x}}` or `{{state.x}}` source needs no edge. A dynamic source is parsed as a JSON array of strings, else split on newlines, with blank entries dropped.
- **`as`** — the loop variable; `[A-Za-z0-9_]+`, and distinct from every task id and param name. Reference it as `{{as}}` in any body member's prompt, command, or sub-workflow `with:` value; it is bound per iteration and **exempt from `depends_on`** (it is not a task output).
- **No `until_*` / `max`** — the list length fixes the iteration count. An **empty list runs zero iterations**: the members never run and the loop closes their gates immediately, so an exit consumer sees empty output (this composes with loop-until-dry: nothing to process reads as drained).
- **Sequential** — passes run in list order; `{{prev.<member>}}` carries the prior pass's output forward exactly as in a `loop:`. A body member's published output is the **final** iteration's value (loop semantics), not a join across iterations.

### `for_each_parallel:` block — iterate concurrently

`for_each_parallel:` is the concurrent spelling of `for_each:`: an identical `in`/`as`/`tasks` block, but every element's pass runs **at the same time** instead of in list order. Reach for it to fan a body out over independent items (probe N services, fix N bugs) where the passes do not depend on each other.

```yaml
tasks:
  - id: fan
    for_each_parallel:
      in: '{{discover}}'
      as: item
      tasks:
        - id: handle
          prompt: process {{item}}
```

It shares every `for_each:` rule (static/dynamic `in`, the `as` variable, required non-empty `tasks`, empty-list-runs-nothing, the entry/internal/exit edges) with two differences:

- **Isolated passes** — each pass runs over its own copy of the member outputs, so one item never observes another's results; a multi-member body whose tasks reference each other (`b` reads `{{a}}`) resolves those references within the same pass.
- **No `{{prev.<id>}}`** — the passes have no ordering, so there is no prior iteration to read; a `prev` reference inside a parallel body fails `loom run check`.

Because the passes race, **which pass's value an exit consumer reads for a member is unspecified** — a downstream task needing a specific element must reference that element, not the loop member. Budget and usage stay global across all passes.

### Edge semantics

A loop body has three edge kinds, distinguished by where each `depends_on` endpoint lives:

- **Entry edge** — a body task depends on a non-member (e.g. `drain` depends on the top-level `seed`). Entry edges are satisfied once, before the loop starts; the upstream output is the same for every iteration.
- **Internal edge** — a body task depends on another member of the same loop (e.g. `refine` depends on `drain`). Internal edges order the tasks *within* each iteration.
- **Exit edge** — a non-member depends on a body member. The downstream task runs once, after the loop converges, seeing the final iteration's output.

### `prev.<id>` placeholder

Inside a loop body, `{{prev.<member>}}` injects the **prior iteration's** output of a sibling member (empty on the first iteration). It lets an iteration build on the last one without forming a cycle — `drain` above reads `{{prev.refine}}` to carry state forward. A `prev` reference is valid only inside a loop body and may only name a member of that same loop; using it elsewhere or across loops fails `loom run check`.

Unlike a plain `{{id}}` placeholder, a `{{prev.<member>}}` reference is **exempt from the `depends_on` rule** and must *not* be listed in `depends_on`: it reads across iterations, not within one, so adding the edge would form a cycle. In the example above `drain` reads `{{prev.refine}}` yet only declares `depends_on: [seed]`.

`loom run check` renders each loop as a labeled group showing its id, a kind-specific summary (a `loop:` shows its convergence target `until_empty=`/`until=` and `max`; a `for_each:` shows `as=` and its list source, `static[n]` or `dynamic<-{{src}}`), and every body task with its deps, so the loop's execution shape is visible without running it.

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

**`system_prompt` is not supported** by the `codex` runtime. Codex CLI has no headless system-prompt flag — use an `AGENTS.md` file in the working directory for persistent instructions instead. Setting `system_prompt` (workflow- or task-level, including a task-level override under `runtime: codex`) is a hard validation error; `loom run check` reports it via the effective runtime per task.

## Authoring workflow

1. Decide the DAG shape first (fan-out, chain, diamond). Sketch task ids and edges.
2. Pick workflow-level defaults to cover the majority of tasks; override only where escalation matters.
3. Keep prompts tight — each `{{id}}` placeholder injects the *entire* upstream output verbatim.
4. Run `loom run check` until it's clean, then `loom run`. When params are declared, `loom run check -p key=val` resolves `{{params.X}}` placeholders before printing the plan, so you can preview the actual prompts the model will see.

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
