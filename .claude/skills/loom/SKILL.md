---
name: loom
description: Author and run loom workflows — YAML DAGs of LLM tasks executed by the `loom` CLI installed from this repo. Use when the user wants to "write a loom workflow", "build a loom DAG", "run this with loom", "check a loom yaml", "chain prompts with loom", or asks for help with `loom run` / `loom run check`.
allowed-tools: Bash(loom *), Read, Write, Edit, Glob, Grep
---

Loom parses a YAML workflow into a DAG (edges from `depends_on` + `{{id}}` placeholders) and dispatches each task to a runtime (`claude-code` shells out to `claude`; `codex` shells out to `codex`). Independent tasks run concurrently.

## CLI

```bash
loom run check <wf> [-p k=v]   # parse + validate + print plan; no execution. ALWAYS run first.
loom run       <wf> [-p k=v]   # check + execute
loom run       <wf> --resume-latest   # skip ok tasks from last run, re-run the rest
loom resume    <run-id|latest>        # resume a specific run
loom runs                      # browse past runs (TUI); `ls`, `show <id>`
loom workflows ls              # list registry workflows runnable by name
```

`<wf>` is a YAML path **or registry name** (no path sep; has `:` or no `.yaml`). Names resolve through `.loom/workflows/` dirs walking cwd→repo root, then global `$LOOM_HOME/workflows/`; nearest wins. `ci/test.yaml`→`ci:test`; eponymous dir `<name>/<name>.yaml`→`<name>`.

## YAML schema

Unknown keys are **rejected** (`KnownFields(true)`). No `inputs:`/`uses:`.

```yaml
name: my_workflow          # required, [A-Za-z0-9_]+
description: ...           # optional, plan-output only
runtime: claude-code       # default for tasks: claude-code | codex
model: sonnet              # default: haiku | sonnet | opus
effort: medium             # default: low | medium | high | max (claude-code)
system_prompt: ...         # optional default; per-task overridable
output: <task_id>          # which task is the result when this wf is linked as a sub-workflow
params:                    # optional; see Params
  - {name: env, description: ..., default: "1", required: false}

tasks:
  - id: <task_id>          # required, [A-Za-z0-9_]+, unique
    description: ...        # optional
    runtime/model/effort: ...        # optional task-level overrides (LLM tasks only)
    system_prompt: ... | system_prompt_file: prompts/s.txt   # task override (mutually exclusive)
    depends_on: [a, b]     # explicit DAG edges
    when: '{{a}} == "x"'   # optional guard; skip task if false
    # exactly ONE body kind (discriminator):
    prompt: "use {{a}} and {{params.env}}"   # — or — prompt_file: prompts/a.txt
    command: 'echo {{a}} | wc -l'            # sh -c; stdout = output; non-zero fails (unless ok_exit)
    script: ./x.sh                           # runs file directly (shebang honored); exit is data
    args: ["{{a}}", "prod"]                  #   optional argv, script-only, substituted
    workflow: release                        # link another wf as one atomic leaf
    with: {version: "{{a}}"}                 #   bind parent ctx → child params (creates dep edge)
    loop: {...} | for_each: {...} | for_each_parallel: {...}   # scoped subgraphs (below)
    ok_exit: [1]           # extra non-zero exit codes to treat as success
```

- `prompt_file` resolves relative to the **YAML dir**, inlined before validation. `script` path resolves relative to **loom's cwd**.
- `system_prompt` carries `{{params.x}}`/`{{state.k}}` only (no task refs). Rejected on `command`/`script`/`workflow` tasks and on `codex` runtime (Codex has no headless flag — use `AGENTS.md`).

## Placeholders & dependencies

- `{{id}}` injects that upstream task's **entire** string output. Every `{{id}}` **must also appear in `depends_on`** — placeholders never implicitly add edges.
- Exempt from the depends_on rule (do NOT list them): `{{params.x}}`, `{{state.x}}`, `{{prev.<member>}}`, loop var `{{as}}`.
- `{{id.exit}}` = upstream decimal exit code; also creates a dep edge. In `when:`, compares to a bare int (`{{c.exit}} != 0`); string ops quote (`{{a}} == "done"`).
- `depends_on` without a placeholder = pure ordering. Cycles, unknown deps/placeholders, dup deps fail `loom run check`.

## Body kinds

- **prompt / prompt_file** — dispatched to the runtime.
- **command** — `sh -c`; stdout is the output; non-zero exit **fails** the task unless tolerated by `ok_exit`. Untrusted `{{id}}` splices are a shell-injection risk — quote/sanitize.
- **script** — runs the file directly; **non-zero exit does NOT fail** (exit is data). Only launch failure (missing/not executable) errors. `ok_exit` *narrows* tolerance to listed codes. Rejects `runtime`/`model`/`effort`/`system_prompt`/`schema`.
- **workflow** — runs another workflow as one leaf; child's `output:` (or lone terminal) task is this task's result.

