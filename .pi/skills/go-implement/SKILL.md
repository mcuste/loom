---
name: go-implement
description: "Implement Go code by using go-generator and an iterative go-lint loop only. This skill should be used proactively for Go features, bug fixes, tests, and refactors that require code changes."
argument-hint: "<feature, bugfix, refactor, or test request>"
allowed-tools: "read, fffind, ffgrep, edit, write, bash, Agent"
---

# Go Implement

Use the smallest useful loop:

```text
go-generator -> go-lint -> go-generator fixes -> go-lint ...
```

Do not bypass the project Makefile gate, and do not substitute ad hoc `go`
commands for validation in this workflow.

## Hard rules

- Use `go-generator` for all Go code/test generation and lint fixes.
- Use `go-lint` for validation.
- Never run raw `go fmt`, `go vet`, or `go test` as the final validation; run
  the go-lint runner instead.
- Stop after 3 lint/fix rounds unless the user explicitly asks to continue.
- Treat missing Go, Make, or Makefile targets as blockers, not as a clean pass.

## Workflow

1. **Load generator guidance**
   - Read `.pi/skills/go-generator/SKILL.md` if its guidance is not already
     loaded.
   - Follow only the sections that are relevant to the requested change.

2. **Implement or generate tests**
   - Use the go-generator guidance to make the requested Go changes.
   - Prefer direct edits in the current agent for small changes.
   - If delegating to a pi subagent, delegate only to a Go generator context
     and explicitly tell it to use `.pi/skills/go-generator/SKILL.md`.

3. **Run lint**
   - Run:

     ```bash
     bash .pi/skills/go-lint/run-lints.sh
     ```

   - If the change touches goroutines, timers, scheduling, cancellation,
     shared state, or process/runtime concurrency, also run `make test-race`
     from the repo root after `go-lint` is clean.

4. **Fix iteratively**
   - If lint is clean, finish.
   - If lint fails, summarize the actionable failures and apply the fix using
     go-generator guidance.
   - Re-run go-lint after each fix round.
   - Repeat for at most 3 rounds.

5. **Report**
   - Files changed.
   - What was implemented.
   - Final lint result, and race-test result when applicable.
   - Any remaining blocker after 3 rounds.
