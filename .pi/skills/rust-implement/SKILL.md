---
name: rust-implement
description: "Implement Rust code by using rust-generator and an iterative rust-lint loop only. This skill should be used proactively for Rust features, bug fixes, tests, and refactors that require code changes."
argument-hint: "<feature, bugfix, refactor, or test request>"
allowed-tools: "read, fffind, ffgrep, edit, write, bash, Agent"
---

# Rust Implement

Use the smallest useful loop:

```text
rust-generator -> rust-lint -> rust-generator fixes -> rust-lint ...
```

Do not use rust-evaluator, rust-docs, rust-researcher, setup-rust-project, or
raw cargo commands for this workflow.

## Hard rules

- Use `rust-generator` for all Rust code/test generation and lint fixes.
- Use `rust-lint` for validation.
- Never run cargo commands directly; run the rust-lint runner instead.
- Stop after 3 lint/fix rounds unless the user explicitly asks to continue.
- Treat missing lint tools as blockers, not as a clean pass.

## Workflow

1. **Load generator guidance**
   - Read `.pi/skills/rust-generator/SKILL.md` if its guidance is not already
     loaded.
   - Follow only the references that are relevant to the requested change.

2. **Implement or generate tests**
   - Use the rust-generator guidance to make the requested Rust changes.
   - Prefer direct edits in the current agent for small changes.
   - If delegating to a pi subagent, delegate only to a Rust generator context
     and explicitly tell it to use `.pi/skills/rust-generator/SKILL.md`.

3. **Run lint**
   - Run:

     ```bash
     bash .pi/skills/rust-lint/run-lints.sh
     ```

4. **Fix iteratively**
   - If lint is clean, finish.
   - If lint fails, summarize the actionable failures and apply the fix using
     rust-generator guidance.
   - Re-run rust-lint after each fix round.
   - Repeat for at most 3 rounds.

5. **Report**
   - Files changed.
   - What was implemented.
   - Final lint result, or the remaining blocker after 3 rounds.
