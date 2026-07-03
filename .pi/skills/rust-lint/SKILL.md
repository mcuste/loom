---
name: rust-lint
description: "Lint Rust code with this project's full gate: cargo fmt --all --check, clippy, cargo machete, cargo deny, and cargo test. This skill should be used proactively after Rust changes and when asked to lint, run clippy, format-check, test, audit dependencies, or check the Rust project."
argument-hint: "<optional workspace or crate path>"
allowed-tools: "bash"
---

# Rust Lint

Run the project-local lint runner instead of hand-typing cargo commands:

```bash
bash .pi/skills/rust-lint/run-lints.sh [optional-path]
```

The runner finds the Cargo workspace root and executes the Loom gate from
`AGENTS.md`:

1. `cargo fmt --all --check`
2. `cargo clippy --workspace --all-targets --all-features -- -D warnings`
3. `cargo machete`
4. `cargo deny check`
5. `cargo test --workspace --all-features`

## Reporting

- If clean, report exactly: `All lints clean.`
- If it fails, summarize one line per actionable finding and keep the command
  output available for the next fix round.
- Missing required tools are failures, not a clean lint pass.
