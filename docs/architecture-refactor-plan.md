# Architecture refactor plan

This plan intentionally allows breaking changes. The goal is to make Loom read as
two separate products:

1. **Interpreter**: parse, validate, compile, and run workflow programs against
   pluggable runtimes.
2. **Scheduler**: own cron/one-shot schedules and a daemon that launches
   interpreter runs.

The scheduler must not know about workflow internals such as tasks, DAGs,
runtimes, gates, reports, or loop execution. It should only know that a schedule
fires a workflow invocation.

## Design principles

- Keep the model small and explicit. Prefer a few cohesive Go packages over many
  tiny files and abstractions.
- Condense while refactoring. Remove obsolete compatibility layers, unused
  helpers, dead DTO fields, old tests, and transitional types as soon as their
  replacement lands.
- Do not grow a parallel architecture beside the old one indefinitely. Each phase
  should migrate callers and delete the superseded code in the same change or in
  the next small cleanup change.
- Use idiomatic Go: simple structs, interfaces at package boundaries, concrete
  types inside packages, table-driven tests, clear package names, and no generic
  framework-style layering unless it pays for itself.
- Keep entrypoints thin. `cmd/loom` should wire use cases and adapters, not hold
  domain rules.
- Make dependencies point inward to stable contracts. Runtimes, stores,
  renderers, reporters, and schedulers are adapters around the interpreter, not
  concepts embedded in workflow definitions.

## Target bounded contexts

```text
Scheduler
  Schedule -> WorkflowInvocation -> RunLauncher

Interpreter
  WorkflowDefinition -> Program -> Run
                         |        |
                         |        +-> Events -> renderer/reporter/store/gates
                         +-> RuntimeProvider
```

Dependency rule:

```text
scheduler uses interpreter application ports
interpreter must not import scheduler
```

Inline `schedule:` blocks, if kept, should be syntax metadata consumed by a
`schedule sync` use case. They should not be part of the interpreter's core
workflow domain.

## Interpreter model

### WorkflowDefinition

Validated immutable workflow definition.

```go
type WorkflowDefinition struct {
    ID          WorkflowID
    Description string
    Defaults    WorkflowDefaults
    Params      []ParamSpec
    Nodes       []WorkflowNode
    Output      OutputSelector
    Policies    WorkflowPolicies
}
```

Owns:

- `ParamSpec[]`
- `WorkflowNode[]`
- `WorkflowDefaults`
- `WorkflowPolicies`
- `OutputSelector`

### WorkflowNode

Use nodes for workflow structure instead of treating every concept as a task.

```text
WorkflowNode
  - TaskNode
  - LoopNode
```

A `TaskNode` is executable:

```go
type TaskNode struct {
    ID          TaskID
    Description string
    DependsOn   []NodeID
    Action      Action
    Condition   *ConditionExpr
    Runtime     RuntimeSelector
    Policies    TaskPolicies
}
```

A `LoopNode` is structural:

```go
type LoopNode struct {
    ID          LoopID
    Description string
    Spec        LoopSpec
    Body        WorkflowFragment
}
```

### Action

Replace optional body fields with one explicit action value.

```text
Action
  - PromptAction
  - CommandAction
  - ScriptAction
  - SubWorkflowAction
```

```go
type PromptAction struct {
    Prompt Template
}

type CommandAction struct {
    Command Template
}

type ScriptAction struct {
    Path Template
    Args []Template
}

type SubWorkflowAction struct {
    Ref  WorkflowRef
    With map[ParamName]Template
}
```

### Policies and value objects

Keep these as small value objects:

- `Template`
- `ConditionExpr`
- `RuntimeSelector`
- `RetryPolicy`
- `BudgetPolicy`
- `CachePolicy`
- `OutputSchema`
- `WorkingDirectory`
- `ParamValues`
- `WorkflowRef`

### Program

The engine should interpret a compiled IR, not raw YAML/domain structs.

```go
type Program struct {
    WorkflowID WorkflowID
    Graph      WorkflowGraph
    Units      []ExecutableUnit
}
```

```text
ExecutableUnit
  - TaskUnit
  - LoopUnit
  - SubWorkflowUnit
```

Compilation responsibilities:

1. Parse YAML into syntax DTOs.
2. Convert DTOs into `WorkflowDefinition`.
3. Validate identifiers, params, dependency graph, placeholders, loop scope,
   runtime selectors, output selection, and policy compatibility.
4. Compile `WorkflowDefinition` into `Program`.

## Run model

`WorkflowDefinition` and `Program` are immutable. `Run` is execution state.

```go
type Run struct {
    ID         RunID
    WorkflowID WorkflowID
    Invocation Invocation
    Status     RunStatus
    Steps      []StepExecution
    Usage      UsageLedger
    StartedAt  time.Time
    FinishedAt time.Time
    Provenance Provenance
}
```

```go
type StepExecution struct {
    ID       StepInstanceID
    TaskID   TaskID
    Status   StepStatus
    Input    StepInput
    Output   StepOutput
    ExitCode int
    Attempts []Attempt
    Usage    Usage
}
```

