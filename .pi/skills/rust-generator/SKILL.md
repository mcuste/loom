---
name: rust-generator
description: "Generate or design Rust code and tests for Loom. Use for Rust features, bug fixes, refactors, module/type design, manifest parsing/validation, runner/store/CLI code, and behavior-focused tests. Keep changes aligned with Loom's workspace lints and format-neutral workflow model."
argument-hint: "<feature, task, or code to generate>"
allowed-tools: "read, fffind, ffgrep, edit, write, bash"
---

# Rust Generator

Generate Loom-specific Rust. Prefer the smallest project-shaped solution over
pattern catalogs, speculative design, or generic Rust advice.

## Modes

- **Design**: read relevant code, sketch options for one-way-door decisions,
  then propose types, modules, boundaries, and trade-offs. Do not edit files.
- **Generate**: implement the requested feature, fix, or refactor as the
  thinnest end-to-end slice that satisfies current behavior/tests.
- **Test**: add behavior-focused tests for observable workflow behavior.

For code changes, validate through `rust-implement`/`rust-lint`. If invoked
directly, run:

```bash
bash .pi/skills/rust-lint/run-lints.sh
```

## Hard rules

- Keep workflow manifests format-neutral. Do not bake YAML into core names,
  diagnostics, or tests unless behavior is YAML-specific.
- Keep raw serde DTOs separate from typed domain models and validated execution
  plans. Raw manifest structs must not leak into runner, store, or CLI behavior.
- Validate the whole workflow before execution. Never execute a partially valid
  workflow.
- Treat `depends_on` as the only graph-edge source. Template placeholders are
  inputs, not dependencies.
- Keep user-facing diagnostics, serialized output, execution planning, and tests
  deterministic.
- Default to private visibility, then `pub(crate)`, then `pub` only for real
  crate APIs.
- Prefer simple concrete types. Add traits, builders, generics, crates,
  hexagonal adapters, or abstraction layers only for a real caller or swap need.
- Use typed domain primitives and enums for fixed sets. Use `Path`/`PathBuf` for
  paths, not string concatenation or open-ended strings.
- Do not add `unwrap`, `expect`, explicit `panic!`, unchecked indexing/slicing,
  unchecked numeric casts, `dbg!`, `todo!`, or unreasoned `allow` attributes.

## Design principles

- Ship vertical slices, not layers. Let design emerge; use Rule of Three before
  unifying coincidental duplication.
- Separate structural tidying from behavior changes. Tidy first only when it
  directly eases the imminent change.
- Prefer deep modules with simple interfaces over shallow pass-through layers.
  Avoid information leakage and hidden ordering between modules.
- Keep a functional core and imperative shell: pure calculations and inert data
  in the core; I/O, clocks, persistence, network, randomness, and telemetry at
  edges.
- Make illegal states unrepresentable with domain ids, explicit optionality,
  error values, and state machines instead of scattered booleans.
- Reuse existing vocabulary. Names should be precise and consistent; comment the
  why, units, and invariants, not the how.
- Keep knowledge DRY in production code. Keep tests DAMP and readable as a
  complete scenario.

## Loom model

- Pipeline: `raw manifest DTOs -> typed workflow spec -> validated graph/plan`.
- Raw DTOs mirror external shape: strings, optional fields, serde renames, and
  loosely typed values are acceptable there only.
- Typed models encode invariants: ids, references, graph edges, conditions,
  retries, budgets, loops, outputs, and routing choices.
- Isolate format adapters from core validation/execution so another
  serde-compatible format does not reshape the domain model.
- Keep crate responsibilities narrow:
  - `loom-core`: manifest schema, parsing, validation, domain types.
  - `loom-runner`: execution planning, scheduling, runtime integration.
  - `loom-store`: persisted state, cache, remembered outputs.
  - `loom-cli`: argument parsing, config loading, user reporting, wiring.

## Rust style

- Keep `lib.rs` to module declarations and re-exports; put implementation in
  sibling modules. Prefer `module.rs` plus `module/`, not new `mod.rs` files.
- Prefer standard conversions/parsing when they clarify call sites: `From`,
  `TryFrom`, `FromStr`, `AsRef`, `Display`.
- Use named structs instead of tuples for domain values or 3+ fields.
- Use deterministic collections (`BTreeMap`, sorted `Vec`) where order is
  visible.
- Follow formatter and workspace lints; fix issues at the source instead of
  silencing them.

## Errors, effects, and observability

- Define invalid states out of existence when practical. Otherwise model errors
  as values with small `thiserror` enums scoped to the API surface.
- Wrap serde, IO, runtime, subprocess, and persistence failures in Loom terms
  with field/task/path context. Do not expose internals as core validation
  diagnostics.
- Error display text should be user-facing, lowercase when natural, and without
  trailing punctuation.
- Do not log and propagate the same error. Log only where handled.
- Crash only for rare unrecoverable cases or impossible invariants.
- Instrument as behavior is added. Prefer structured, vendor-neutral telemetry;
  never log secrets or PII.

## Tests

- Test observable behavior through public or module boundaries, not private
  state.
- Put tests where complexity lives: domain core with focused unit tests;
  orchestration with integration tests for happy path and edges.
- Use one scenario per test with Arrange-Act-Assert and one action.
- Prefer output-based assertions. Mock only external systems at adapter edges;
  use real instances for dependencies owned by Loom.
- Parser/validator tests should use the smallest readable manifest snippet. Add
  JSON coverage for format-neutral behavior when the adapter exists or is being
  added.
- Do not test Rust derives, serde itself, third-party behavior, or duplicate the
  same scenario. Do not reimplement production logic in assertions or add
  production code only for tests.

## Read references only when relevant

Files are relative to this skill directory.

- serde and manifest DTO details: `references/crate-serde.md`
- validation/domain errors: `references/error-handling.md`
- API boundaries and conversion choices: `references/api-design.md`
- module/crate layout or state-machine design: `references/architecture-design.md`
- test strategy: `references/testing.md`
- async runner work: `references/concurrency-async.md` and
  `references/concurrency-advanced.md`
- CLI behavior: `references/domain-cli.md`

Ignore unrelated generic references unless the task explicitly enters that
area.
