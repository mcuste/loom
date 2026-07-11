# Launcher

`launcher` turns a workflow reference into a run. Both `loom run` and the
daemon use it to load a workflow, check its parameters, and build a
`runner.Request`.

```text
loom run                         loom daemon
   |                                  |
   v                                  v
launcher.Launcher.Prepare      scheduler finds a due schedule
   |                                  |
   |                                  v
   |                            launcher.Runner.Launch
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
CLI prints the plan       scheduler opens a log file
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

The scheduler only needs to say “run this workflow with these parameters.” It
does not need to know how workflows are loaded or checked. `Runner` is the
small interface that keeps that boundary clear and makes scheduler tests simple.

The CLI calls `Prepare` directly so it can print the plan before starting the
run. Scheduled runs call `Launch`, which prepares the request, writes output to
one log file per fire, and passes schedule details to the runner.
