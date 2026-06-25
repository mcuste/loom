# Loom workflow spec

A loom workflow is a single YAML document. This is the complete reference for
what it may contain. Every field, every constraint here is enforced by the
parser (`pkg/workflow`) and surfaced by `loom run check` before anything runs.

- [Document shape](#document-shape)
- [Top-level fields](#top-level-fields)
- [Tasks](#tasks)
  - [LLM tasks vs. command tasks](#llm-tasks-vs-command-tasks)
  - [Task fields](#task-fields)
- [Placeholders and dependencies](#placeholders-and-dependencies)
- [Runtime, model, effort](#runtime-model-effort)
- [Params](#params)
- [Conditionals: `when`](#conditionals-when)
- [Retry](#retry)
- [Output schema validation](#output-schema-validation)
- [Budgets](#budgets)
- [Caching (memoization)](#caching-memoization)
- [Cross-run state](#cross-run-state)
- [Scoped loops](#scoped-loops)
- [Sub-workflows](#sub-workflows)
  - [`loop`: converge until a target drains](#loop-converge-until-a-target-drains)
  - [`for_each`: iterate a finite list](#for_each-iterate-a-finite-list)
  - [`for_each_parallel`: iterate concurrently](#for_each_parallel-iterate-concurrently)
  - [Loop edge semantics](#loop-edge-semantics)
  - [`prev.<id>`: read the prior iteration](#previd-read-the-prior-iteration)
- [Validation reference](#validation-reference)

---

## Document shape

```yaml
name: my_workflow            # required
description: ...             # optional
runtime: claude-code         # optional default for tasks
model: sonnet                # optional default for tasks
effort: medium               # optional default for tasks
system_prompt: ...           # optional, appended to every LLM task
cache: false                 # optional workflow-wide memoization default
budget:                      # optional cumulative cost ceiling
  max_cost_usd: 2.50
params:                      # optional, see Params
  - name: ...
tasks:                       # required, non-empty
  - id: ...
```

**Unknown keys are rejected.** The decoder runs in known-fields mode, so any
top-level or task-level key the schema does not recognize is a hard error. There
is no `inputs:` or `uses:` key, and no top-level `loops:` key (loops are declared
inline as tasks; see [Scoped loops](#scoped-loops)). A task **may** link another
workflow with `workflow:` / `with:`, and a workflow **may** name its result task
with a top-level `output:` (see [Sub-workflows](#sub-workflows)).

Identifiers (`name`, every task `id`, every param `name`, loop ids, the
`for_each` `as` variable) all share one alphabet: **`[A-Za-z0-9_]+`**,
non-empty. They live in one global namespace; a task id, a param name, and a
loop id can never collide.

---

## Top-level fields

- **`name`** (required, string): workflow id. Must match `[A-Za-z0-9_]+`.
  Used as the directory name for run records.
- **`description`** (optional, string): shown in plan output only; never sent
  to a model.
- **`runtime`** (optional, enum): default runtime for tasks: `claude-code` or
  `codex`. A task with no runtime and no workflow default fails validation.
- **`model`** (optional, enum): default model. Valid values depend on the
  runtime (see [Runtime, model, effort](#runtime-model-effort)).
- **`effort`** (optional, enum): default effort. Valid values depend on the
  runtime.
- **`system_prompt`** (optional, string): appended to every LLM task's system
  prompt. May reference `{{params.x}}` and `{{state.x}}`, but **not**
  `{{task_id}}` (no task can be its dependency). The effective runtime must
  support a system prompt, or validation fails; `codex` does not, so
  `system_prompt` + `runtime: codex` is rejected.
- **`cache`** (optional, bool): workflow-wide memoization default; see
  [Caching](#caching-memoization). Absent = `false`.
- **`budget`** (optional, mapping): `{ max_cost_usd: <positive float> }`;
  cumulative spend ceiling. See [Budgets](#budgets).
- **`params`** (optional, list): declared CLI parameters. See [Params](#params).
- **`output`** (optional, string): names the task whose output is this
  workflow's result string when it is run as a sub-workflow. Absent = the lone
  terminal task (a task no other task depends on); 0 or >1 terminals with no
  `output:` is an error. See [Sub-workflows](#sub-workflows).
- **`tasks`** (required, list): non-empty. The workflow's tasks (and inline
  loop wrappers) in declaration order.

The `runtime` / `model` / `effort` set here are **defaults**: a task inherits
each one it does not override. Pick defaults that cover the majority of tasks
and override only where a task needs to step up or down.

---

## Tasks

`tasks:` is a non-empty list. Each entry is either an executable task (an LLM
prompt or a shell command) or a loop wrapper (a task carrying a `loop:` or
`for_each:` block; see [Scoped loops](#scoped-loops)).

### LLM tasks vs. command tasks

Every executable task sets **exactly one** of `prompt:`, `prompt_file:`, or
`command:`. Setting more than one, or none, is a hard validation error.
`prompt_file:` is a path form of `prompt:` — it is inlined at parse time and
is otherwise identical to an inline `prompt:` body.

- A **prompt task** dispatches its (substituted) prompt to the effective
  runtime/model/effort. Its output is the model's text response.
- A **command task** runs `sh -c <substituted-command>` instead of calling a
  model. Its captured **stdout** becomes the task's output, consumable
  downstream via `{{task_id}}` exactly like model output. Stderr streams live
  and is not captured (it surfaces in the error message on failure). A non-zero
  exit code fails the task and propagates through the DAG like any other error.

Command tasks have no model, so `runtime:`, `model:`, and `effort:` at the task
level are **rejected** (workflow-level defaults are tolerated and silently
ignored). A command task may not set a `schema:` either. Everything else
(`depends_on`, `when`, `retry`, `writes_state`, `budget`, `cache`,
placeholders) works identically.

> **Security note.** Model output spliced into a `command:` body via
> `{{task_id}}` is untrusted input. If an upstream task is fed user-supplied
> data, shell injection is possible. Quote or sanitize values before splicing
> them into commands.

### Task fields

- **`id`** (all tasks, string): required, unique, `[A-Za-z0-9_]+`.
- **`prompt`** (LLM, string): the text sent to the model. Non-empty. Mutually
  exclusive with `prompt_file` and `command` — set exactly one.
- **`prompt_file`** (LLM, string): path to a plain-text file whose content is
  used as the prompt. The path must be **relative** (absolute paths are
  rejected) and is resolved relative to the directory containing the workflow
  YAML. The file is read and inlined before validation, so the run record
  embeds the verbatim prompt text (not the path). Mutually exclusive with
  `prompt` and `command`. To keep these prompt files beside the workflow, put
  the workflow in its own registry directory as `<name>/<name>.yaml` (the
  registry collapses the redundant segment, so it still runs as `<name>` — see
  the [CLI registry docs](cli.md)) and reference siblings like
  `prompt_file: prompts/step.md`.
- **`command`** (shell, string): body run via `sh -c`. Non-empty. Mutually
  exclusive with `prompt` and `prompt_file`.
- **`description`** (all tasks, string): plan output only; never sent to a model.
- **`runtime`** (LLM, enum): task-level override. Rejected on command tasks.
- **`model`** (LLM, enum): task-level override. Rejected on command tasks.
- **`effort`** (LLM, enum): task-level override. Rejected on command tasks.
- **`depends_on`** (all tasks, list): explicit DAG edges. Each entry names a
  known task and appears at most once.
- **`when`** (all tasks, string): conditional guard; the task is skipped when
  it evaluates false. See [Conditionals](#conditionals-when).
- **`retry`** (all tasks, mapping): retry policy. See [Retry](#retry).
- **`writes_state`** (all tasks, string): records this task's trimmed output to
  cross-run state under this key. See [Cross-run state](#cross-run-state).
- **`budget`** (all tasks, mapping): per-task retry-cost ceiling. See
  [Budgets](#budgets).
- **`schema`** (LLM, mapping): JSON-schema validation of the output. Rejected
  on command tasks. See [Output schema](#output-schema-validation).
- **`cache`** (all tasks, bool): per-task memoization override (`true`/`false`).
  See [Caching](#caching-memoization).
- **`loop`** (wrapper, mapping): marks this task as a scoped loop. See
  [`loop`](#loop-converge-until-a-target-drains).
- **`for_each`** (wrapper, mapping): marks this task as a `for_each` loop. See
  [`for_each`](#for_each-iterate-a-finite-list).
- **`for_each_parallel`** (wrapper, mapping): like `for_each`, but runs the body
  once per element concurrently. See
  [`for_each_parallel`](#for_each_parallel-iterate-concurrently).
- **`workflow`** (sub-workflow, string): a registry name or path of another
  workflow to run as a leaf. Rejected alongside any other body form, and
  task-level `runtime:`/`model:`/`effort:` are rejected (the child brings its
  own). See [Sub-workflows](#sub-workflows).
- **`with`** (sub-workflow, mapping): values passed to the linked child's
  params. Only valid with `workflow:`. Each key names a declared child param;
  each value is substituted against the parent context first.

A single task sets exactly one of `prompt`, `prompt_file`, `command`, `loop`,
`for_each`, `for_each_parallel`, or `workflow`.

---

## Placeholders and dependencies

Four placeholder namespaces are recognized, each resolved from its own source in
a single substitution pass (so a value that happens to contain `{{x}}` text is
never re-expanded):

- **`{{task_id}}`**: resolves to the full string output of an upstream task.
  Creates no edge (templating only); **must** also appear in `depends_on`.
- **`{{params.name}}`**: resolves to a resolved workflow parameter. Creates no
  edge; must name a declared param.
- **`{{state.key}}`**: resolves to a cross-run state value. Creates no edge; no
  declaration needed (a missing key yields the empty string).
- **`{{prev.id}}`**: resolves to a loop sibling's prior-iteration output.
  Creates no edge; loop body only, must name a member of the same loop, and
  must **not** be in `depends_on`.

The central rule: **a `{{task_id}}` placeholder must also be declared in
`depends_on`.** Placeholders never implicitly extend the graph — declare every
edge up front so the DAG is unambiguous. Repeating a placeholder is fine
(templating, not declaration). `depends_on` may also list a task that has no
placeholder, expressing a pure ordering constraint.

```yaml
tasks:
  - id: a
    prompt: produce a value

  - id: b
    depends_on: [a]          # required because {{a}} is referenced
    prompt: |
      Build on this: {{a}}
      And again: {{a}}        # repeating is fine

  - id: c
    depends_on: [a, b]        # 'a' here is a pure ordering edge (no {{a}} below)
    prompt: |
      Finalize {{b}}.
```

`{{params.name}}` is a separate namespace from task ids: `{{foo}}` is always a
task reference, never a param. If you write a bare `{{env}}` where you meant
`{{params.env}}`, the error carries a hint suggesting the prefix.

Cycles, unknown dependencies, duplicate dependencies, and unknown placeholders
all fail `loom run check`.

---

## Runtime, model, effort

These three route an LLM task to a backend. Resolution per task is
**task field → workflow default**. They are meaningless for command tasks
(rejected at the task level).

### `claude-code` (the `claude` CLI)

Models, pick by task difficulty:

- **`haiku`**: very simple mechanical work: rename, format, run a fixed
  command, summarize a short input.
- **`sonnet`**: standard work: implement from a clear plan, write tests against
  a contract, follow a spec.
- **`opus`**: the hardest work: architecture, ambiguity, design synthesis, hard
  debugging, adversarial review.

Efforts, pick by how much deliberation the task warrants:

- **`low`**: one-shot, mostly typing.
- **`medium`**: some reasoning, a couple of options weighed (the typical default).
- **`high`**: extended thinking, multi-step reasoning, careful verification.
- **`max`**: maximum compute; reserve for the hardest synthesis/critique/design
  steps.

`claude-code` supports `system_prompt`.

### `codex` (the `codex` CLI)

Requires `OPENAI_API_KEY` (or `CODEX_API_KEY`) in the environment, or a prior
`codex login`.

- **`gpt-5.5`**: reasoning; accepts effort.
- **`gpt-5.4`**: reasoning; accepts effort.
- **`gpt-5.4-mini`**: reasoning, smaller/faster; accepts effort.
- **`gpt-5.3-codex-spark`**: non-reasoning, text-only; effort is forwarded but
  may be ignored.

Efforts: `minimal`, `low`, `medium`, `high`, `xhigh`. An empty effort leaves the
runtime default.

`codex` does **not** support `system_prompt` (the CLI has no headless
system-prompt flag; use an `AGENTS.md` file in the working directory for
persistent instructions instead). Setting `system_prompt` with `runtime: codex`
is a hard validation error.

Because independent tasks run concurrently, a wide fan-out of cheap
`haiku`/`low` drafts costs roughly one task's wall-clock time, not N.

---

## Params

Params are named values supplied at run time and substituted via
`{{params.name}}`. They do **not** create DAG edges.

```yaml
params:
  - name: env
    description: Target environment
    required: true            # must be supplied via -p; no default allowed
  - name: replicas
    default: "1"              # string value used when -p omits it
  - name: tag
    default: latest
```

Each entry:

- **`name`** (required): `[A-Za-z0-9_]+`, unique.
- **`description`** (optional): plan output only.
- **`default`** (optional): string value used when no `-p` value is supplied.
  **String only**: `default: 1` and `default: true` are stringified to `"1"` /
  `"true"`. `default: ""` is a valid empty-string default and is distinct from
  "no default". A `null` default is rejected.
- **`required`** (optional): when `true`, the param must be supplied via `-p`.
  Mutually exclusive with `default`.

**Every declared param must be referenced** by at least one prompt, command, or
the `system_prompt`; an unused param fails validation.

Supply values on the CLI with repeatable `-p key=val`:

```bash
loom run workflows/deploy.yaml -p env=prod -p replicas=3
loom run workflows/deploy.yaml -p env=prod        # replicas, tag fall back to defaults
```

Resolution order (later wins): **declared default → `-p` value**. Unknown keys
and missing required params are hard errors. Duplicate `-p key=` for the same
key is an error (not last-wins). Values may contain `=` (only the first splits
key from value) and may be empty (`-p note=`).

`loom run check -p env=prod` resolves params before printing the plan, so you
can preview the exact prompts the model will see.

---

## Conditionals: `when`

A task may carry a `when:` guard. The executor evaluates it once the task's
dependencies have resolved; if it is false, the task is **skipped** (status
`skipped`, empty output) and its gate still closes so downstream tasks proceed.

A `when:` expression may reference **only this task's declared dependencies**
(referencing any other task, or the task's own id, is rejected at load time —
the executor evaluates after the dependency gates close, so other outputs may
not exist yet).

The grammar is deliberately tiny. An expression is exactly one of:

- **`{{id}} == "literal"`**: true when the output of `id` equals the string.
- **`{{id}} != "literal"`**: true when the output of `id` differs from the string.
- **`{{id}} < n`**: true when `id`'s output parses as an integer less than `n`.
- **`{{id}} > n`**: true when `id`'s output parses as an integer greater than `n`.
- **`contains({{id}}, "substr")`**: true when `id`'s output contains the substring.
- **`succeeded(id)`**: true when `id` ran to completion successfully.
- **`failed(id)`**: true when `id` ran and did **not** succeed (a *skipped*
  task is never "failed").

`==`/`!=`/`contains` take a double-quoted string (escapes `\"` and `\\` are
honored); `<`/`>` take an integer and error at run time if the referenced
output is not an integer.

```yaml
tasks:
  - id: lint
    command: "golangci-lint run ./... > /dev/null 2>&1; echo $?"

  - id: fix
    depends_on: [lint]
    when: '{{lint}} != "0"'        # only run the fix when lint reported nonzero
    prompt: |
      The linter failed. Inspect and fix the issues.
```

```yaml
  - id: rescue
    depends_on: [build]
    when: failed(build)            # only run when the build task failed
    prompt: Diagnose why the build failed and propose a fix.
```

---

## Retry

A per-task retry policy. The zero value (no `retry:` block) means no retry.

```yaml
  - id: flaky_call
    depends_on: [seed]
    retry:
      max: 3                 # retries AFTER the first attempt; must be >= 0
      backoff: exponential   # none | constant | exponential
      on: [transient]        # error classes that are retryable
    prompt: ...
```

- **`max`**: number of retries after the first attempt. Must be `>= 0`; `0`
  disables retry.
- **`backoff`**: `none` (no delay), `constant` (fixed base delay), or
  `exponential` (base, 2×base, …). Defaults to `exponential` when the block is
  present but `backoff` is omitted.
- **`on`**: error classes that are retryable. Only `transient` is currently
  recognized. Defaults to `[transient]` when omitted.

Retry applies to both LLM and command tasks. Unknown keys, a negative `max`, an
unknown backoff, or an unrecognized error class all fail validation.

---

## Output schema validation

An LLM task may declare a `schema:` block. After the task produces output, the
executor validates that the output parses as JSON and conforms to the schema; on
mismatch the task fails (and retries if a `retry:` policy is set). This is a
minimal JSON-Schema subset — properties nest one level deep.

```yaml
  - id: classify
    depends_on: [input]
    schema:
      type: object
      required: [label, confidence]
      properties:
        label:
          type: string
        confidence:
          type: string
    prompt: |
      Classify {{input}}. Respond ONLY with JSON:
      {"label": "...", "confidence": "..."}
```

- **`type`**: the declared JSON type, e.g. `object`.
- **`required`**: list of property names that must be present.
- **`properties`**: map of property name → `{ type: <json-type> }` (one level
  deep).

`schema:` is **rejected on command tasks** (validation applies only to LLM
output).

---

## Budgets

A `budget:` block caps cumulative cost in US dollars. It exists at two levels:

- **Workflow level** — caps total spend across all completed tasks. When the
  ceiling would be exceeded, the run aborts before further spend.
- **Task level** — caps the cumulative cost of a single task's retries. Once
  the task's accumulated cost would exceed it, no further retry is attempted.

```yaml
budget:
  max_cost_usd: 2.50         # whole-workflow ceiling

tasks:
  - id: expensive
    budget:
      max_cost_usd: 0.50     # this task's retries may not exceed $0.50
    retry: { max: 5 }
    prompt: ...
```

`max_cost_usd` must be a **positive float**. Zero, negative, NaN, infinity, an
absent value, or an unknown field inside the block all fail validation. A
missing `budget:` block means no limit.

---

## Caching (memoization)

Hash-based memoization skips a model call when an identical task has run before,
replaying the stored output. The cache key is the tuple
`(runtime, model, effort, system_prompt, prompt)`.

- Workflow level: `cache: true` opts every task in by default.
- Task level: `cache: true` / `cache: false` overrides the workflow default for
  that task. Absent (`nil`) inherits the workflow default.

```yaml
cache: true                  # workflow-wide default: memoize

tasks:
  - id: stable_summary
    prompt: Summarize the project README.   # cached after first run

  - id: always_fresh
    cache: false             # opt this one out
    prompt: What time is it conceptually right now?
```

**Command (shell) tasks are never memoized**, regardless of the `cache` setting
— their effects and stdout may depend on the filesystem and environment.
Caching is most valuable while iterating on a multi-task workflow: unchanged
upstream tasks replay for free.

---

## Cross-run state

State persists values **across separate runs** of the same workflow (distinct
from `{{task_id}}` outputs, which live only within one run). It is stored at
`$LOOM_HOME/state/<workflow_id>.json` (see [cli.md](cli.md) for `LOOM_HOME`).

- A task with `writes_state: <key>` records its **trimmed output** under that
  key after the run completes.
- Any prompt, command, or the `system_prompt` may read state via
  `{{state.<key>}}`. A missing key substitutes to the empty string (legitimately
  empty on the first run), so it never leaks braces and needs no declaration and
  creates no edge.

```yaml
system_prompt: |
  Previous run's notes (may be empty on first run):
  {{state.notes}}

tasks:
  - id: work
    prompt: Do the task, building on any prior notes above.

  - id: remember
    depends_on: [work]
    writes_state: notes        # next run sees this via {{state.notes}}
    prompt: |
      Summarize for next time, in 3 bullets:
      {{work}}
```

`writes_state` must match `[A-Za-z0-9_]+` and is allowed on both LLM and command
tasks.

---

## Scoped loops

A **scoped loop** is a named subgraph that the run pipeline re-executes; only
the loop's own body tasks repeat, the rest of the DAG runs once. There are two
forms, both declared **inline as a task** carrying a block instead of a
`prompt`/`command`:

- `loop:` — re-runs the body **until a convergence target drains** (a
  while-style loop with a hard iteration cap).
- `for_each:` — runs the body **once per element** of a finite list,
  sequentially. `for_each_parallel:` is the same, run concurrently.

The wrapper task's `id` is the loop id, and the loop renders in the execution
flow at its position. There is **no top-level `loops:` block** (a stray one is
rejected as an unknown field).

A loop-wrapper task may carry only its `id`, `description`, and the loop block.
It must **not** set `prompt`/`command`, runtime knobs (`runtime`/`model`/
`effort`), or task-only fields (`depends_on`, `when`, `writes_state`, `schema`,
`retry`, `budget`, `cache`) — those belong to the body tasks. A task setting
more than one of `loop:`, `for_each:`, and `for_each_parallel:` is rejected.

The loop id shares the global namespace: it must be unique and distinct from
every task id and param name. The loop body (`tasks:`) is non-empty and uses the
identical task schema. It is otherwise an ordinary DAG: members carry their own
`depends_on` edges and **need not be connected to one another**, so independent
members run **in parallel within a pass**, exactly like independent top-level
tasks. Each body task belongs to exactly one loop.

### `loop`: converge until a target drains

```yaml
tasks:
  - id: seed
    prompt: List open problems, one per line.

  - id: work                       # the loop id (wrapper task)
    description: Drain the problem list, refining as we go.
    loop:
      until_empty: drain           # converge when 'drain' output is empty
      # --- OR ---
      # until: '{{drain}} == "done"'   # a when-style expression over members
      max: 4                       # required, >= 1; hard cap on iterations
      tasks:
        - id: drain
          depends_on: [seed]
          prompt: |
            Remaining problems: {{seed}}
            Already handled last pass: {{prev.refine}}
            Output the still-unsolved ones, or nothing if all solved.
        - id: refine
          depends_on: [drain]
          prompt: Refine the handling of {{drain}}.

  - id: report                     # exit consumer (depends on a body member)
    depends_on: [drain]
    prompt: |
      Summarize the final state:
      {{drain}}
```

Inside the `loop:` block, only these keys are allowed:

- **`until_empty`**: names a body member whose **empty trimmed output** ends
  the loop.
- **`until`**: a `when`-style expression that ends the loop when true. May
  reference **only members of this loop**.
- **`max`**: **required**, `>= 1`. Bounds iterations even if the target never
  converges.
- **`tasks`**: **required**, non-empty. The loop body.

Exactly one of `until_empty` or `until` must be set.

### `for_each`: iterate a finite list

`for_each` runs its body once per element of a list, **sequentially**, binding
each element to a loop variable.

```yaml
tasks:
  - id: discover
    command: "ls cmd"               # newline list (or a JSON array of strings)

  - id: process                     # the loop id (wrapper task)
    for_each:
      in: [redis, postgres]         # static list -- OR -- a single placeholder: in: '{{discover}}'
      as: backend                   # the loop variable, bound per element
      tasks:
        - id: probe
          prompt: |
            Probe the {{backend}} backend.
            Prior pass output: {{prev.probe}}

  - id: report                      # exit consumer
    depends_on: [probe]
    prompt: |
      Summarize: {{probe}}
```

Inside the `for_each:` block:

- **`in`**: either a static YAML sequence (`[a, b, c]`) **or** a single
  `{{...}}` placeholder scalar resolved at run time. A `{{taskid}}` source makes
  that task an entry dependency; a `{{params.x}}` or `{{state.x}}` source needs
  no edge. A dynamic source is parsed as a JSON array of strings, else split on
  newlines (blank entries dropped).
- **`as`**: the loop variable, `[A-Za-z0-9_]+`, distinct from every task id and
  param name. Reference it as `{{as}}` in any body member; it is bound per
  iteration and is **exempt from `depends_on`** (it is not a task output).
- **`tasks`**: **required**, non-empty. The loop body.

There is no `until_*` or `max` — the list length fixes the iteration count. An
**empty list runs zero iterations**: the members never run, the loop closes
their gates immediately, and an exit consumer sees empty output. Passes run in
list order; a body member's published output is the **final** iteration's value
(not a join across iterations).

### `for_each_parallel`: iterate concurrently

`for_each_parallel` is the concurrent spelling of `for_each`: an identical
`in` / `as` / `tasks` block, but every element's pass runs **at the same time**
instead of in list order. Use it to fan a body out over independent items.

```yaml
tasks:
  - id: fan
    for_each_parallel:
      in: '{{discover}}'
      as: item
      tasks:
        - id: handle
          prompt: "Process {{item}}."
```

It shares every `for_each` rule (static or dynamic `in`, the `as` loop variable,
the required non-empty `tasks`, empty-list-runs-nothing, the entry/internal/exit
edge semantics) with two differences:

- Each pass runs over an **isolated copy** of the member outputs, so one item's
  body never observes another's results. A multi-member body whose tasks
  reference each other (`b` reads `{{a}}`) resolves those references **within the
  same pass**.
- **`{{prev.<id>}}` is rejected** inside a parallel body: the passes have no
  ordering, so there is no prior iteration to read (`loom run check` fails).

Because the passes race, **which pass's value an exit consumer reads for a given
member is unspecified**. A downstream task that needs a specific element's result
must reference that element, not the loop member. Cost-budget and usage
accounting stay global across all passes, exactly as with the sequential loop.

### Loop edge semantics

A loop body has three edge kinds, distinguished by where each `depends_on`
endpoint lives:

- **Entry edge** — a body task depends on a non-member (e.g. `drain` →
  top-level `seed`). Satisfied once, before the loop starts; the upstream output
  is the same for every iteration.
- **Internal edge** — a body task depends on another member of the same loop
  (e.g. `refine` → `drain`). Orders the tasks *within* each iteration.
- **Exit edge** — a non-member depends on a body member (e.g. `report` →
  `drain`). The downstream task runs once, after the loop finishes, seeing the
  final iteration's output.

### `prev.<id>`: read the prior iteration

Inside a loop body, `{{prev.<member>}}` injects the **prior iteration's** output
of a sibling member (empty on the first iteration). It lets an iteration build
on the last one without forming a cycle.

A `{{prev.<member>}}` reference:

- is valid **only inside a loop body**, and may only name a member of that same
  loop (otherwise `loom run check` fails);
- is **exempt from the `depends_on` rule** and must **not** be listed in
  `depends_on` — it reads across iterations, not within one, so adding the edge
  would form a cycle.

In the `loop` example above, `drain` reads `{{prev.refine}}` yet declares only
`depends_on: [seed]`.

---

## Sub-workflows

A task may **link and run another workflow** instead of carrying a prompt or
command. Set `workflow:` to a registry name or path; the linked child runs as a
single leaf in the parent DAG, and its result becomes the task's output, usable
downstream via `{{task_id}}` like any other.

```yaml
# .loom/workflows/release/release.yaml
name: release
output: publish              # this task's output is the workflow's result
params:
  - name: version
    required: true
tasks:
  - id: build
    prompt: build {{params.version}}
  - id: publish
    depends_on: [build]
    prompt: publish {{build}}

# parent workflow
tasks:
  - id: cut_release
    workflow: release        # registry name OR path (e.g. ./sub.yaml)
    with:
      version: "1.4.0"       # values substituted with the PARENT ctx first
  - id: announce
    depends_on: [cut_release]
    prompt: "announce {{cut_release}}"   # == release's publish output
```

- **`workflow:`** resolves through the same registry rules as `loom run <name>`
  (a name resolves against the local and global registries; a path ref resolves
  relative to the linking workflow's directory).
- **`with:`** binds values to the child's params. Each key must be a declared
  child param; required child params must be covered. Values are substituted
  against the **parent** context first (so `with: { version: "{{seed}}" }` passes
  the parent `seed` task's output), which also creates the implicit DAG edge.
- **Result.** The task output is the child task named by the child's top-level
  `output:`, or the child's lone terminal task when `output:` is omitted. A
  child with 0 or >1 terminals and no `output:` fails `loom run check`.

### v1 semantics

The sub-workflow task is an **atomic black box**:

- The parent shows **one task row** for it, carrying the child result and the
  **summed** token/cost usage. There are no per-child task rows.
- It is atomic for **resume**: if the task is not yet `ok`, the whole child
  re-runs (same as a prompt or command task).
- Children are **re-resolved from disk** at run *and* resume time, not frozen
  into the parent manifest the way `prompt_file:` is.
- A child's `writes_state` does **not** persist in v1 (state write-back is a
  CLI-layer pass over the top-level report only).
- Child `prompt_file:` paths resolve beside the **child** YAML.
- A task-level `schema:` validates the child result uniformly with an LLM task;
  `retry:` / `budget:` wrap the whole child run the same way.

Cycles between linked workflows (A links B links A) are detected and rejected.

---

## Validation reference

`loom run check <file>` runs the full validation pipeline, roughly in this
order. Any failure stops the check and prints a precise message.

1. Workflow `name` and every task `id` match `[A-Za-z0-9_]+`.
2. Task ids are unique (across top-level and every loop body).
3. Loop ids and `for_each` `as` variables are valid, unique, and do not collide
   with task ids or param names.
4. Params: names valid and unique; `required` and `default` are mutually
   exclusive; defaults are scalar strings (no null); every declared param is
   referenced somewhere.
5. Each task sets exactly one of `prompt` / `prompt_file` / `command` / `loop` / `for_each` / `for_each_parallel` / `workflow`. `prompt_file` paths are relative and the referenced file must be readable.
6. Command and sub-workflow tasks set no task-level `runtime`/`model`/`effort`;
   command tasks also set no `schema`. Loop wrappers set none of the body-only
   fields. A linked `workflow:` resolves, its `with:` covers the child's
   required params with no unknown keys, and the child's `output:` resolves.
7. Every `depends_on` entry names a known task and appears at most once.
8. Every `{{task_id}}` placeholder is in that task's `depends_on`; every
   `{{params.x}}` names a declared param; `{{prev.x}}` is loop-body-only and
   names a member of the same loop.
9. `system_prompt` has no `{{task_id}}` placeholders.
10. The task graph has no cycles.
11. Each loop sets a valid convergence/iteration spec (a `loop:` names a member
    as its target) and a non-empty body. The body is an ordinary DAG; its
    members need not be connected.
12. Each LLM task's effective `runtime`/`model`/`effort` is accepted by the
    registered runtime; `system_prompt` is only set for a runtime that supports
    it.

When it prints clean, the plan shows the execution order, each task's
runtime/model/effort, and each loop as a labeled group (its convergence target
and `max`, or its `as` variable and list source). Run it until clean, then
`loom run`.
