# Loom CLI

```bash
loom run    <workflow> [-p key=val ...] [--resume-latest]        # validate, then execute
loom run check <workflow> [-p key=val ...]                       # validate + print plan, no execution
loom resume <run-id> [-p key=val ...]                            # resume a past run
loom runs   [workflow]                                           # browse past runs (TUI / table)
loom runs ls   [workflow] [-n N]                                 # list past runs as a table
loom runs show <run-id> [--task ID] [--summary]                  # print a stored run inline
loom workflows ls                                                # list registry workflows by name
```

Exit code is `0` on success, `1` on any validation or execution error.

---

## Registry — run by name

A workflow stored in a registry root can be run by name instead of by path:

```bash
loom run deploy                   # resolves deploy.yaml from the nearest registry
loom run deploy:prod              # resolves deploy/prod.yaml
loom run check deploy             # validate only, no execution
loom workflows ls                 # list every registered workflow with its description
```

**Classification rule** (syntactic, cwd-independent — an argument with `/` or `\` is always a path):

| Argument form | Treated as |
|---|---|
| contains `/` or `\` | filesystem path (returned verbatim) |
| contains `:` (no separator) | registry name; `:` is the hierarchy separator |
| ends `.yaml` or `.yml` (no separator) | filesystem path |
| anything else | flat registry name |

A Windows drive path such as `C:\wf.yaml` contains `\` and is therefore a filesystem path despite its `:`.

**Registry search order** — loom searches roots in this order and uses the first match (nearest wins, shadowing later roots):

1. `.loom/workflows/` in the current working directory.
2. `.loom/workflows/` in each ancestor directory, walking up to and including the git repo root (the first ancestor containing a `.git` entry). The walk stops at the git root, so registries above the repo are never searched.
3. `$LOOM_HOME/workflows/` — the global registry, searched last.

`loom workflows ls` merges all roots under the same shadowing rule, so the listing reflects exactly what `loom run` would resolve.

Registry layout (example with both a project-local and a global registry):

```text
<repo-root>/.loom/workflows/
├── deploy.yaml          → name: deploy  (shadows any global deploy)
├── ci/
│   └── test.yaml        → name: ci:test
└── release/
    ├── release.yaml     → name: release        (eponymous-dir form)
    └── prompts/
        └── notes.md     → prompt_file: prompts/notes.md (beside the workflow)

$LOOM_HOME/workflows/
├── deploy.yaml          → name: deploy  (shadowed by local above)
├── deploy/
│   └── prod.yaml        → name: deploy:prod
└── infra/
    └── setup.yaml       → name: infra:setup
```

A `.yaml` and a `.yml` file with the same stem map to the same name; `.yaml` takes precedence.

**Eponymous-dir form** — when a file's name equals its parent directory (`X/X.yaml`),
the redundant trailing segment is collapsed, so `release/release.yaml` is named
`release`, not `release:release`. This lets a workflow live in its own directory
beside the `prompt_file:` text it references (`prompt_file:` resolves relative to
the workflow's own directory). Nested non-matching files are unaffected
(`ci/test.yaml` stays `ci:test`). If both a flat file `X.yaml` and the dir form
`X/X.yaml` exist in one root, the flat file wins.

**Sub-workflow refs** — a task's `workflow:` ref resolves through these exact
same registry rules: a name (e.g. `release`, `ci:test`) resolves against the
local and global roots, and a path ref (e.g. `./sub.yaml`) resolves relative to
the linking workflow's own directory. See
[Sub-workflows](workflow-spec.md#sub-workflows).

### Shell completion

The workflow argument of `loom run` / `loom run check` tab-completes against the
registry, so `loom run tui<TAB>` expands to `tui_demo` (nested workflows
complete with their `:` name). Path-mode arguments still fall back to normal
file completion. Resolution itself is exact-name only — completion is what turns
a typed prefix into a full name; no prefix matching happens at run time.

Install the completion script once per shell (cobra generates it):

```bash
loom completion fish > ~/.config/fish/completions/loom.fish        # fish
loom completion zsh  > "${fpath[1]}/_loom"                          # zsh
source <(loom completion bash)                                     # bash (current shell)
```

---

## `loom run check <workflow>`

Parse and validate the manifest, then print the execution plan — **without
dispatching a single model call**. This is the authoring workhorse: it surfaces
every structural error (cycles, unknown deps, unknown placeholders, bad
model/effort, malformed loop/param/budget/schema blocks) for free.

```bash
loom run check workflows/deploy.yaml
loom run check workflows/deploy.yaml -p env=prod    # resolves {{params.X}} in the printed plan
```

With params declared, passing `-p` resolves placeholders so the printed plan
shows the actual prompts the model will receive. A missing **required** param is
downgraded to a warning here (so `check` doubles as a "what params does this
workflow need?" probe), whereas `loom run` treats it as a hard failure.

---

## `loom run <workflow>`

Runs the same validation/plan phase as `check`, and only if it passes, executes
every task. Independent tasks (no edge between them) are dispatched
concurrently — fan-out costs roughly one task's wall-clock time, not N.

```bash
loom run workflows/go_implement.yaml -p task="add a Bloom filter to pkg/cache"
```

Stdout carries the plan, per-task progress (id, runtime/model/effort, tokens,
cost), the run-file path, and a final summary (total tokens, cost, completion).
Task **outputs** do not leak to stdout beyond the summary; they live in memory
for `{{id}}` substitution and on disk in the run record.

- `-p`, `--param key=val`: set a workflow parameter (repeatable).
- `--resume-latest`: skip the tasks that already completed in this workflow's
  most recent run and re-run the rest (see [Resume](#resuming-a-run)).

---

## Run records

Every `loom run` persists a self-contained JSON record as it executes:

```bash
$LOOM_HOME/runs/<workflow_id>/<run_id>.json     # full record, rewritten per task event
$LOOM_HOME/runs/<workflow_id>/latest.json       # symlink to the most recent run
```

`<run_id>` is `YYYYMMDDTHHMMSSZ-<6 hex>` (UTC, lexicographically sortable). The
file is rewritten atomically (write `.tmp`, rename) on every task start/finish,
so a crash mid-run still leaves a parseable file.

The record embeds:

- the **verbatim manifest** (byte-identical to what you ran, with any
  `prompt_file:` references already inlined as literal `prompt:` text — the
  record is self-contained even when the external file is later changed or
  deleted);
- the resolved **params** and the **cwd** the run was invoked from;
- a top-level **usage** total (`input_tokens`, `output_tokens`,
  `cache_read_tokens`, `total_cost_usd`), `task_count`, `status`, and timing;
- a **per-task array**: id (and 1-based `iteration` for looped tasks),
  runtime/model/effort, the substituted `prompt` or `command`, the full
  `output`, per-task `usage`, `elapsed_ms`, `status`, and any `error`.

Task `status` is one of `started`, `ok`, `failed` (a `when`-skipped task is
recorded distinctly so it is never re-dispatched on resume).

Inspect a record with `jq`:

```bash
jq '.usage, .task_count' "$LOOM_HOME/runs/<wf>/latest.json"
jq '.tasks[] | {id, model, effort, usage, elapsed_ms}' "$LOOM_HOME/runs/<wf>/latest.json"
jq -r '.tasks[] | select(.id=="report") | .output' "$LOOM_HOME/runs/<wf>/latest.json"
```

---

## Resuming a run

A failed or interrupted run can be resumed: loom seeds every task whose stored
`status` is `ok` with its recorded output and only dispatches the remainder. The
original params are reused (no `-p` needed, though you may override). If the
record carries the directory the original run was launched from, loom `chdir`s
into it first so shell tasks and relative paths resolve correctly.

```bash
loom resume latest                       # most recent run across all workflows
loom resume 20260624T101500Z-0afad3      # a specific run by full id
loom resume 0afad3                        # ... or by its short hex suffix
loom run workflows/deploy.yaml --resume-latest   # same, keyed off the YAML path
```

A run id may be the full id, the short suffix shown by `loom runs ls`, or a
leading timestamp prefix; an exact full-id match always wins, and an ambiguous
fragment is reported with the candidates. `latest` follows the
most-recently-updated `latest.json` symlink. `--resume-latest` reads the
workflow **body** from the YAML on disk (so edits to not-yet-run tasks take
effect) and takes only the seeded outputs and params from the record; tasks
present in the record but no longer in the workflow are dropped from the seed.

---

## `loom runs` — browse past runs

```bash
loom runs                 # interactive TUI browser (falls back to a table when piped)
loom runs deploy          # filter to one workflow
loom runs --plain         # force the plain table
loom runs -n 20           # at most the 20 most-recent runs
```

`loom runs` opens an interactive browser on a real terminal and prints a plain
table when output is piped, `--plain` is set, or there are no runs yet.

### `loom runs ls [workflow]`

Never opens the browser; always prints the table. `-n`/`--limit N` keeps the N
most-recent runs (the index is newest-first). Alias: `loom runs list`.

### `loom runs show <run-id>`

Prints a stored run inline. By default: a header, a per-task summary table, and
each task's dependencies, prompt/command, output, and error.

- `-s`, `--summary`: print only the header and the per-task summary table
  (omit prompts and outputs).
- `-t`, `--task ID`: print only one task's prompt and output.

The run id accepts the same forms as `loom resume` (full id, short suffix,
timestamp prefix, or `latest`).

---

## `LOOM_HOME` — the data directory

All run records and cross-run state live under loom's home directory:

- `$LOOM_HOME` when set;
- otherwise `$HOME/.loom`.

A relative `LOOM_HOME` is resolved to an absolute path before use, so a `run`
followed by a `resume` (which may `chdir`) always reach the same store. Layout:

```text
$LOOM_HOME/
├── runs/<workflow_id>/<run_id>.json   per-run records
├── runs/<workflow_id>/latest.json     symlink to the newest run
├── state/<workflow_id>.json           cross-run state (writes_state / {{state.x}})
└── workflows/<name>.yaml             registry — workflows runnable by name
```

> Note: the embeddable library layer defaults its root to a local `.loom`
> directory, but the `loom` CLI always uses `LOOM_HOME` (default `$HOME/.loom`)
> as described above.
