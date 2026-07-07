---
name: go-lint
description: "Lint and test Go code with this project's canonical Makefile gate: make lint-test (go fmt, go vet, go test ./...). Use proactively after Go changes and when asked to lint, vet, format, test, or check the Go project."
argument-hint: "<optional module, package, or file path>"
allowed-tools: "bash"
---

# Go Lint

Run the project-local lint runner instead of hand-typing Go commands:

```bash
bash .pi/skills/go-lint/run-lints.sh [optional-path]
```

The runner finds the Go module root and executes the Loom gate from `AGENTS.md`:

1. `make lint-test`
   - `make lint`
   - `make fmt` (`go fmt ./...`)
   - `make vet` (`go vet ./...`)
   - `make test` (`go test ./...`)

`make fmt` can update files. If it does, inspect the diff before reporting.

## Extra checks

When touching goroutines, timers, scheduler/daemon code, cancellation, shared
state, process execution, or runtime concurrency, run this after the lint runner
is clean:

```bash
make test-race
```

Run `make tidy` before go-lint when dependencies changed.

## Reporting

- If clean, report exactly: `All lints clean.`
- If it fails, summarize one line per actionable finding and keep the command
  output available for the next fix round.
- Missing Go, Make, or required Makefile targets are failures, not a clean lint
  pass.
