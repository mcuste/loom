# Loom

## Build, lint, test

Use the Makefile targets; they are the canonical commands. Run from
the repo root. After any code change, run `make lint-test` and make
sure it is clean before committing.

- **Lint**: `make lint` (runs `fmt` + `vet`: `go fmt ./...` then
  `go vet ./...`). There is no golangci-lint config; `go vet` is the
  lint gate.
- **Lint + test**: `make lint-test` (runs `lint` then `test`). This is
  the default pre-commit gate.
- **Format only**: `make fmt` (`go fmt ./...`).
- **Vet only**: `make vet` (`go vet ./...`).
- **Test**: `make test` (runs `go test ./...`). Use
  `make test-race` (`go test -race ./...`) when touching concurrency.
- **Build/install**: `make build` (binary `./loom`) or `make install`
  (`go install ./cmd/loom`).
- **Tidy modules**: `make tidy` after changing dependencies.
- **Validate workflows**: `make check WORKFLOW=path/to.yaml` for one,
  or `make check-all` for every YAML under `workflows/`.

## Commit convention

Subsystem-prefixed, imperative subject. Format:

```
[area] subject

[optional body, wrapped at 72]

[optional footer: BREAKING CHANGE: ... / Refs: ...]
```

Rules:

- **Area** (required): bracketed package or surface name, e.g.
  `[workflow]`, `[executor]`, `[store]`, `[runtime]`, `[cli]`,
  `[skill]`, `[examples]`, `[docs]`, `[build]`.
- **Multi-area**: stack brackets in dependency order (lowest layer
  first), e.g. `[workflow][executor] ...`. Cap at ~3 brackets. If you
  need more, the commit is too big; split it.
- **Subject**: imperative mood, lowercase, no trailing period, ≤ 50
  chars *after* the bracket prefix. Capitalize only proper Names
  (e.g. `Cobra`, `YAML`, `Go`, `claude-code`).
- **Body**: only when the *why* is non-obvious. Prefer a bullet list
  of short, concrete points (`-` or `*`); reach for paragraphs only
  when a single point needs more than a line or two to explain. Do
  not use `—` (em-dash) or `–` (en-dash).
- **No AI annotations**: do not add `Co-Authored-By: Claude`,
  `Generated with`, or any tool/agent footer. Commits are authored
  by the human; the tooling stays invisible.
- **Breaking changes**: prefix the subject with `!`, e.g.
  `[workflow] !rename TaskID to NodeID`, and add a `BREAKING CHANGE:`
  footer.
- **Mixed changes**: prefer splitting into separate commits. If a
  single commit genuinely spans multiple subsystems (a feature that
  needs touches in `workflow`, `executor`, and `cli` to land), list
  all areas in brackets and write the subject from the user's POV
  (what the *feature* does, not what each file changes).

Examples:

```
[cli] add -p key=val for workflow params
[workflow] reject param names that collide with task ids
[executor] pass Options instead of variadics
[workflow][executor][store][cli] add CLI workflow params
[skill] document params block
[examples] add deploy.yaml using new params syntax
```

## Deriving areas

Don't memorize a list. Discover it. For a given commit:

1. **Run `git log --oneline -50`** and scan the bracket prefixes
   already in use. Reuse an existing name if one fits.
2. **Otherwise derive from the changed paths**:
   * `pkg/<name>/...` → `[<name>]` (e.g. `pkg/workflow/` → `[workflow]`).
   * `cmd/<name>/...` → `[cli]` (or `[<name>]` if more than one CLI exists).
   * Nested packages collapse to the parent unless the sub-package is
     itself the unit of change, e.g. `pkg/runtime/claudecode/` →
     `[runtime/claude-code]`.
   * Top-level dirs map to their name (`workflows/` → `[workflows]`,
     `.claude/skills/loom/` → `[skill]`).
   * Build/config files (`go.mod`, `Makefile`, `.gitignore`) → `[build]`.
3. **When unsure**, pick the lowest-level identifier that uniquely
   names the changed surface. Coining a new area is fine; once it
   lands in history, step 1 will surface it for the next commit.