Retries should be represented as attempts rather than hidden inside one terminal
result.

## Runtime model

Runtimes are providers behind a catalog.

```go
type RuntimeProvider interface {
    Name() RuntimeName
    Validate(RuntimeRequest) error
    Run(context.Context, RuntimeRequest) (RuntimeResponse, error)
}
```

The interpreter depends on this contract, not concrete runtime implementations.
Concrete runtimes live as adapters.

## Events, rendering, reporting, and gates

Use execution events as the extension seam.

```text
RunStarted
StepReady
GateEvaluated
StepStarted
StepFinished
StepSkipped
UsageAccrued
RunFinished
```

Consumers:

- live renderer: TUI/plain progress
- reporter: markdown/json/html/junit summaries
- run recorder: persisted run records
- metrics sink: future telemetry

Gates are policy extensions evaluated by the interpreter.

```go
type Gate interface {
    Evaluate(context.Context, GateContext) GateDecision
}
```

Gate points:

- pre-run
- pre-step
- post-step
- post-run

Built-in gates:

- budget gate
- `when` condition gate
- schema gate
- future approval/policy gates

## Scheduler model

### Schedule

```go
type Schedule struct {
    ID        ScheduleID
    Target    WorkflowInvocation
    Trigger   Trigger
    Enabled   bool
    Overlap   OverlapPolicy
    Catchup   CatchupPolicy
    NextFire  time.Time
    LastFire  *time.Time
    LastRunID *RunID
    CreatedAt time.Time
}
```

### Trigger

```text
Trigger
  - CronTrigger
  - OneShotTrigger
```

```go
type CronTrigger struct {
    Expr string
    TZ   string
}

type OneShotTrigger struct {
    At time.Time
}
```

### WorkflowInvocation

Opaque interpreter request stored by the scheduler.

```go
type WorkflowInvocation struct {
    Ref    WorkflowRef
    Params map[string]string
    Cwd    string
}
```

### Daemon

Daemon responsibilities:

1. Load enabled schedules.
2. Compute due fires.
3. Apply catchup policy.
4. Apply overlap policy.
5. Launch runs through a small interpreter port.
6. Persist `NextFire`, `LastFire`, and `LastRunID`.

Port into the interpreter/application layer:

```go
type RunLauncher interface {
    Launch(context.Context, WorkflowInvocation, Provenance) (RunID, error)
}
```

## Suggested package direction

Do not treat this as a mandate to create every directory immediately. Prefer
moving code only when it removes confusion.

```text
pkg/interpreter/       workflow domain, compiler, engine, events, gates
pkg/runtime/           runtime contracts and catalog
pkg/scheduler/         schedule domain, store, daemon, sync use cases
pkg/store/             run/state persistence, if still cohesive after cleanup
pkg/tui/               TUI/plain renderers, or move under adapters if clearer
cmd/loom/              CLI wiring only
```

Avoid deep trees like `domain/application/adapters/ports` unless the package has
enough code to justify it. Idiomatic Go package names should describe what the
package does, not which architecture diagram box it came from.

## Migration sequence

1. **Document boundaries**
   - Add this plan.
   - Agree that interpreter and scheduler are separate bounded contexts.

2. **Introduce explicit actions**
   - Make task body handling use one `Action` value.
   - Migrate callers away from optional fields such as prompt/command/script as
     decision points.
   - Delete old body-kind compatibility code once callers move.

3. **Separate syntax from domain**
   - Keep YAML-only structs in syntax/parsing code.
   - Convert parsed DTOs into `WorkflowDefinition`.
   - Delete parser artifacts that leak into execution.

4. **Compile to Program**
   - Move DAG ordering, loop expansion/metadata, and executable-unit selection
     into a compiler.
   - Make the interpreter consume `Program`.
   - Remove duplicated graph/scheduling helpers.

5. **Make events the presentation seam**
   - Replace ad-hoc hooks with an event stream.
   - Adapt store, TUI/plain rendering, and summaries to consume events.
   - Delete bridge types after migration.

6. **Extract gates as policies**
   - Move budget, `when`, schema, and future approval checks behind gate points.
   - Keep simple built-ins close to the interpreter until they need their own
     package.

7. **Isolate scheduler**
   - Move schedule-specific state and daemon behavior out of workflow domain.
   - Make daemon call a `RunLauncher` port.
   - Keep schedule storage and next-fire computation in scheduler packages.

8. **Cleanup pass after each phase**
   - Run `go test ./...` or `make lint-test`.
   - Remove unused files, old types, obsolete tests, and redundant wrappers.
   - Prefer merging tiny files into cohesive package files over spreading small
     structs across many files.

## Refactor checklist

Before merging each phase:

- Does this reduce or increase the number of concepts a reader must hold?
- Did we delete the old path, or did we just add a second one?
- Are package names short and domain-specific?
- Are interfaces only at seams used by another package or adapter?
- Are CLI files only wiring commands and use cases?
- Can tests describe behavior without coupling to internal choreography?
- Did `make lint-test` pass?