```yaml
- id: check
  script: ./scripts/healthcheck.sh
  args: ["{{params.env}}"]
- id: alert
  depends_on: [check]
  when: "{{check.exit}} != 0"
  command: echo "failed {{check.exit}}: {{check}}"
```

**ok_exit** — non-zero codes counted as success (0 always succeeds). Lets `command`/LLM tasks opt into exit-is-data so you can branch on `{{id.exit}}`. Rejected on `workflow`/loop-wrapper tasks. A tolerated non-zero LLM task has empty `{{id}}` output (no response) and is never memoized. Even without `ok_exit`, a failing task records its exit code (shown as `exit=N`).

## Params

Top-level declarations, referenced as `{{params.name}}` in prompts/`command`/`system_prompt`/`with`. Separate namespace from task ids; no DAG edges. String values only (`default: 1`→`"1"`). `required: true` forbids `default`. Pass with repeatable `-p key=val`; `-p` overrides declared default; unknown/missing-required keys are hard errors.

## Scoped loops

A task carrying `loop:`/`for_each:`/`for_each_parallel:` (instead of a body) wraps a subgraph that re-runs; the rest of the DAG runs once. The wrapper id is the loop id. Wrapper must NOT set a body, runtime knobs, or task fields (`depends_on`/`when`/`schema`/`retry`/`budget`/`cache`/`writes_state`) — those go on body tasks. No top-level `loops:` block.

**Edge kinds** (by where `depends_on` points): *entry* (member→non-member, satisfied once before loop), *internal* (member→member, orders within a pass), *exit* (non-member→member, runs once after convergence, sees final pass).

**`loop:`** — converge on `until_empty: <member>` (empty trimmed output) **or** `until: '<when-expr over members>'`; `max: N` (≥1, required cap). Body `tasks:` is a normal DAG (independent members run parallel within a pass). `{{prev.<member>}}` = prior iteration's output (empty pass 1); exempt from depends_on (listing it would cycle).

```yaml
- id: work
  loop:
    until_empty: drain
    max: 4
    tasks:
      - id: drain
        depends_on: [seed]
        prompt: drain {{seed}} {{prev.refine}}
      - id: refine
        depends_on: [drain]
        prompt: refine {{drain}}
- id: report          # exit consumer
  depends_on: [drain]
  prompt: summarize {{drain}}
```

**`for_each:`** — runs body once per list element, **sequentially**. `in:` = static seq `[a,b]` or a single `{{placeholder}}` (a `{{taskid}}` source adds an entry dep; parsed as JSON array, else newline-split, blanks dropped). `as:` = loop var, referenced `{{as}}`, exempt from depends_on. No `until`/`max`. Empty list = 0 iterations (exit consumer sees empty). `{{prev.<member>}}` works; a member's published output is the **final** iteration's.

**`for_each_parallel:`** — same as `for_each:` but passes run **concurrently** with isolated outputs (one pass can't see another). **No `{{prev}}`** (fails check). Which pass's value an exit consumer reads is unspecified — reference the element, not the member.

## Runtime / model / effort

LLM-only; resolution is task field → workflow default (no default + no task value = validation error). Ignored by command/script tasks (task-level overrides there are hard errors).

- **claude-code** models: `haiku` (mechanical), `sonnet` (spec-following impl), `opus` (architecture, hard debugging, design/review). Efforts: `low` (one-shot) / `medium` (default) / `high` (multi-step) / `max` (hardest synthesis). Override only where escalation matters; wide fan-out of haiku/low costs ~one task's wall time.
- **codex** models: `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini` (reasoning), `gpt-5.3-codex-spark` (non-reasoning). Efforts: `minimal|low|medium|high|xhigh`. Needs `OPENAI_API_KEY`/`CODEX_API_KEY` or `codex login`. No `system_prompt`.

## Output & resume

`loom run` prints plan + per-task progress (id, runtime/model/effort, tokens, cost) + summary, and persists a self-contained JSON record (atomic rewrite per task event, so crashes leave a parseable file):

```
.loom/runs/<wf_id>/<run_id>.json    # run_id = YYYYMMDDTHHMMSSZ-<6hex>, sortable
.loom/runs/<wf_id>/latest.json      # most recent
```

Record holds the manifest, per-task substituted `prompt`, full `output`, `usage` (in/out/cache tokens, cost USD), timing, status, plus totals. Resume (`loom resume <id|latest>`, or `loom run wf.yaml --resume-latest`) seeds `ok` tasks from stored output and re-dispatches the rest; original params are reused.

```bash
jq '.tasks[] | {id, model, usage, elapsed_ms}' .loom/runs/<wf_id>/latest.json
```

## Authoring

1. Sketch the DAG (fan-out / chain / diamond): task ids + edges.
2. Set workflow defaults for the majority; override per task only to escalate.
3. Keep prompts tight — each `{{id}}` injects the entire upstream output.
4. `loom run check` until clean (use `-p k=v` to preview substituted prompts), then `loom run`.

Full detail: `docs/cli.md`, `docs/workflow-spec.md`, `pkg/runtime/{claudecode,codex}/`.
