---
name: go-generator
description: "Generate or design Go code and tests for Loom. Use for Go features, bug fixes, refactors, package/API design, workflow parsing/validation, executor/runner/store/CLI code, and behavior-focused tests. Keep changes aligned with Loom's Makefile gate and package boundaries."
argument-hint: "<feature, task, or code to generate>"
allowed-tools: "read, fffind, ffgrep, edit, write, bash"
---

# Go Generator

Generate Loom-specific Go. Prefer the smallest project-shaped solution over
pattern catalogs, speculative design, or generic Go advice.

## Modes

- **Design**: read relevant code, sketch options for one-way-door decisions,
  then propose packages, types, boundaries, and trade-offs. Do not edit files.
- **Generate**: implement the requested feature, fix, or refactor as the
  thinnest end-to-end slice that satisfies current behavior/tests.
- **Test**: add behavior-focused tests for observable workflow behavior.

For code changes, validate through `go-implement`/`go-lint`. If invoked
directly, run:

```bash
bash .pi/skills/go-lint/run-lints.sh
```

## Hard rules

- Use the Makefile as the source of truth for validation. The default full gate
  is `make lint-test` (`go fmt ./...`, `go vet ./...`, `go test ./...`).
- Keep workflow manifest decoding separate from typed workflow models and
  execution behavior. Raw syntax structs must not leak into runner, store, or
  CLI logic.
- Validate the whole workflow before execution. Never execute a partially valid
  workflow.
- Treat explicit workflow dependency fields as graph edges. Template
  placeholders and runtime outputs are inputs, not implicit dependency edges.
- Keep user-facing diagnostics, serialized output, execution planning, and tests
  deterministic. Sort map-derived output when order is visible.
- Default to package-private identifiers. Export only real package APIs used
  across package boundaries.
- Prefer simple concrete types. Add interfaces only at package boundaries for a
  real alternate implementation, test seam, or external adapter.
- Return errors instead of panicking. Do not add unchecked panics, unbounded
  goroutines, ignored errors, debug prints, sleeps for synchronization, or
  unexplained lint suppressions.

## Loom package map

Discover the exact current boundary before editing, but use these defaults:

- `cmd/loom`: Cobra commands, CLI flag parsing, user-facing reporting, and
  top-level wiring only.
- `pkg/syntax`: external workflow document shape and YAML decoding details.
- `pkg/workflow`: typed workflow model, validation, references, planning,
  retry/budget/condition parsing, and domain errors.
- `pkg/executor`: task execution semantics, compiled programs, loops, shell and
  script execution, cache hooks, and per-task reports.
- `pkg/runner`: orchestration around workflow execution, observers, stores,
  resume/seed behavior, and run finalization.
- `pkg/runtime`: runtime interfaces and runtime provider specs; provider
  packages such as `pkg/runtime/claudecode` and `pkg/runtime/codex` own command
  construction and provider-specific decoding.
- `pkg/store`: persisted runs, state, cache records, run ids, and atomic file
  updates.
- `pkg/registry`: workflow discovery, roots, and extension resolution.
- `pkg/daemon`, `pkg/plan`, `pkg/run`, `pkg/schedule`, and `pkg/tui`: keep
  their focused presentation/planning/scheduling/event responsibilities.
- `internal/...`: implementation details that should not become public package
  APIs.

## Go style

- Keep packages cohesive and acyclic. Move behavior to the package that owns the
  data or invariant rather than adding pass-through helpers.
- Keep `main`/Cobra layers thin. Push testable behavior into packages below
  `cmd/loom`.
- Use `context.Context` for cancellation at I/O, subprocess, runtime, and
  scheduler boundaries; do not store contexts in structs.
- Wrap errors with `%w` at boundaries and include task/path/runtime context.
  Do not log and return the same error.
- Prefer value types for immutable configuration and small domain objects;
  use pointers for shared mutable state, large structs, or optional identity.
- Prefer explicit structs over open-ended `map[string]any` after parsing.
- Avoid package globals except constants and immutable sentinels. Inject clocks,
  runtimes, environment, filesystem paths, and stores where behavior needs a
  seam.
- Use standard library helpers before new dependencies. After dependency
  changes, run `make tidy` and then `go-lint`.
- Keep comments for exported API contracts, invariants, and non-obvious why;
  avoid narrating straightforward implementation.

## Concurrency and processes

- Use `errgroup`, channels, mutexes, or contexts deliberately; document the
  owner of channel close and goroutine lifetimes.
- Never synchronize tests with arbitrary sleeps. Use contexts, channels,
  fakes, or deterministic clocks.
- Always wait for subprocesses and include stderr/stdout context in Loom-shaped
  errors where useful. Never leak secrets in logs or errors.
- When changing scheduling, daemon, runtime, or shared-state code, run
  `make test-race` after `go-lint` is clean.

## Tests

- Test observable behavior through public or package-level boundaries, not
  private implementation details.
- Put tests where complexity lives: focused package tests for domain logic;
  command/runner integration tests for wiring and user-visible behavior.
- Use Arrange-Act-Assert with one action per test. Table tests are welcome when
  each case is easy to read and failures name the scenario.
- Prefer output/state assertions over interaction assertions. Mock only external
  systems or adapter edges; use fakes for Loom-owned dependencies.
- Use `t.Helper`, `t.TempDir`, `t.Setenv`, and context timeouts for clean tests.
- Keep manifests/snippets minimal but readable. Add tests for validation errors,
  deterministic ordering, and edge cases introduced by the change.
- Do not test Go syntax, struct tags, third-party behavior, or duplicate the
  same scenario through multiple layers.
