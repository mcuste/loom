# Launcher

`launcher` turns a workflow reference into a run. Both `loom run` and the
daemon use it to load a workflow, check its parameters, and build a
`runner.Request`.

```text
loom run                         loom daemon
   |                                  |
   v                                  v
launcher.Launcher.Prepare      daemon finds a due schedule
   |                                  |
   |                                  v
   |                            launcher.RunLauncher.Launch
   |                                  |
   +---------------+------------------+
                   v
          launcher.Launcher
          - find the workflow file or registry entry
          - load the workflow
          - check parameters and runtime routing
          - build runner.Request
                   |
        +----------+-----------+
        |                      |
        v                      v
CLI prints the plan       daemon opens a log file
and runs the request      and records why it ran
        |                      |
        +----------+-----------+
                   v
              runner.Run
                   |
                   v
          executor runs workflow tasks
```

## Why this package exists

The daemon only needs to say “run this workflow with these parameters.” It
does not need to know how workflows are loaded or checked. `RunLauncher` is
the small interface that keeps that boundary clear and makes daemon tests simple.

The CLI calls `Prepare` directly so it can print the plan before starting the
run. Scheduled runs call `Launch`, which prepares the request, writes output to
one log file per scheduled run, and passes schedule details to the runner.

## Run output

`runner.RunOutput` is the presentation seam for a run's header, task events,
summary, and store errors. `Launcher.NewOutput` is a `RunOutputFactory` that
creates it for a destination writer. For scheduled runs, the launcher opens the
per-run log file and passes it to `NewOutput`; for launches without a schedule,
it uses `io.Discard`.
